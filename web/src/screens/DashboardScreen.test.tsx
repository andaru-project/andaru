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

// The System widget has its own tests; here it is one more widget, still loading.
const noSystem = { error: '', loading: true, reload: vi.fn(), refresh: vi.fn() };
const settled: ChangeState = { applies: { pointers: { ...pointers, live: '1' }, items: [apply({})] }, head: '3' };
const latest = (fields: Parameters<typeof apply>[0]): ChangeState => ({ applies: { pointers: { ...pointers, live: '1' }, items: [apply({ id: '2', ...fields }), apply({})] }, head: '3' });

// mount returns once the layout has been read or has failed to be: no widget shows before that.
async function mount(capabilities: string[], changes: Partial<Resource<ChangeState | undefined>> = { data: settled }) {
  const resource = { error: '', loading: false, reload: vi.fn(), refresh: vi.fn(), ...changes };
  render(<MemoryRouter><DashboardScreen api={new ApiClient('csrf')} session={session(capabilities)} changes={resource} system={noSystem} onSessionLost={vi.fn()} /></MemoryRouter>);
  await waitFor(() => expect(screen.queryByLabelText('Loading the layout')).not.toBeInTheDocument());
}

afterEach(() => vi.useRealTimers());

it('shows each widget its own data, and a widget that fails does not blank the others', async () => {
  fetchRoutes({
    '/api/v1/status/links': { items: [link('lo', 'UNKNOWN', []), link('ens18', 'UP', [['10.30.0.100', 24, 'global'], ['fe80::1', 64, 'link']])] },
    '/api/v1/status/prefixes': () => jsonResponse(503, { error: { code: 'INVENTORY_UNAVAILABLE', message: 'The delegated prefixes are unavailable.', details: [] } }),
  });
  await mount(['configuration.read', 'status.read']);

  const interfaces = screen.getByRole('region', { name: 'Interfaces' });
  expect(await within(interfaces).findByText('ens18')).toBeInTheDocument();
  expect(within(interfaces).getByText('10.30.0.100/24')).toBeInTheDocument();
  // The loopback and link-local addresses are noise on a dashboard.
  expect(within(interfaces).queryByText('lo')).not.toBeInTheDocument();
  expect(within(interfaces).queryByText('fe80::1/64')).not.toBeInTheDocument();

  expect(await within(screen.getByRole('region', { name: 'IPv6 prefixes' })).findByRole('alert')).toHaveTextContent('The delegated prefixes are unavailable.');
  expect(within(screen.getByRole('region', { name: 'Changes' })).getByText(/Nothing is pending/)).toBeInTheDocument();
  // A widget's title leads to the page with the full view.
  expect(screen.getByRole('link', { name: 'Open Changes' })).toHaveAttribute('href', '/changes');
});

it('shows only the widgets the capabilities allow', async () => {
  fetchRoutes({});
  await mount(['configuration.read']);
  expect(screen.getAllByRole('region').map((region) => region.getAttribute('aria-labelledby'))).toEqual(['widget-changes']);
});

it('says so when the account can see no widget', async () => {
  fetchRoutes({});
  await mount(['tokens.manageSelf']);
  expect(screen.getByRole('status')).toHaveTextContent('none of the dashboard');
});

it.each<[string, ChangeState, RegExp]>([
  ['saved changes that are not applied', { applies: { pointers, items: [] }, head: '4' }, /^Saved changes are not applied yet\.$/],
  ['an apply in progress', latest({ state: 'activating' }), /^Applying saved version 3…$/],
  ['an apply that failed', latest({ state: 'failed' }), /^The last apply failed\.$/],
  ['an apply that was rolled back', latest({ state: 'rolled_back' }), /^The last apply was rolled back\.$/],
  ['an apply awaiting confirmation', latest({ state: 'awaiting_confirmation', deadline: '2099-01-01T00:00:00Z' }), /^Applied, awaiting confirmation: rolls back at \d\d:\d\d:\d\d \(.+ left\)\.$/],
])('reports %s', async (_, state, text) => {
  fetchRoutes({});
  await mount(['configuration.read'], { data: state });
  expect(within(screen.getByRole('region', { name: 'Changes' })).getByText(text)).toBeInTheDocument();
});

