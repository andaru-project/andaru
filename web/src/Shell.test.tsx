import { MemoryRouter } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { ApiClient } from './api';
import { configureNetplaneApi } from './netplane/api';
import { setRoutePrefix } from './netplane/routing';
import { Shell } from './Shell';
import { fetchRoutes, jsonResponse } from './test/fetch';
import { apply, pointers, session } from './test/fixtures';
import { act, render, screen } from './test/render';
import userEvent from '@testing-library/user-event';

const reader = session(['configuration.read']);
const unapplied = { pointers: { ...pointers, confirmed: '1', live: '1' }, items: [apply({})] };

function routes() {
  return fetchRoutes({
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [{ id: '5', createdAt: '2026-11-21T11:00:00Z', actor: 'admin', changedObjectCount: 1 }] },
  });
}

function mount(path: string) {
  const api = new ApiClient('csrf');
  const onLogout = vi.fn();
  const onSessionLost = vi.fn();
  const shell = (connected: boolean) => <MemoryRouter initialEntries={[path]}><Shell api={api} session={reader} connected={connected} onLogout={onLogout} onSessionLost={onSessionLost} /></MemoryRouter>;
  const result = render(shell(true));
  return (connected: boolean) => result.rerender(shell(connected));
}

let visibility: DocumentVisibilityState = 'visible';
Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibility });

afterEach(() => {
  visibility = 'visible';
  configureNetplaneApi(null);
  setRoutePrefix('');
  vi.useRealTimers();
});

const appliesReads = (fetchMock: ReturnType<typeof fetchRoutes>) => fetchMock.mock.calls.filter(([url]) => url === '/api/v1/applies').length;

it('polls the change state every 15 seconds while the tab is visible, and when it becomes visible', async () => {
  // A poll waits for the answer that is still out, so each answer is let in before the next tick.
  const answered = () => act(async () => { await new Promise((resolve) => setTimeout(resolve)); });
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  const fetchMock = routes();
  mount('/system/session');
  expect(appliesReads(fetchMock)).toBe(1);
  await answered();

  act(() => { vi.advanceTimersByTime(14_999); });
  expect(appliesReads(fetchMock)).toBe(1);
  act(() => { vi.advanceTimersByTime(1); });
  expect(appliesReads(fetchMock)).toBe(2);
  await answered();

  visibility = 'hidden';
  act(() => { vi.advanceTimersByTime(30_000); });
  expect(appliesReads(fetchMock)).toBe(2);

  visibility = 'visible';
  act(() => { document.dispatchEvent(new Event('visibilitychange')); });
  expect(appliesReads(fetchMock)).toBe(3);
});

it('reloads the change state when the connection comes back', () => {
  const fetchMock = routes();
  const rerender = mount('/system/session');
  expect(appliesReads(fetchMock)).toBe(1);

  rerender(false);
  expect(appliesReads(fetchMock)).toBe(1);
  rerender(true);
  expect(appliesReads(fetchMock)).toBe(2);
});

it('shows the pending notice everywhere but on Review and apply', async () => {
  routes();
  mount('/system/session');
  expect(await screen.findByText('Saved changes are not applied yet')).toBeInTheDocument();
});

it('says so when the change state cannot be read: no news must not look like nothing pending', async () => {
  fetchRoutes({ '/api/v1/applies': () => jsonResponse(503, { error: { code: 'STORE_UNAVAILABLE', message: 'The applies are unavailable.', details: [] } }), '/api/v1/versions?': { state: 'saved', items: [] } });
  mount('/system/session');
  expect(await screen.findByText('Whether changes are pending cannot be read')).toBeInTheDocument();
});

it.each(['/changes', '/changes/'])('leaves the apply controls to the Review and apply page at %s', async (path) => {
  routes();
  mount(path);
  expect(await screen.findByRole('heading', { name: 'Review and apply' })).toBeInTheDocument();
  expect(await screen.findByText('Apply 1, saved version 3: Confirmed.')).toBeInTheDocument();
  expect(screen.queryByText('Saved changes are not applied yet')).not.toBeInTheDocument();
});

