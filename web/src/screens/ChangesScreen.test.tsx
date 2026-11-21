import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { clock, type ChangeState } from '../changes';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { render, screen, within } from '../test/render';
import type { Apply, Session } from '../types';
import { ChangesScreen } from './ChangesScreen';

const session = (capabilities: string[]): Session => ({
  account: { id: '0657cce4-d4f9-4d1e-9cae-53a99855bb10', login: 'admin', displayName: 'Administrator' },
  capabilities, csrfToken: 'csrf', expiresAt: '2026-11-16T12:00:00Z', idleTimeoutSeconds: 1800,
});
const admin = session(['configuration.read', 'configuration.apply', 'configuration.confirm']);
const apply = (fields: Partial<Apply>): Apply => ({ id: '1', savedVersion: '3', state: 'confirmed', previous: null, reason: '', createdAt: '2026-11-21T10:00:00Z', confirmTimeoutSeconds: 120, files: [], ...fields });
const pointers = { confirmed: '1', lastKnownGood: null, pending: null, live: '1' };
const unapplied: ChangeState = { applies: { pointers, items: [apply({})] }, head: '5' };

const info = (id: string) => ({ id, createdAt: '2026-11-21T11:00:00Z', actor: 'admin', changedObjectCount: 1 });
const declaration = (zones: Array<{ id: string; name: string }>) => ({ schemaVersion: 3, links: [], segments: [], zones, roles: [] });
const preview = (findings: Array<Record<string, string>> = []) => ({ state: 'preview', savedVersion: '5', realizable: !findings.some((finding) => finding.severity === 'error'), resolved: [], files: [], findings });

function routes(extra: Record<string, unknown> = {}) {
  return fetchRoutes({
    '/api/v1/versions?': { state: 'saved', items: [info('5'), info('4'), info('3'), info('2')] },
    '/api/v1/versions/3': { ...info('3'), state: 'saved', value: declaration([{ id: 'zon_a', name: 'LAN' }, { id: 'zon_b', name: 'Guest' }]), changes: [] },
    '/api/v1/versions/4': { ...info('4'), state: 'saved', value: declaration([{ id: 'zon_a', name: 'LAN' }, { id: 'zon_c', name: 'DMZ' }]), changes: [{ collection: 'zones', id: 'zon_b', deleted: true }, { collection: 'zones', id: 'zon_c', created: true }] },
    '/api/v1/versions/5': { ...info('5'), state: 'saved', value: declaration([{ id: 'zon_a', name: 'Inside' }, { id: 'zon_c', name: 'DMZ' }]), changes: [{ collection: 'zones', id: 'zon_a' }] },
    '/api/v1/preview/network': preview(),
    ...extra,
  });
}

function show(state: ChangeState, options: { connected?: boolean; who?: Session } = {}) {
  const onChanged = vi.fn();
  render(<ChangesScreen api={new ApiClient('csrf')} session={options.who ?? admin} state={state} stateError="" connected={options.connected ?? true} onChanged={onChanged} onSessionLost={vi.fn()} />);
  return onChanged;
}

it('shows each changed object with its live and saved value', async () => {
  routes();
  show(unapplied);

  const created = await screen.findByLabelText('DMZ created');
  expect(created).toHaveTextContent('Not present');
  expect(created).toHaveTextContent('"name": "DMZ"');
  const changed = screen.getByLabelText('Inside changed');
  expect(changed).toHaveTextContent('"name": "LAN"');
  expect(changed).toHaveTextContent('"name": "Inside"');
  const deleted = screen.getByLabelText('Guest deleted');
  expect(deleted).toHaveTextContent('"name": "Guest"');
  expect(deleted).toHaveTextContent('Deleted');
  expect(screen.getByText('Saved version 5 by admin', { exact: false })).toBeInTheDocument();
  expect(screen.queryByText('Saved version 3 by admin', { exact: false })).not.toBeInTheDocument();
  expect(screen.getByText('Apply 1, saved version 3: Confirmed.')).toBeInTheDocument();
});

