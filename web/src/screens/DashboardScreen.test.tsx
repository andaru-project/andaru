import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import type { ChangeState } from '../changes';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { apply, pointers, session } from '../test/fixtures';
import { act, render, screen, waitFor, within } from '../test/render';
import type { Resource } from '../useResource';
import { DashboardScreen } from './DashboardScreen';

const link = (name: string, operState: string, addresses: Array<[string, number, string]>) => ({
  ifIndex: 2, name, linkType: name === 'lo' ? 'loopback' : 'ether', mtu: 1500, operState, flags: [],
  addresses: addresses.map(([address, prefixLength, scope]) => ({ family: 'ipv4', address, prefixLength, scope })),
});

const settled: ChangeState = { applies: { pointers: { ...pointers, live: '1' }, items: [apply({})] }, head: '3' };
const latest = (fields: Parameters<typeof apply>[0]): ChangeState => ({ applies: { pointers: { ...pointers, live: '1' }, items: [apply({ id: '2', ...fields }), apply({})] }, head: '3' });

function mount(capabilities: string[], changes: Partial<Resource<ChangeState | undefined>> = { data: settled }) {
  const resource = { error: '', loading: false, reload: vi.fn(), refresh: vi.fn(), ...changes };
  render(<MemoryRouter><DashboardScreen api={new ApiClient('csrf')} session={session(capabilities)} changes={resource} onSessionLost={vi.fn()} /></MemoryRouter>);
}

afterEach(() => vi.useRealTimers());

it('shows each widget its own data, and a widget that fails does not blank the others', async () => {
  fetchRoutes({
    '/api/v1/status/links': { items: [link('lo', 'UNKNOWN', []), link('ens18', 'UP', [['10.30.0.100', 24, 'global'], ['fe80::1', 64, 'link']])] },
    '/api/v1/status/prefixes': () => jsonResponse(503, { error: { code: 'INVENTORY_UNAVAILABLE', message: 'The delegated prefixes are unavailable.', details: [] } }),
  });
  mount(['configuration.read', 'status.read']);

  const interfaces = screen.getByRole('region', { name: 'Interfaces' });
  expect(await within(interfaces).findByText('ens18')).toBeInTheDocument();
  expect(within(interfaces).getByText('10.30.0.100/24')).toBeInTheDocument();
  // The loopback and link-local addresses are noise on a dashboard.
  expect(within(interfaces).queryByText('lo')).not.toBeInTheDocument();
  expect(within(interfaces).queryByText('fe80::1/64')).not.toBeInTheDocument();

  expect(await within(screen.getByRole('region', { name: 'IPv6 prefixes' })).findByRole('alert')).toHaveTextContent('The delegated prefixes are unavailable.');
  expect(within(screen.getByRole('region', { name: 'Changes' })).getByText(/Nothing is pending/)).toBeInTheDocument();
  // A widget's title leads to the page with the full view.
  expect(screen.getByRole('link', { name: 'Changes' })).toHaveAttribute('href', '/changes');
});

it('shows only the widgets the capabilities allow', () => {
  fetchRoutes({});
  mount(['configuration.read']);
  expect(screen.getAllByRole('region').map((region) => region.getAttribute('aria-labelledby'))).toEqual(['widget-changes']);
});

it('says so when the account can see no widget', () => {
  fetchRoutes({});
  mount(['tokens.manageSelf']);
  expect(screen.getByRole('status')).toHaveTextContent('none of the dashboard');
});

it.each<[string, ChangeState, RegExp]>([
  ['saved changes that are not applied', { applies: { pointers, items: [] }, head: '4' }, /^Saved changes are not applied yet\.$/],
  ['an apply in progress', latest({ state: 'activating' }), /^Applying saved version 3…$/],
  ['an apply that failed', latest({ state: 'failed' }), /^The last apply failed\.$/],
  ['an apply that was rolled back', latest({ state: 'rolled_back' }), /^The last apply was rolled back\.$/],
  ['an apply awaiting confirmation', latest({ state: 'awaiting_confirmation', deadline: '2099-01-01T00:00:00Z' }), /^Applied, awaiting confirmation: rolls back at \d\d:\d\d:\d\d \(.+ left\)\.$/],
])('reports %s', (_, state, text) => {
  fetchRoutes({});
  mount(['configuration.read'], { data: state });
  expect(within(screen.getByRole('region', { name: 'Changes' })).getByText(text)).toBeInTheDocument();
});

