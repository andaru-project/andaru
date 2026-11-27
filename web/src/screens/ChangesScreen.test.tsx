import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { clock, type ChangeState } from '../changes';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { apply, pointers, session } from '../test/fixtures';
import { render, screen, within } from '../test/render';
import type { Session } from '../types';
import { ChangesScreen } from './ChangesScreen';

const admin = session(['configuration.read', 'configuration.apply', 'configuration.confirm']);
const live = { pointers: { ...pointers, confirmed: '1', live: '1' }, unapplied: true, items: [apply({})] };
const unapplied: ChangeState = { applies: live, head: '5' };

const info = (id: string) => ({ id, createdAt: '2026-11-21T11:00:00Z', actor: 'admin', changedObjectCount: 1 });
const declaration = (zones: Array<{ id: string; name: string; description?: string }>) => ({ schemaVersion: 3, links: [], segments: [], zones, roles: [] });
const preview = (findings: Array<Record<string, string>> = []) => ({ state: 'preview', savedVersion: '5', realizable: !findings.some((finding) => finding.severity === 'error'), resolved: [], files: [], findings });
const history = (...ids: string[]) => ({ state: 'saved', items: ids.map(info) });
const failure = (status: number, code: string, message: string, extra = {}) => () => jsonResponse(status, { error: { code, message, details: [], ...extra } });

function routes(extra: Record<string, unknown> = {}) {
  return fetchRoutes({
    '/api/v1/applies': live,
    '/api/v1/versions?': history('5', '4', '3', '2'),
    '/api/v1/versions/3': { ...info('3'), state: 'saved', value: declaration([{ id: 'zon_a', name: 'LAN' }, { id: 'zon_b', name: 'Guest' }]), changes: [] },
    '/api/v1/versions/4': { ...info('4'), state: 'saved', value: declaration([{ id: 'zon_a', name: 'LAN' }, { id: 'zon_c', name: 'DMZ' }]), changes: [{ collection: 'zones', id: 'zon_b', deleted: true }, { collection: 'zones', id: 'zon_c', created: true }] },
    '/api/v1/versions/5': { ...info('5'), state: 'saved', value: declaration([{ id: 'zon_a', name: 'Inside', description: 'Home' }, { id: 'zon_c', name: 'DMZ' }]), changes: [{ collection: 'zones', id: 'zon_a' }] },
    '/api/v1/versions/6': { ...info('6'), state: 'saved', value: declaration([{ id: 'zon_a', name: 'Inside' }, { id: 'zon_c', name: 'Servers' }]), changes: [{ collection: 'zones', id: 'zon_c' }] },
    '/api/v1/preview/network': preview(),
    ...extra,
  });
}

// The shell passes the same client and callbacks on every render; so does show's rerender.
function show(state: ChangeState, options: { connected?: boolean; who?: Session } = {}) {
  const api = new ApiClient('csrf');
  const onChanged = vi.fn();
  const onSessionLost = vi.fn();
  const screenFor = (next: ChangeState, connected: boolean) => <ChangesScreen api={api} session={options.who ?? admin} state={next} stateError="" connected={connected} onChanged={onChanged} onSessionLost={onSessionLost} />;
  const result = render(screenFor(state, options.connected ?? true));
  return { onChanged, rerender: (next: ChangeState, connected = true) => result.rerender(screenFor(next, connected)) };
}

const versionLists = (fetchMock: ReturnType<typeof fetchRoutes>) => fetchMock.mock.calls.filter(([url]) => String(url).startsWith('/api/v1/versions?'));