it('applies the reviewed version with the chosen confirmation window', async () => {
  const fetchMock = routes({ 'POST /api/v1/applies': apply({ id: '2', savedVersion: '5', state: 'awaiting_confirmation', deadline: '2026-11-21T12:05:00Z' }) });
  const onChanged = show(unapplied);

  await userEvent.click(await screen.findByLabelText('300 seconds'));
  await userEvent.click(screen.getByRole('button', { name: 'Apply saved version 5' }));
  const post = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST');
  expect(JSON.parse(String(post?.[1]?.body))).toEqual({ savedVersion: '5', confirmTimeoutSeconds: 300 });
  expect(await screen.findByText('Apply 2: Awaiting confirmation.')).toBeInTheDocument();
  expect(onChanged).toHaveBeenCalled();
});

it('reloads the review when the saved state moved', async () => {
  const fetchMock = routes({ 'POST /api/v1/applies': () => jsonResponse(409, { error: { code: 'CONFLICT', message: 'The saved head moved.' } }) });
  const onChanged = show(unapplied);

  await userEvent.click(await screen.findByRole('button', { name: 'Apply saved version 5' }));
  expect(await screen.findByText(/The saved configuration changed since this review/)).toBeInTheDocument();
  expect(onChanged).toHaveBeenCalled();
  expect(fetchMock.mock.calls.filter(([url]) => String(url).startsWith('/api/v1/versions?'))).toHaveLength(2);
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

const awaiting: ChangeState = {
  applies: { pointers: { ...pointers, live: '2', pending: '2' }, items: [apply({ id: '2', savedVersion: '5', state: 'awaiting_confirmation', deadline: '2026-11-21T12:05:00Z' }), apply({})] },
  head: '5',
};

it('confirms an apply awaiting confirmation and reports the server answer', async () => {
  const fetchMock = routes({ 'POST /api/v1/applies/2/confirmation': apply({ id: '2', savedVersion: '5', state: 'confirmed' }) });
  const onChanged = show(awaiting);

  expect(screen.getByText(/The box rolls back at/)).toHaveTextContent(clock('2026-11-21T12:05:00Z'));
  expect(screen.getByRole('button', { name: 'Roll back' })).toBeInTheDocument();
  expect(await screen.findByText('Nothing to apply: every saved change is live.')).toBeInTheDocument();
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }));
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/applies/2/confirmation', expect.objectContaining({ method: 'POST' }));
  expect(await screen.findByText('Apply 2: Confirmed.')).toBeInTheDocument();
  expect(onChanged).toHaveBeenCalled();
});

it('rolls back an apply awaiting confirmation', async () => {
  const fetchMock = routes({ 'POST /api/v1/applies/2/rollback': apply({ id: '2', savedVersion: '5', state: 'rolled_back', reason: 'Rolled back by admin.' }) });
  show(awaiting);

  await userEvent.click(screen.getByRole('button', { name: 'Roll back' }));
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/applies/2/rollback', expect.objectContaining({ method: 'POST' }));
  expect(await screen.findByText('Apply 2: Rolled back. Rolled back by admin.')).toBeInTheDocument();
});

it('says the state is unknown when the connection is lost while awaiting confirmation', async () => {
  routes();
  show(awaiting, { connected: false });

  expect(await screen.findByRole('alert')).toHaveTextContent(`Connection lost: the box will roll back at ${clock('2026-11-21T12:05:00Z')} unless this apply is confirmed. Its state is unknown until the box answers again.`);
  expect(screen.queryByRole('button', { name: 'Confirm' })).not.toBeInTheDocument();
});

it('shows the review read-only without the apply and confirm capabilities', async () => {
  routes();
  show(unapplied, { who: session(['configuration.read']) });

  expect(await screen.findByLabelText('DMZ created')).toBeInTheDocument();
  expect(screen.getByText('Your account can review changes but not apply them.')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /Apply saved version/ })).not.toBeInTheDocument();
});

it('says the factory network runs when nothing was applied', async () => {
  routes({ '/api/v1/versions?': { state: 'saved', items: [info('0')] } });
  show({ applies: { pointers: { confirmed: null, lastKnownGood: null, pending: null, live: null }, items: [] }, head: '0' });

  expect(screen.getByText('Nothing has been applied; the box runs its factory network.')).toBeInTheDocument();
  expect(await screen.findByText('Nothing to apply: every saved change is live.')).toBeInTheDocument();
  expect(screen.getByText('No apply yet.')).toBeInTheDocument();
});