it('keeps the last change state beside the error when its poll fails', () => {
  fetchRoutes({});
  mount(['configuration.read'], { data: settled, error: 'The box does not answer.' });
  const changes = screen.getByRole('region', { name: 'Changes' });
  expect(within(changes).getByRole('alert')).toHaveTextContent('The box does not answer.');
  expect(within(changes).getByText(/Nothing is pending/)).toBeInTheDocument();
});

it('lists a delegated prefix and where it went', async () => {
  fetchRoutes({
    '/api/v1/status/links': { items: [] },
    '/api/v1/status/prefixes': { wans: [{ interface: 'ens18', current: { prefix: '2001:db8:30::/56' }, segments: [{ interface: 'ens19', subnetId: '1', prefix: '2001:db8:30:1::/64' }], pools: [{ interface: 'ens19', subnetId: '1', prefix: '2001:db8:30:10::/60' }], notFitting: ['guest'] }] },
  });
  mount(['status.read']);
  const prefixes = screen.getByRole('region', { name: 'IPv6 prefixes' });
  expect(await within(prefixes).findByText('2001:db8:30::/56')).toBeInTheDocument();
  expect(within(prefixes).getByText('2001:db8:30:1::/64 on ens19')).toBeInTheDocument();
  expect(within(prefixes).getByText('2001:db8:30:10::/60 on ens19')).toBeInTheDocument();
  expect(within(prefixes).getByText('No room for: guest')).toBeInTheDocument();
});

it('polls a widget every 10 seconds, and waits for an answer that is still out', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  let answer: (response: Response) => void = () => undefined;
  const fetchMock = fetchRoutes({
    '/api/v1/status/links': () => new Promise<Response>((resolve) => { answer = resolve; }),
    '/api/v1/status/prefixes': { wans: [] },
  });
  const reads = () => fetchMock.mock.calls.filter(([url]) => url === '/api/v1/status/links').length;
  mount(['status.read']);
  expect(reads()).toBe(1);

  // The first answer is slower than the poll: asking again would throw it away when it comes.
  act(() => { vi.advanceTimersByTime(30_000); });
  expect(reads()).toBe(1);
  await act(async () => { answer(jsonResponse(200, { items: [link('ens18', 'UP', [])] })); await Promise.resolve(); });
  expect(await screen.findByText('ens18')).toBeInTheDocument();

  act(() => { vi.advanceTimersByTime(9_999); });
  expect(reads()).toBe(1);
  act(() => { vi.advanceTimersByTime(1); });
  expect(reads()).toBe(2);
});

const everything = ['configuration.read', 'status.read'];
const titles = () => screen.getAllByRole('heading', { level: 3 }).map((heading) => heading.textContent);
const saves = (fetchMock: ReturnType<typeof fetchRoutes>) => fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT').map(([, init]) => JSON.parse(String(init?.body)) as unknown);

// layoutRoutes answers the dashboard's reads and stores what it is sent, as the engine does.
function layoutRoutes(preferences: object | Response, save: () => Response | undefined = () => undefined) {
  return fetchRoutes({
    '/api/v1/session/preferences': () => (preferences instanceof Response ? preferences : jsonResponse(200, preferences)),
    'PUT /api/v1/session/preferences': (init) => save() ?? jsonResponse(200, JSON.parse(String(init?.body))),
    '/api/v1/status/links': { items: [] },
    '/api/v1/status/prefixes': { wans: [] },
  });
}

async function unlock() {
  const padlock = await screen.findByRole('button', { name: 'Layout locked' });
  await waitFor(() => expect(padlock).toBeEnabled());
  await userEvent.click(padlock);
  expect(screen.getByRole('button', { name: 'Layout unlocked' })).toHaveAttribute('aria-pressed', 'true');
}