it('shows each changed object with its live and saved value', async () => {
  routes();
  show(unapplied);
  // Before the review loads, too: the shell already knows a newer version is saved.
  expect(screen.queryByRole('status', { name: 'Confirmed' })).not.toBeInTheDocument();

  const created = await screen.findByLabelText('DMZ created');
  expect(created).toHaveTextContent('"name": "DMZ"');
  // A changed object lists only the fields that differ, old beside new.
  const changed = screen.getByLabelText('Inside changed');
  expect(within(changed).getByRole('row', { name: 'name "LAN" "Inside"' })).toBeInTheDocument();
  expect(changed).not.toHaveTextContent('"id"');
  expect(within(changed).getByRole('row', { name: 'description Not set "Home"' })).toBeInTheDocument();
  const deleted = screen.getByLabelText('Guest deleted');
  expect(deleted).toHaveTextContent('"name": "Guest"');
  expect(screen.getByText('Saved version 5 by admin', { exact: false })).toBeInTheDocument();
  expect(screen.queryByText('Saved version 3 by admin', { exact: false })).not.toBeInTheDocument();
  expect(screen.getByText('Apply 1, saved version 3: Confirmed.')).toBeInTheDocument();
  // Version 4 changed two objects and version 5 one; the summary counts them and names no raw ID.
  expect(screen.getByText('2 changed objects')).toBeInTheDocument();
  expect(screen.getByText('1 changed object')).toBeInTheDocument();
  // The confirmed banner would contradict the pending changes below it.
  expect(screen.queryByRole('status', { name: 'Confirmed' })).not.toBeInTheDocument();
});

it('applies the reviewed version with the chosen confirmation window', async () => {
  const fetchMock = routes({ 'POST /api/v1/applies': apply({ id: '2', savedVersion: '5', state: 'awaiting_confirmation', deadline: '2026-11-21T12:05:00Z' }) });
  const { onChanged } = show(unapplied);

  await userEvent.click(await screen.findByLabelText('300 seconds'));
  await userEvent.click(screen.getByRole('button', { name: 'Apply saved version 5' }));
  const post = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST');
  expect(JSON.parse(String(post?.[1]?.body))).toEqual({ savedVersion: '5', confirmTimeoutSeconds: 300 });
  expect(await screen.findByText('Apply 2: Awaiting confirmation.')).toBeInTheDocument();
  expect(onChanged).toHaveBeenCalled();
});

it('applies the version it reviewed when that differs from the head the shell last saw', async () => {
  const fetchMock = routes({ '/api/v1/versions?': history('6', '5', '4', '3'), 'POST /api/v1/applies': apply({ id: '2', savedVersion: '6', state: 'awaiting_confirmation' }) });
  show(unapplied);

  await userEvent.click(await screen.findByRole('button', { name: 'Apply saved version 6' }));
  const post = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST');
  expect(JSON.parse(String(post?.[1]?.body))).toMatchObject({ savedVersion: '6' });
});

it('keeps the review on the version it loaded until reloaded', async () => {
  let head = '5';
  const fetchMock = routes({ '/api/v1/versions?': () => jsonResponse(200, head === '5' ? history('5', '4', '3') : history('6', '5', '4', '3')) });
  const { rerender } = show(unapplied);
  expect(await screen.findByRole('button', { name: 'Apply saved version 5' })).toBeEnabled();

  head = '6';
  rerender({ ...unapplied, head: '6' });
  expect(screen.getByRole('button', { name: 'Apply saved version 5' })).toBeDisabled();
  expect(screen.getByRole('status', { name: 'Saved version 6 arrived since this review.' })).toBeInTheDocument();
  expect(versionLists(fetchMock)).toHaveLength(1);

  await userEvent.click(screen.getByRole('button', { name: 'Reload' }));
  expect(await screen.findByRole('button', { name: 'Apply saved version 6' })).toBeEnabled();
  expect(screen.queryByText('Saved version 6 arrived since this review.')).not.toBeInTheDocument();
});

it('reloads the review when the server says the saved head moved', async () => {
  const fetchMock = routes({ 'POST /api/v1/applies': failure(412, 'PRECONDITION_FAILED', 'Only the saved head can be applied.') });
  const { onChanged } = show(unapplied);

  await userEvent.click(await screen.findByRole('button', { name: 'Apply saved version 5' }));
  expect(await screen.findByText(/The saved configuration changed since this review/)).toBeInTheDocument();
  expect(onChanged).toHaveBeenCalled();
  expect(versionLists(fetchMock)).toHaveLength(2);
});