it('keeps the last change state beside the error when its poll fails', async () => {
  fetchRoutes({});
  await mount(['configuration.read'], { data: settled, error: 'The box does not answer.' });
  const changes = screen.getByRole('region', { name: 'Changes' });
  expect(within(changes).getByRole('alert')).toHaveTextContent('The box does not answer.');
  expect(within(changes).getByText(/Nothing is pending/)).toBeInTheDocument();
});

it('lists a delegated prefix and where it went, each line the same: the interface, its zone, the prefix', async () => {
  fetchRoutes({
    '/api/v1/status/links': { items: [link('ens18', 'UP', []), link('ens19', 'UP', [])] },
    '/api/v1/status/prefixes': { wans: [{ interface: 'ens18', current: { prefix: '2001:db8:30::/56' }, segments: [{ interface: 'ens19', subnetId: '1', prefix: '2001:db8:30:1::/64' }], pools: [{ interface: 'ens19', subnetId: '1', prefix: '2001:db8:30:10::/60' }], notFitting: ['guest'] }] },
    '/api/v1/config': { state: 'saved', savedVersion: '1', value: {
      links: [{ id: 'lnk_a', name: 'wan', kind: 'ethernet', mtu: 1500, enabled: true }, { id: 'lnk_b', name: 'lan', kind: 'ethernet', mtu: 1500, enabled: true }],
      segments: [{ id: 's1', name: 'wan', link: 'lnk_a', zone: 'z1', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'slaac' } } }, { id: 's2', name: 'lan', link: 'lnk_b', zone: 'z2', addressing: { ipv4: { mode: 'static' }, ipv6: { mode: 'none' } } }],
      zones: [{ id: 'z1', name: 'wan' }, { id: 'z2', name: 'lan' }],
    } },
    '/api/v1/preview/network': { state: 'preview', savedVersion: '1', realizable: true, resolved: [{ link: 'lnk_a', interface: 'ens18' }, { link: 'lnk_b', interface: 'ens19' }], findings: [] },
  });
  await mount(['status.read', 'configuration.read']);
  const prefixes = screen.getByRole('region', { name: 'IPv6 prefixes' });
  expect(await within(prefixes).findByText('2001:db8:30::/56')).toBeInTheDocument();
  await within(prefixes).findAllByText('zone lan');
  // Three lines of one shape, read as text: interface, zone, what it is, prefix.
  const lines = [...prefixes.querySelectorAll('li')].filter((item) => !item.querySelector('li')).map((item) => item.textContent);
  expect(lines).toEqual(['ens19zone lanlan2001:db8:30:1::/64', 'ens19zone lanlanpool2001:db8:30:10::/60']);
  expect(prefixes.querySelector('li')).toHaveTextContent(/^ens18zone wanwandelegated2001:db8:30::\/56/);
  expect(within(prefixes).getByText('No room for: guest')).toBeInTheDocument();

  // The same zones show wherever the interfaces do.
  const interfaces = screen.getByRole('region', { name: 'Interfaces' });
  expect(within(interfaces).getByText('zone wan')).toBeInTheDocument();
  expect(within(interfaces).getByText('zone lan')).toBeInTheDocument();
  // An interface's state is a dot badge, a shape a zone's pill never has.
  expect(within(interfaces).getAllByText('up').map((state) => state.closest('.mantine-Badge-root')?.getAttribute('data-variant'))).toEqual(['dot', 'dot']);
});

it('shows interfaces without zones to an account that may not read the configuration, and asks for none', async () => {
  const fetchMock = fetchRoutes({ '/api/v1/status/links': { items: [link('ens18', 'UP', [])] }, '/api/v1/status/prefixes': { wans: [] } });
  await mount(['status.read']);
  expect(await within(screen.getByRole('region', { name: 'Interfaces' })).findByText('ens18')).toBeInTheDocument();
  expect(screen.queryByText(/^zone /)).not.toBeInTheDocument();
  expect(fetchMock.mock.calls.map(([url]) => url)).not.toContain('/api/v1/config');
});