it('names the box in the header, for an account that may read status: its version is on the dashboard', async () => {
  fetchRoutes({
    '/api/v1/status/system': { version: '0.5.0-a.4', hostname: 'edge', time: '2026-11-21T12:00:00Z', bootedAt: null },
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
  });
  const api = new ApiClient('csrf');
  render(<MemoryRouter initialEntries={['/system/session']}><Shell api={api} session={session(['status.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} /></MemoryRouter>);
  expect((await screen.findByText('edge')).textContent).toBe('edge');
  expect(screen.queryByText(/0\.5\.0-a\.4/)).not.toBeInTheDocument();
});

it('has the firewall rules as a page, for an account that may read the configuration', async () => {
  fetchRoutes({
    '/api/v1/config/rules': { state: 'saved', savedVersion: '0', version: '0', items: [] },
    '/api/v1/config': { state: 'saved', savedVersion: '0', unusedSecrets: [], value: { links: [], segments: [], zones: [] } },
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
  });
  render(<MemoryRouter initialEntries={['/firewall/rules']}><Shell api={new ApiClient('csrf')} session={session(['configuration.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} /></MemoryRouter>);
  expect(await screen.findByRole('heading', { name: 'Rules' })).toBeInTheDocument();
  expect(await screen.findByText('No rules')).toBeInTheDocument();
});

it('has the aliases as a page beside the rules', async () => {
  fetchRoutes({
    '/api/v1/config/aliases/': { state: 'saved', savedVersion: '0', items: [] },
    '/api/v1/config/rules': { state: 'saved', savedVersion: '0', version: '0', items: [] },
    '/api/v1/config': { state: 'saved', savedVersion: '0', unusedSecrets: [], value: { links: [], segments: [], zones: [] } },
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
  });
  render(<MemoryRouter initialEntries={['/firewall/aliases']}><Shell api={new ApiClient('csrf')} session={session(['configuration.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} /></MemoryRouter>);
  expect(await screen.findByRole('heading', { name: 'Aliases' })).toBeInTheDocument();
  expect(await screen.findByText('No aliases')).toBeInTheDocument();
});

it('has a page for what the box is made of, under System', async () => {
  fetchRoutes({
    '/api/v1/status/system': { version: '0.5.0-a.4', hostname: 'edge', kernel: '6.17', architecture: 'x86_64', time: '2026-11-21T12:00:00Z', bootedAt: null, memoryBytes: 0, guest: '', iommu: false,
      cpu: { model: 'Test CPU', cores: 2, threads: 2, microcode: '', level: '', features: { cryptography: [], security: [], virtualization: [] }, vulnerabilities: [] },
      hardware: { vendor: '', product: '', serial: '', boardVendor: '', board: '', biosVendor: '', biosVersion: '', biosDate: '' } },
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
  });
  render(<MemoryRouter initialEntries={['/system/details']}><Shell api={new ApiClient('csrf')} session={session(['status.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} /></MemoryRouter>);
  expect(await screen.findByRole('heading', { name: 'System details' })).toBeInTheDocument();
  expect(await screen.findByRole('region', { name: 'Processor' })).toHaveTextContent('Test CPU, 2 cores');
});

it('reads the box again each minute, so an upgrade behind an open tab shows', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  let version = '0.5.0-a.4';
  fetchRoutes({
    '/api/v1/status/system': () => jsonResponse(200, { version, hostname: '', kernel: '6.17', architecture: 'x86_64', time: '2026-11-21T12:00:00Z', bootedAt: null, memoryBytes: 0, guest: '', iommu: false,
      cpu: { model: 'Test CPU', cores: 2, threads: 2, microcode: '', level: '', features: { cryptography: [], security: [], virtualization: [] }, vulnerabilities: [] },
      hardware: { vendor: '', product: '', serial: '', boardVendor: '', board: '', biosVendor: '', biosVersion: '', biosDate: '' } }),
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
  });
  render(<MemoryRouter initialEntries={['/system/details']}><Shell api={new ApiClient('csrf')} session={session(['status.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} /></MemoryRouter>);
  expect(await screen.findByText('0.5.0-a.4')).toBeInTheDocument();
  version = '0.5.0-a.5';
  act(() => { vi.advanceTimersByTime(60_000); });
  expect(await screen.findByText('0.5.0-a.5')).toBeInTheDocument();
});

it('asks nothing about the box for an account that may not read status', async () => {
  const fetchMock = routes();
  mount('/system/session');
  await screen.findByText('Saved changes are not applied yet');
  expect(fetchMock.mock.calls.map(([url]) => url)).not.toContain('/api/v1/status/system');
});

const question = { id: 'rename', question: 'Rename the zones?', choices: [{ id: 'keep', label: 'Keep', description: '' }], provisional: 'keep', objects: ['zon_lan'], choice: 'keep', answered: false };

it('opens on Finish the upgrade while the saved settings wait for decisions, and says so on every other page', async () => {
  fetchRoutes({
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    'POST /api/v1/config/upgrade/preview': { savedVersion: '5', questions: [question] },
  });
  mount('/');
  expect(await screen.findByRole('heading', { name: 'Finish the upgrade' })).toBeInTheDocument();
  expect(await screen.findByText('Rename the zones?')).toBeInTheDocument();
  expect(screen.queryByRole('link', { name: 'Answer the questions' })).not.toBeInTheDocument();
});

it('leaves the dashboard alone when no decision waits', async () => {
  const fetchMock = fetchRoutes({
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    'POST /api/v1/config/upgrade/preview': { savedVersion: '5', questions: [] },
  });
  mount('/');
  await vi.waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url === '/api/v1/config/upgrade/preview')).toBe(true));
  expect(screen.queryByRole('heading', { name: 'Finish the upgrade' })).not.toBeInTheDocument();
  expect(screen.queryByText('Answer the questions')).not.toBeInTheDocument();
});

it('says when it cannot tell whether decisions wait', async () => {
  fetchRoutes({
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    'POST /api/v1/config/upgrade/preview': jsonResponse(503, { error: { code: 'STORE_UNAVAILABLE', message: 'Unavailable.' } }),
  });
  mount('/system/session');
  expect(await screen.findByText('Whether the settings wait for decisions cannot be read')).toBeInTheDocument();
});

it('points to the questions from any other page', async () => {
  fetchRoutes({
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    'POST /api/v1/config/upgrade/preview': { savedVersion: '5', questions: [question] },
  });
  mount('/system/session');
  expect(await screen.findByRole('link', { name: 'Answer the questions' })).toBeInTheDocument();
});

it('sets the document title with the page and hostname, and marks pending changes in navigation', async () => {
  fetchRoutes({
    '/api/v1/status/system': { version: '0.5.0-a.4', hostname: 'edge', time: '2026-11-21T12:00:00Z', bootedAt: null },
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [{ id: '5', createdAt: '2026-11-21T11:00:00Z', actor: 'admin', changedObjectCount: 1 }] },
  });
  const { unmount } = render(<MemoryRouter initialEntries={['/system/session']}><Shell api={new ApiClient('csrf')} session={session(['status.read', 'configuration.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} /></MemoryRouter>);
  await screen.findByText('edge');
  expect(document.title).toBe('Current session — edge — Andaru');
  expect(await screen.findByRole('link', { name: /Review and apply.*Pending/i })).toBeInTheDocument();
  unmount();
  expect(document.title).toBe('Andaru administration');
});

it('mounts the fleet screens under /network/* for status.read without requesting targets when configuration.read is absent', async () => {
  const fetchMock = fetchRoutes({
    '/api/v1/status/system': { version: '0.5.0-a.4', hostname: 'edge', time: '2026-11-21T12:00:00Z', bootedAt: null },
    '/api/v1/fleet/state': {
      devices: [{ id: 'local', name: 'edge', driver: 'andaru', role: 'router', online: true, capabilities: [], system_stats: {}, sensors: [], firmware: {}, uptime_sec: 120 }],
      links: [],
      lag_groups: [],
      segments: [],
      zones: [],
      gateways: [],
      routes: [],
      vpns: [],
      firewalls: [],
      radios: [],
      wireless_uplinks: [],
      clients: [],
    },
    '/api/v1/fleet/topology': { generated_at: '2026-11-21T12:00:00Z', root_ids: ['local'], nodes: [], edges: [] },
    '/api/v1/fleet/vlans/flow': { generated_at: '2026-11-21T12:00:00Z', vlans: [], findings: [] },
    '/api/v1/fleet/diagnostics?limit=30': { items: [] },
  });
  render(
    <MemoryRouter initialEntries={['/network/topology']}>
      <Shell api={new ApiClient('csrf')} session={session(['status.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(await screen.findByRole('heading', { name: 'Topology' })).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'VLAN Flow' })).toHaveAttribute('href', '/network/vlans/flow');
  expect(screen.getByRole('link', { name: 'Devices' })).toHaveAttribute('href', '/network/devices');
  await vi.waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url === '/api/v1/fleet/topology')).toBe(true));
  expect(fetchMock.mock.calls.map(([url]) => url)).not.toContain('/api/v1/fleet/targets');
  expect(document.title).toBe('Topology — edge — Andaru');
});

it('keeps Devices active and loads /api/v1/fleet/devices/:id on device detail routes', async () => {
  fetchRoutes({
    '/api/v1/status/system': { version: '0.5.0-a.4', hostname: 'edge', time: '2026-11-21T12:00:00Z', bootedAt: null },
    '/api/v1/fleet/state': {
      devices: [{ id: 'sw-core', name: 'Core Switch', driver: 'snmp', role: 'switch', online: true, capabilities: [], system_stats: {}, sensors: [], firmware: {}, uptime_sec: 120 }],
      links: [],
      lag_groups: [],
      segments: [],
      zones: [],
      gateways: [],
      routes: [],
      vpns: [],
      firewalls: [],
      radios: [],
      wireless_uplinks: [],
      clients: [],
    },
    '/api/v1/fleet/topology': { generated_at: '2026-11-21T12:00:00Z', root_ids: ['sw-core'], nodes: [], edges: [] },
    '/api/v1/fleet/vlans/flow': { generated_at: '2026-11-21T12:00:00Z', vlans: [], findings: [] },
    '/api/v1/fleet/diagnostics?limit=30': { items: [] },
    '/api/v1/fleet/devices/sw-core': {
      device: { id: 'sw-core', name: 'Core Switch', driver: 'snmp', role: 'switch', online: true, capabilities: [], system_stats: {}, sensors: [], firmware: {}, uptime_sec: 120 },
      links: [],
      lag_groups: [],
      segments: [],
      zones: [],
      gateways: [],
      routes: [],
      vpns: [],
      firewalls: [],
      radios: [],
      wireless_uplinks: [],
      clients: [],
    },
  });
  render(
    <MemoryRouter initialEntries={['/network/devices/sw-core']}>
      <Shell api={new ApiClient('csrf')} session={session(['status.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(await screen.findByRole('heading', { name: 'Core Switch' })).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Devices' })).toHaveAttribute('aria-current', 'page');
  expect(document.title).toBe('Device — edge — Andaru');
});

it('wires session CSRF token, onSessionLost on 401, and background header on periodic fleet polls', async () => {
  const emptyState = {
    devices: [],
    links: [],
    lag_groups: [],
    segments: [],
    zones: [],
    gateways: [],
    routes: [],
    vpns: [],
    firewalls: [],
    radios: [],
    wireless_uplinks: [],
    clients: [],
  };
  const fetchMock = fetchRoutes({
    '/api/v1/status/system': { version: '0.5.0-a.4', hostname: 'edge', time: '2026-11-21T12:00:00Z', bootedAt: null },
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    '/api/v1/fleet/state': emptyState,
    '/api/v1/fleet/topology': { generated_at: '2026-11-21T12:00:00Z', root_ids: [], nodes: [], edges: [] },
    '/api/v1/fleet/vlans/flow': { generated_at: '2026-11-21T12:00:00Z', vlans: [], findings: [] },
    '/api/v1/fleet/targets': { items: [{ id: 'sw-1', name: 'Switch 1', driver: 'snmp', endpoint: '10.0.0.2', enabled: true, poll_interval: 30 }] },
    '/api/v1/fleet/diagnostics?limit=30': { items: [] },
    'POST /api/v1/fleet/targets/sw-1/poll': { ok: true },
  });
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  const fleetSession = { ...session(['status.read', 'configuration.read', 'configuration.write']), csrfToken: 'shell-csrf-secret' };
  const onSessionLost = vi.fn();
  render(
    <MemoryRouter initialEntries={['/network/topology']}>
      <Shell api={new ApiClient(fleetSession.csrfToken)} session={fleetSession} connected onLogout={vi.fn()} onSessionLost={onSessionLost} />
    </MemoryRouter>,
  );

  const pollBtn = await screen.findByRole('button', { name: 'Poll all' });
  await userEvent.click(pollBtn);
  const pollCall = fetchMock.mock.calls.find(([url, init]) => url === '/api/v1/fleet/targets/sw-1/poll' && init?.method === 'POST');
  expect(pollCall).toBeDefined();
  expect(new Headers(pollCall?.[1]?.headers).get('X-Andaru-CSRF')).toBe('shell-csrf-secret');

  // Unattended periodic fleet polls must carry X-Andaru-Background: 1.
  const beforePollCount = fetchMock.mock.calls.filter(([url]) => url === '/api/v1/fleet/state').length;
  act(() => { vi.advanceTimersByTime(15_000); });
  await vi.waitFor(() => expect(fetchMock.mock.calls.filter(([url]) => url === '/api/v1/fleet/state').length).toBe(beforePollCount + 1));
  const bgStateCall = fetchMock.mock.calls.filter(([url]) => url === '/api/v1/fleet/state').at(-1);
  expect(new Headers(bgStateCall?.[1]?.headers).get('X-Andaru-Background')).toBe('1');

  // A 401 on a fleet read calls onSessionLost and never triggers window.prompt.
  const promptSpy = vi.spyOn(window, 'prompt').mockReturnValue('unused');
  fetchRoutes({
    '/api/v1/status/system': { version: '0.5.0-a.4', hostname: 'edge', time: '2026-11-21T12:00:00Z', bootedAt: null },
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    '/api/v1/fleet/state': () => jsonResponse(401, { error: { code: 'UNAUTHORIZED', message: 'Expired' } }),
    '/api/v1/fleet/topology': () => jsonResponse(401, { error: { code: 'UNAUTHORIZED', message: 'Expired' } }),
    '/api/v1/fleet/vlans/flow': () => jsonResponse(401, { error: { code: 'UNAUTHORIZED', message: 'Expired' } }),
    '/api/v1/fleet/targets': () => jsonResponse(401, { error: { code: 'UNAUTHORIZED', message: 'Expired' } }),
    '/api/v1/fleet/diagnostics?limit=30': () => jsonResponse(401, { error: { code: 'UNAUTHORIZED', message: 'Expired' } }),
  });
  act(() => { vi.advanceTimersByTime(15_000); });
  await vi.waitFor(() => expect(onSessionLost).toHaveBeenCalled());
  expect(promptSpy).not.toHaveBeenCalled();
});