it('reports any other refusal as the server words it, without a reload', async () => {
  const fetchMock = routes({ 'POST /api/v1/applies': failure(409, 'APPLY_IN_PROGRESS', 'Apply 7 is awaiting confirmation; confirm it or roll it back first.') });
  show(unapplied);

  await userEvent.click(await screen.findByRole('button', { name: 'Apply saved version 5' }));
  expect(await screen.findByText('Apply 7 is awaiting confirmation; confirm it or roll it back first.')).toBeInTheDocument();
  expect(screen.queryByText(/changed since this review/)).not.toBeInTheDocument();
  expect(versionLists(fetchMock)).toHaveLength(1);
});

it('lists the findings of a refused apply', async () => {
  routes({ 'POST /api/v1/applies': failure(422, 'APPLY_REFUSED', 'This configuration cannot be applied to this host as it stands.', {
    findings: [{ severity: 'error', code: 'WOULD_UNCONFIGURE', path: 'enp1s0', message: 'Applying would unconfigure enp1s0.' }],
  }) });
  show(unapplied);

  await userEvent.click(await screen.findByRole('button', { name: 'Apply saved version 5' }));
  expect(await screen.findByText('This configuration cannot be applied to this host as it stands.')).toBeInTheDocument();
  const refused = screen.getByRole('alert', { name: 'The engine refused the apply' });
  expect(refused).toHaveTextContent('Applying would unconfigure enp1s0.');
  expect(within(refused).getByText('enp1s0')).toBeInTheDocument();
});

it('keeps Apply disabled when a reload of the review fails', async () => {
  let reachable = true;
  routes({
    '/api/v1/versions?': () => (reachable ? jsonResponse(200, history('5', '4', '3')) : jsonResponse(503, { error: { code: 'UNAVAILABLE', message: 'The store is busy.' } })),
    'POST /api/v1/applies': () => { reachable = false; return jsonResponse(412, { error: { code: 'PRECONDITION_FAILED', message: 'Only the saved head can be applied.' } }); },
  });
  show(unapplied);

  await userEvent.click(await screen.findByRole('button', { name: 'Apply saved version 5' }));
  expect(await screen.findByText('The store is busy.')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Apply saved version 5' })).toBeDisabled();
});

it('shows only the error when the review cannot load', async () => {
  routes({ '/api/v1/versions?': failure(503, 'UNAVAILABLE', 'The store is busy.') });
  show(unapplied);

  expect(await screen.findByText('The store is busy.')).toBeInTheDocument();
  expect(screen.queryByText(/Nothing to apply/)).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /Apply saved version/ })).not.toBeInTheDocument();
});

it('disables Apply while the apply runs', async () => {
  routes({ 'POST /api/v1/applies': () => new Promise<Response>(() => {}) });
  show(unapplied);

  await userEvent.click(await screen.findByRole('button', { name: 'Apply saved version 5' }));
  expect(screen.getByRole('button', { name: 'Apply saved version 5' })).toBeDisabled();
  expect(screen.getByRole('status', { name: 'Applying' })).toBeInTheDocument();
});