it('polls a widget every 10 seconds, and waits for an answer that is still out', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  let answer: (response: Response) => void = () => undefined;
  const fetchMock = fetchRoutes({
    '/api/v1/status/links': () => new Promise<Response>((resolve) => { answer = resolve; }),
    '/api/v1/status/prefixes': { wans: [] },
  });
  const reads = () => fetchMock.mock.calls.filter(([url]) => url === '/api/v1/status/links').length;
  await mount(['status.read']);
  // The widgets mount once the layout is read; their first request follows in an effect.
  await waitFor(() => expect(reads()).toBe(1));

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
  expect(screen.getByRole('button', { name: 'Layout unlocked' })).toBeInTheDocument();
}

it('applies the stored layout: its order, its hidden widgets, and a widget new since it was saved', async () => {
  // "gone" is a widget of another version. "interfaces" is not named, so it joins at the end.
  layoutRoutes({ dashboard: { order: ['prefixes', 'gone', 'changes'], hidden: ['changes'] } });
  await mount(everything);
  await waitFor(() => expect(titles()).toEqual(['IPv6 prefixes', 'System', 'Resources', 'Network activity', 'Interfaces']));
});

it('stays locked until asked: nothing moves or hides by accident', async () => {
  layoutRoutes({ dashboard: { hidden: ['changes'] } });
  await mount(everything);
  await waitFor(() => expect(titles()).toEqual(['System', 'Resources', 'Network activity', 'Interfaces', 'IPv6 prefixes']));
  expect(screen.queryByRole('button', { name: /^(Move|Hide|Show) / })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Reset layout' })).not.toBeInTheDocument();

  await unlock();
  for (const name of ['Move Interfaces', 'Hide Interfaces', 'Move IPv6 prefixes', 'Hide IPv6 prefixes', 'Show Changes', 'Reset layout']) expect(screen.getByRole('button', { name })).toBeInTheDocument();
  expect(within(screen.getByRole('region', { name: 'Hidden widgets' })).getByText('Changes')).toBeInTheDocument();
});

it('hides and shows a widget, saving the whole preferences and keeping what it does not know', async () => {
  const fetchMock = layoutRoutes({ another: { version: 'keeps this' }, dashboard: {} });
  await mount(everything);
  await unlock();

  await userEvent.click(screen.getByRole('button', { name: 'Hide Interfaces' }));
  expect(titles()).toEqual(['System', 'Resources', 'Network activity', 'Changes', 'IPv6 prefixes', 'Hidden widgets']);
  await userEvent.click(screen.getByRole('button', { name: 'Show Interfaces' }));
  expect(titles()).toEqual(['System', 'Resources', 'Network activity', 'Changes', 'Interfaces', 'IPv6 prefixes']);

  const order = ['system', 'resources', 'traffic', 'changes', 'interfaces', 'prefixes'];
  expect(saves(fetchMock)).toEqual([
    { another: { version: 'keeps this' }, dashboard: { order, hidden: ['interfaces'] } },
    { another: { version: 'keeps this' }, dashboard: { order, hidden: [] } },
  ]);
});

it('resets to the default layout', async () => {
  const fetchMock = layoutRoutes({ dashboard: { order: ['prefixes', 'interfaces', 'changes'], hidden: ['changes'] } });
  await mount(everything);
  await unlock();
  await userEvent.click(screen.getByRole('button', { name: 'Reset layout' }));
  expect(titles()).toEqual(['System', 'Resources', 'Network activity', 'Changes', 'Interfaces', 'IPv6 prefixes']);
  expect(saves(fetchMock)).toEqual([{ dashboard: {} }]);
});

it('says so when every widget is hidden, and how to get them back while locked', async () => {
  layoutRoutes({ dashboard: { hidden: ['system', 'resources', 'traffic', 'changes', 'interfaces', 'prefixes'] } });
  await mount(everything);
  expect(screen.getByText(/Every widget is hidden/)).toHaveTextContent('Every widget is hidden. Unlock the layout to show them again.');
  await unlock();
  expect(screen.getByText(/Every widget is hidden/)).toHaveTextContent(/^Every widget is hidden\.$/);
});

it('reads the layout before any widget shows, so a hidden widget never asks the box for anything', async () => {
  let answer: (response: Response) => void = () => undefined;
  const fetchMock = fetchRoutes({
    '/api/v1/session/preferences': () => new Promise<Response>((resolve) => { answer = resolve; }),
    '/api/v1/status/links': { items: [] },
    '/api/v1/status/prefixes': { wans: [] },
  });
  render(<MemoryRouter><DashboardScreen api={new ApiClient('csrf')} session={session(everything)} changes={{ data: settled, error: '', loading: false, reload: vi.fn(), refresh: vi.fn() }} system={noSystem} onSessionLost={vi.fn()} /></MemoryRouter>);
  expect(screen.getByLabelText('Loading the layout')).toBeInTheDocument();
  expect(screen.queryByRole('region')).not.toBeInTheDocument();
  await act(async () => { answer(jsonResponse(200, { dashboard: { hidden: ['interfaces'] } })); await Promise.resolve(); });
  await waitFor(() => expect(titles()).toEqual(['System', 'Resources', 'Network activity', 'Changes', 'IPv6 prefixes']));
  expect(fetchMock.mock.calls.map(([url]) => url)).not.toContain('/api/v1/status/links');
});

it('keeps the order and the hidden state of widgets this account may not see', async () => {
  const fetchMock = layoutRoutes({ dashboard: { order: ['prefixes', 'changes', 'interfaces'], hidden: ['changes'] } });
  await mount(['status.read']);
  expect(titles()).toEqual(['IPv6 prefixes', 'Interfaces', 'System', 'Resources', 'Network activity']);
  await unlock();
  // Changes is hidden but not this account's: it is not offered, and saving does not forget it.
  expect(screen.queryByRole('button', { name: 'Show Changes' })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole('button', { name: 'Hide IPv6 prefixes' }));
  await waitFor(() => expect(saves(fetchMock)).toEqual([{ dashboard: { order: ['prefixes', 'changes', 'interfaces', 'system', 'resources', 'traffic'], hidden: ['changes', 'prefixes'] } }]));
});

it('moves a widget by its grip with the keyboard, and saves the new order', async () => {
  // jsdom lays nothing out, and a keyboard move goes by where the widgets are: put them in a row.
  const rect = vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
    const index = [...document.querySelectorAll('.widget')].indexOf(this.closest('.widget') ?? this);
    return DOMRect.fromRect(index < 0 ? {} : { x: index * 400, y: 0, width: 380, height: 200 });
  });
  const fetchMock = layoutRoutes({});
  await mount(everything);
  await unlock();
  const grip = screen.getByRole('button', { name: 'Move Changes' });
  grip.focus();
  await userEvent.keyboard('{Enter}');
  await userEvent.keyboard('{ArrowRight}');
  await userEvent.keyboard('{Enter}');
  await waitFor(() => expect(titles()).toEqual(['System', 'Resources', 'Network activity', 'Interfaces', 'Changes', 'IPv6 prefixes']));
  await waitFor(() => expect(saves(fetchMock)).toEqual([{ dashboard: { order: ['system', 'resources', 'traffic', 'interfaces', 'changes', 'prefixes'], hidden: [] } }]));
  expect(screen.getByRole('status', { hidden: true })).toHaveTextContent('Dropped Changes at position 5 of 6.');
  rect.mockRestore();
});