it('applies the stored layout: its order, its hidden widgets, and a widget new since it was saved', async () => {
  // "gone" is a widget of another version. "interfaces" is not named, so it joins at the end.
  layoutRoutes({ dashboard: { order: ['prefixes', 'gone', 'changes'], hidden: ['changes'] } });
  mount(everything);
  await waitFor(() => expect(titles()).toEqual(['IPv6 prefixes', 'Interfaces']));
});

it('stays locked until asked: nothing moves or hides by accident', async () => {
  layoutRoutes({ dashboard: { hidden: ['changes'] } });
  mount(everything);
  await waitFor(() => expect(titles()).toEqual(['Interfaces', 'IPv6 prefixes']));
  expect(screen.queryByRole('button', { name: /^(Move|Hide|Show) / })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Reset layout' })).not.toBeInTheDocument();

  await unlock();
  for (const name of ['Move Interfaces', 'Hide Interfaces', 'Move IPv6 prefixes', 'Hide IPv6 prefixes', 'Show Changes', 'Reset layout']) expect(screen.getByRole('button', { name })).toBeInTheDocument();
  expect(within(screen.getByRole('region', { name: 'Hidden widgets' })).getByText('Changes')).toBeInTheDocument();
});

it('hides and shows a widget, saving the whole preferences and keeping what it does not know', async () => {
  const fetchMock = layoutRoutes({ another: { version: 'keeps this' }, dashboard: {} });
  mount(everything);
  await unlock();

  await userEvent.click(screen.getByRole('button', { name: 'Hide Interfaces' }));
  expect(titles()).toEqual(['Changes', 'IPv6 prefixes', 'Hidden widgets']);
  await userEvent.click(screen.getByRole('button', { name: 'Show Interfaces' }));
  expect(titles()).toEqual(['Changes', 'Interfaces', 'IPv6 prefixes']);

  const order = ['changes', 'interfaces', 'prefixes'];
  expect(saves(fetchMock)).toEqual([
    { another: { version: 'keeps this' }, dashboard: { order, hidden: ['interfaces'] } },
    { another: { version: 'keeps this' }, dashboard: { order, hidden: [] } },
  ]);
});

it('resets to the default layout', async () => {
  const fetchMock = layoutRoutes({ dashboard: { order: ['prefixes', 'interfaces', 'changes'], hidden: ['changes'] } });
  mount(everything);
  await unlock();
  await userEvent.click(screen.getByRole('button', { name: 'Reset layout' }));
  expect(titles()).toEqual(['Changes', 'Interfaces', 'IPv6 prefixes']);
  expect(saves(fetchMock)).toEqual([{ dashboard: {} }]);
});

it('says so when every widget is hidden', async () => {
  layoutRoutes({ dashboard: { hidden: ['changes', 'interfaces', 'prefixes'] } });
  mount(everything);
  expect(await screen.findByText(/Every widget is hidden/)).toBeInTheDocument();
});

it('keeps the layout locked when the preferences cannot be read: a save would replace what it has not seen', async () => {
  const fetchMock = layoutRoutes(jsonResponse(503, { error: { code: 'STORE_UNAVAILABLE', message: 'Preferences are temporarily unavailable.', details: [] } }));
  mount(everything);
  expect(await screen.findByText(/The layout cannot be changed now: Preferences are temporarily unavailable\./)).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Layout locked' })).toBeDisabled();
  expect(titles()).toEqual(['Changes', 'Interfaces', 'IPv6 prefixes']);
  expect(saves(fetchMock)).toEqual([]);
});

it('says when a change was not saved', async () => {
  layoutRoutes({}, () => jsonResponse(503, { error: { code: 'STORE_UNAVAILABLE', message: 'Preferences are temporarily unavailable.', details: [] } }));
  mount(everything);
  await unlock();
  await userEvent.click(screen.getByRole('button', { name: 'Hide Changes' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('The layout was not saved');
});