it('blocks the apply on a preview error and lists its path', async () => {
  routes({ '/api/v1/preview/network': preview([
    { severity: 'error', code: 'HARDWARE_MISSING', path: '/links/0/permanentMac', message: 'No interface with this permanent MAC is present.' },
    { severity: 'warning', code: 'DNS_NOT_APPLIED', path: '', message: 'DNS is not applied.' },
  ]) });
  show(unapplied);

  const blocking = await screen.findByRole('alert', { name: 'These problems block the apply' });
  expect(blocking).toHaveTextContent('No interface with this permanent MAC is present.');
  expect(within(blocking).getByText('/links/0/permanentMac')).toBeInTheDocument();
  expect(screen.getByText('DNS is not applied.')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Apply saved version 5' })).toBeDisabled();
});

const awaitingApplies = { pointers: { ...pointers, confirmed: '1', live: '2', pending: '2' }, unapplied: false, items: [apply({ id: '2', savedVersion: '5', state: 'awaiting_confirmation', deadline: '2026-11-21T12:05:00Z' }), apply({})] };
const awaiting: ChangeState = { applies: awaitingApplies, head: '5' };

it('confirms an apply awaiting confirmation and reports the server answer', async () => {
  const fetchMock = routes({ '/api/v1/applies': awaitingApplies, 'POST /api/v1/applies/2/confirmation': apply({ id: '2', savedVersion: '5', state: 'confirmed' }) });
  const { onChanged } = show(awaiting);

  expect(screen.getByText(/The box rolls back at/)).toHaveTextContent(clock('2026-11-21T12:05:00Z'));
  expect(screen.getByRole('button', { name: 'Roll back' })).toBeInTheDocument();
  expect(await screen.findByText('Nothing to apply: every saved change is live.')).toBeInTheDocument();
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }));
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/applies/2/confirmation', expect.objectContaining({ method: 'POST' }));
  expect(await screen.findByText('Apply 2: Confirmed.')).toBeInTheDocument();
  expect(onChanged).toHaveBeenCalled();
});

it('rolls back an apply awaiting confirmation', async () => {
  const fetchMock = routes({ '/api/v1/applies': awaitingApplies, 'POST /api/v1/applies/2/rollback': apply({ id: '2', savedVersion: '5', state: 'rolled_back', reason: 'Rolled back by admin.' }) });
  show(awaiting);

  await userEvent.click(screen.getByRole('button', { name: 'Roll back' }));
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/applies/2/rollback', expect.objectContaining({ method: 'POST' }));
  expect(await screen.findByText('Apply 2: Rolled back. Rolled back by admin.')).toBeInTheDocument();
});

it('says the state is unknown when the connection is lost while awaiting confirmation, and still offers the actions', async () => {
  routes({ '/api/v1/applies': awaitingApplies });
  show(awaiting, { connected: false });

  expect(await screen.findByRole('alert', { name: 'Connection lost' })).toHaveTextContent(`Connection lost: the box will roll back at ${clock('2026-11-21T12:05:00Z')} unless this apply is confirmed. Its state is unknown until the box answers again.`);
  expect(screen.getByRole('button', { name: 'Confirm' })).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Roll back' })).toBeInTheDocument();
});

it('shows the review read-only without the apply and confirm capabilities', async () => {
  routes();
  show(unapplied, { who: session(['configuration.read']) });

  expect(await screen.findByLabelText('DMZ created')).toBeInTheDocument();
  expect(screen.getByText('Your account can review changes but not apply them.')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /Apply saved version/ })).not.toBeInTheDocument();
});

it('says the factory network runs when nothing was applied', async () => {
  const none = { pointers, unapplied: false, items: [] };
  routes({ '/api/v1/applies': none, '/api/v1/versions?': history('0') });
  show({ applies: none, head: '0' });

  expect(screen.getByText('Nothing has been applied; the box runs its factory network.')).toBeInTheDocument();
  expect(await screen.findByText('Nothing to apply: every saved change is live.')).toBeInTheDocument();
  expect(screen.getByText('No apply yet.')).toBeInTheDocument();
});

it('lists the last 20 applies', async () => {
  const many = { pointers: { ...pointers, confirmed: '25', live: '25' }, unapplied: false, items: Array.from({ length: 25 }, (_, index) => apply({ id: String(25 - index), savedVersion: '5' })) };
  routes({ '/api/v1/applies': many });
  show({ applies: many, head: '5' });

  const table = screen.getByRole('table', { name: 'Apply history' });
  expect(within(table).getAllByRole('row')).toHaveLength(21);
  expect(await screen.findByText('Nothing to apply: every saved change is live.')).toBeInTheDocument();
});

const refusal = failure(422, 'APPLY_REFUSED', 'This configuration cannot be applied to this host as it stands.', {
  findings: [{ severity: 'error', code: 'WOULD_UNCONFIGURE', path: 'enp1s0', message: 'Applying would unconfigure enp1s0.' }],
});