it('sends saves one at a time, each on what the last one stored, and skips one that a newer has overtaken', async () => {
  const held: Array<(response: Response) => void> = [];
  const fetchMock = fetchRoutes({
    '/api/v1/session/preferences': { dashboard: {} },
    'PUT /api/v1/session/preferences': () => new Promise<Response>((resolve) => { held.push(resolve); }),
    '/api/v1/status/links': { items: [] },
    '/api/v1/status/prefixes': { wans: [] },
  });
  await mount(everything);
  await unlock();
  for (const name of ['Hide Changes', 'Hide Interfaces', 'Hide IPv6 prefixes']) await userEvent.click(screen.getByRole('button', { name }));
  // The first save is out; the second and third wait, and only the last of them is worth sending.
  expect(saves(fetchMock)).toHaveLength(1);
  // The box answers with what it now holds, which another feature has written a key into.
  await act(async () => { held[0](jsonResponse(200, { other: 'kept', dashboard: { hidden: ['changes'] } })); await Promise.resolve(); });
  await waitFor(() => expect(saves(fetchMock)).toHaveLength(2));
  expect(saves(fetchMock)[1]).toEqual({ other: 'kept', dashboard: { order: ['system', 'resources', 'traffic', 'changes', 'interfaces', 'prefixes'], hidden: ['changes', 'interfaces', 'prefixes'] } });
});

it('moves focus to the padlock when the button that was pressed goes away', async () => {
  layoutRoutes({});
  await mount(everything);
  await unlock();
  await userEvent.click(screen.getByRole('button', { name: 'Hide Changes' }));
  expect(screen.getByRole('button', { name: 'Layout unlocked' })).toHaveFocus();
  await userEvent.click(screen.getByRole('button', { name: 'Show Changes' }));
  expect(screen.getByRole('button', { name: 'Layout unlocked' })).toHaveFocus();
});

it('tries again when the layout could not be read, and unlocks once it can', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  let up = false;
  fetchRoutes({
    '/api/v1/session/preferences': () => (up ? jsonResponse(200, { dashboard: { hidden: ['changes'] } }) : jsonResponse(503, { error: { code: 'STORE_UNAVAILABLE', message: 'Preferences are temporarily unavailable.', details: [] } })),
    '/api/v1/status/links': { items: [] },
    '/api/v1/status/prefixes': { wans: [] },
  });
  await mount(everything);
  expect(screen.getByRole('button', { name: 'Layout locked' })).toBeDisabled();
  up = true;
  act(() => { vi.advanceTimersByTime(10_000); });
  await waitFor(() => expect(screen.getByRole('button', { name: 'Layout locked' })).toBeEnabled());
  expect(titles()).toEqual(['System', 'Resources', 'Network activity', 'Interfaces', 'IPv6 prefixes']);
  expect(screen.queryByText(/The layout cannot be changed now/)).not.toBeInTheDocument();
});

it('keeps the layout locked when the preferences cannot be read: a save would replace what it has not seen', async () => {
  const fetchMock = layoutRoutes(jsonResponse(503, { error: { code: 'STORE_UNAVAILABLE', message: 'Preferences are temporarily unavailable.', details: [] } }));
  await mount(everything);
  expect(await screen.findByText(/The layout cannot be changed now: Preferences are temporarily unavailable\./)).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Layout locked' })).toBeDisabled();
  expect(titles()).toEqual(['System', 'Resources', 'Network activity', 'Changes', 'Interfaces', 'IPv6 prefixes']);
  expect(saves(fetchMock)).toEqual([]);
});

it('says when a change was not saved', async () => {
  layoutRoutes({}, () => jsonResponse(503, { error: { code: 'STORE_UNAVAILABLE', message: 'Preferences are temporarily unavailable.', details: [] } }));
  await mount(everything);
  await unlock();
  await userEvent.click(screen.getByRole('button', { name: 'Hide Changes' }));
  expect(await screen.findByText('The layout was not saved')).toBeInTheDocument();
});

it('shows the system description it is handed, or why it has none', async () => {
  fetchRoutes({});
  const info = { version: '0.5.0-a.4', hostname: 'edge', kernel: '6.17', architecture: 'x86_64', time: '2026-11-21T12:00:00Z', bootedAt: null, memoryBytes: 0, guest: '', iommu: false,
    cpu: { model: 'Test CPU', cores: 2, threads: 2, microcode: '', level: '', features: { cryptography: [], security: [], virtualization: [] }, vulnerabilities: [] },
    hardware: { vendor: '', product: '', serial: '', boardVendor: '', board: '', biosVendor: '', biosVersion: '', biosDate: '' } };
  const base = { error: '', loading: false, reload: vi.fn(), refresh: vi.fn() };
  const screenWith = (system: Parameters<typeof DashboardScreen>[0]['system']) => <MemoryRouter><DashboardScreen api={new ApiClient('csrf')} session={session(['status.read'])} changes={{ ...base, data: settled }} system={system} onSessionLost={vi.fn()} /></MemoryRouter>;
  const view = render(screenWith({ ...base, data: { info, loadedAt: Date.now() } }));
  const system = await screen.findByRole('region', { name: 'System' });
  expect(within(system).getByRole('rowheader', { name: 'Andaru' }).nextElementSibling).toHaveTextContent('0.5.0-a.4');
  // The System widget has no page of its own, so its title is not a link.
  expect(screen.queryByRole('link', { name: 'Open System' })).not.toBeInTheDocument();

  view.rerender(screenWith({ ...base, error: 'The system description is unavailable.' }));
  expect(within(screen.getByRole('region', { name: 'System' })).getByRole('alert')).toHaveTextContent('The system description is unavailable.');
});