it('is not stale when the review saw a newer apply than the shell last polled', async () => {
  // Another session applied version 4 after the shell's last poll, which still shows apply 1 of version 3.
  const reapplied = { pointers: { ...pointers, confirmed: '2', live: '2' }, unapplied: true, items: [apply({ id: '2', savedVersion: '4' }), apply({})] };
  routes({ '/api/v1/applies': reapplied });
  show(unapplied);

  expect(await screen.findByRole('button', { name: 'Apply saved version 5' })).toBeEnabled();
  expect(screen.queryByText(/since this review/)).not.toBeInTheDocument();
  expect(screen.queryByLabelText('DMZ created')).not.toBeInTheDocument();
});

it('is stale when the shell saw a newer apply, and Reload refreshes the shell too', async () => {
  routes();
  const { onChanged } = show({ applies: { ...live, items: [apply({ id: '2', savedVersion: '3', state: 'rolled_back' }), apply({})] }, head: '5' });

  expect(await screen.findByRole('status', { name: 'The live configuration changed since this review.' })).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Apply saved version 5' })).toBeDisabled();
  await userEvent.click(screen.getByRole('button', { name: 'Reload' }));
  expect(onChanged).toHaveBeenCalled();
});

it('offers a reload when the review cannot load', async () => {
  let reachable = false;
  routes({ '/api/v1/versions?': () => (reachable ? jsonResponse(200, history('5', '4', '3')) : jsonResponse(503, { error: { code: 'UNAVAILABLE', message: 'The store is busy.' } })) });
  show(unapplied);

  const failed = await screen.findByRole('alert', { name: 'Review request failed' });
  reachable = true;
  await userEvent.click(within(failed).getByRole('button', { name: 'Reload' }));
  expect(await screen.findByRole('button', { name: 'Apply saved version 5' })).toBeEnabled();
});

it('reloads the review when the connection comes back, dropping old answers', async () => {
  const fetchMock = routes({ 'POST /api/v1/applies': refusal });
  const { rerender } = show(unapplied);

  await userEvent.click(await screen.findByRole('button', { name: 'Apply saved version 5' }));
  expect(await screen.findByText('Applying would unconfigure enp1s0.')).toBeInTheDocument();
  rerender(unapplied, false);
  rerender(unapplied, true);
  expect(versionLists(fetchMock)).toHaveLength(2);
  expect(screen.queryByText('Applying would unconfigure enp1s0.')).not.toBeInTheDocument();
  expect(screen.queryByText('This configuration cannot be applied to this host as it stands.')).not.toBeInTheDocument();
});

it('drops the findings of a refused apply on confirm or roll back', async () => {
  routes({ 'POST /api/v1/applies': refusal, 'POST /api/v1/applies/2/confirmation': apply({ id: '2', savedVersion: '5', state: 'confirmed' }) });
  const { rerender } = show(unapplied);

  await userEvent.click(await screen.findByRole('button', { name: 'Apply saved version 5' }));
  expect(await screen.findByText('Applying would unconfigure enp1s0.')).toBeInTheDocument();
  rerender(awaiting);
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }));
  expect(await screen.findByText('Apply 2: Confirmed.')).toBeInTheDocument();
  expect(screen.queryByText('Applying would unconfigure enp1s0.')).not.toBeInTheDocument();
});

it('treats immediate-effect saves (unapplied: false) as already live and skips preview and Apply', async () => {
  const synced = { pointers: { ...pointers, confirmed: '1', live: '1' }, unapplied: false, items: [apply({})] };
  const fetchMock = routes({ '/api/v1/applies': synced });
  show({ applies: synced, head: '5' });

  expect(screen.getByRole('status', { name: 'Confirmed' })).toHaveTextContent('Apply 1 of saved version 3 is confirmed.');
  expect(await screen.findByText('Nothing to apply: every saved change is live.')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /Apply saved version/ })).not.toBeInTheDocument();
  expect(fetchMock.mock.calls.some(([url]) => String(url).startsWith('/api/v1/preview/network'))).toBe(false);
});
