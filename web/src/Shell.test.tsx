import { MemoryRouter } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { ApiClient } from './api';
import type { Apply } from './types';
import { configureNetplaneApi } from './netplane/api';
import { setRoutePrefix } from './netplane/routing';
import { Shell } from './Shell';
import { clearNotificationToasts, defaultNotificationPrefs, setNotificationPrefs } from './notifications';
import { openStreams } from './test/events';
import { setTimeZone } from './timezone';
import { fetchRoutes, jsonResponse } from './test/fetch';
import { apply, pointers, session } from './test/fixtures';
import { act, render, screen, waitFor, within } from './test/render';
import userEvent from '@testing-library/user-event';

const reader = session(['configuration.read']);
const unapplied = { pointers: { ...pointers, confirmed: '1', live: '1' }, unapplied: true, items: [apply({})] };

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

const originalNotification = window.Notification;
const originalIsSecureContext = Object.getOwnPropertyDescriptor(window, 'isSecureContext');

afterEach(() => {
  visibility = 'visible';
  configureNetplaneApi(null);
  setRoutePrefix('');
  setTimeZone('local');
  clearNotificationToasts();
  setNotificationPrefs(defaultNotificationPrefs);
  if (originalIsSecureContext) Object.defineProperty(window, 'isSecureContext', originalIsSecureContext);
  else Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
  Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: originalNotification });
  vi.useRealTimers();
});

const appliesReads = (fetchMock: ReturnType<typeof fetchRoutes>) => fetchMock.mock.calls.filter(([url]) => url === '/api/v1/applies').length;

it('reads the change state when the event stream opens and at each event, and opens a refused stream again', async () => {
  const answered = () => act(async () => { await new Promise((resolve) => setTimeout(resolve)); });
  const fetchMock = routes();
  mount('/system/session');
  expect(appliesReads(fetchMock)).toBe(1);
  const [stream] = openStreams();
  expect(stream.url).toBe('/api/v1/events');

  act(() => { stream.open(); });
  expect(appliesReads(fetchMock)).toBe(2);
  // A read already out does not hold back the next: it may have been answered before the change.
  act(() => { stream.message(); stream.message(); });
  expect(appliesReads(fetchMock)).toBe(4);
  await answered();

  // A failed stream is read after once, until a stream opens again, so a lost connection shows. A dropped stream is
  // the browser's to reopen; a refused one is the page's, after 15 seconds.
  act(() => { stream.fail(false); });
  expect(appliesReads(fetchMock)).toBe(5);
  act(() => { stream.fail(false); });
  expect(appliesReads(fetchMock)).toBe(5);
  await answered();
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
  act(() => { stream.fail(true); });
  expect(appliesReads(fetchMock)).toBe(5);
  expect(openStreams()).toHaveLength(0);
  act(() => { vi.advanceTimersByTime(15_000); });
  const [reopened] = openStreams();
  act(() => { reopened.open(); });
  expect(appliesReads(fetchMock)).toBe(6);
  act(() => { reopened.fail(true); });
  expect(appliesReads(fetchMock)).toBe(7);
  act(() => { vi.advanceTimersByTime(15_000); });
  expect(openStreams()).toHaveLength(1);

  // A hidden tab gives its connection back, and reads again when it is shown.
  visibility = 'hidden';
  act(() => { document.dispatchEvent(new Event('visibilitychange')); });
  expect(openStreams()).toHaveLength(0);
  act(() => { vi.advanceTimersByTime(15_000); });
  expect(openStreams()).toHaveLength(0);
  visibility = 'visible';
  act(() => { document.dispatchEvent(new Event('visibilitychange')); });
  act(() => { openStreams()[0].open(); });
  expect(openStreams()).toHaveLength(1);
  expect(appliesReads(fetchMock)).toBe(8);
});

it('reads the change state again every 15 seconds after a failed read, until one answers', async () => {
  const answered = () => act(async () => { await new Promise((resolve) => setTimeout(resolve)); });
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  let fail = false;
  const fetchMock = fetchRoutes({
    '/api/v1/applies': () => (fail ? jsonResponse(503, { error: { code: 'X', message: 'down', details: [] } }) : jsonResponse(200, unapplied)),
    '/api/v1/versions?': { state: 'saved', items: [] },
  });
  mount('/system/session');
  await answered();
  act(() => { vi.advanceTimersByTime(15_000); });
  expect(appliesReads(fetchMock)).toBe(1);

  fail = true;
  act(() => { openStreams()[0].message(); });
  await answered();
  expect(appliesReads(fetchMock)).toBe(2);
  fail = false;
  act(() => { vi.advanceTimersByTime(15_000); });
  await answered();
  expect(appliesReads(fetchMock)).toBe(3);
  act(() => { vi.advanceTimersByTime(30_000); });
  expect(appliesReads(fetchMock)).toBe(3);
});

it('reads the change state every 15 seconds while an apply is under way, and not after', async () => {
  const answered = () => act(async () => { await new Promise((resolve) => setTimeout(resolve)); });
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  let state: Apply['state'] = 'awaiting_confirmation';
  const fetchMock = fetchRoutes({
    '/api/v1/applies': () => jsonResponse(200, { pointers, unapplied: false, items: [apply({ state })] }),
    '/api/v1/versions?': { state: 'saved', items: [] },
  });
  mount('/system/session');
  await answered();
  act(() => { vi.advanceTimersByTime(15_000); });
  expect(appliesReads(fetchMock)).toBe(2);
  state = 'confirmed';
  await answered();
  act(() => { vi.advanceTimersByTime(15_000); });
  expect(appliesReads(fetchMock)).toBe(3);
  await answered();
  act(() => { vi.advanceTimersByTime(30_000); });
  expect(appliesReads(fetchMock)).toBe(3);
});

it('closes its event stream when it goes away', () => {
  routes();
  const { unmount } = render(<MemoryRouter initialEntries={['/system/session']}><Shell api={new ApiClient('csrf')} session={reader} connected onLogout={vi.fn()} onSessionLost={vi.fn()} /></MemoryRouter>);
  expect(openStreams()).toHaveLength(1);
  unmount();
  expect(openStreams()).toHaveLength(0);
});

it('opens no event stream for an account that may not read the configuration', () => {
  fetchRoutes({});
  render(<MemoryRouter initialEntries={['/system/session']}><Shell api={new ApiClient('csrf')} session={session([])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} /></MemoryRouter>);
  expect(openStreams()).toHaveLength(0);
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

it('hides the pending notice and nav badge when a newer saved version has unapplied: false', async () => {
  const fetchMock = fetchRoutes({
    '/api/v1/applies': { pointers: { ...pointers, confirmed: '1', live: '1' }, unapplied: false, items: [apply({})] },
    '/api/v1/versions?': { state: 'saved', items: [{ id: '5', createdAt: '2026-11-21T11:00:00Z', actor: 'admin', changedObjectCount: 1 }] },
    'POST /api/v1/config/upgrade/preview': { savedVersion: '5', questions: [] },
  });
  mount('/system/session');
  // upgradePreview only fires after loadChangeState commits changes.data.head.
  await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url === '/api/v1/config/upgrade/preview')).toBe(true));
  expect(screen.queryByText('Saved changes are not applied yet')).not.toBeInTheDocument();
  expect(screen.queryByText('Pending')).not.toBeInTheDocument();
});

it('says so when the change state cannot be read: no news must not look like nothing pending', async () => {
  fetchRoutes({ '/api/v1/applies': () => jsonResponse(503, { error: { code: 'STORE_UNAVAILABLE', message: 'The applies are unavailable.', details: [] } }), '/api/v1/versions?': { state: 'saved', items: [] } });
  mount('/system/session');
  expect(await screen.findByText('Could not check pending changes')).toBeInTheDocument();
});

it('suppresses duplicate connection-lost banners and stale Loaded errors when disconnected, and recovers on reconnect', async () => {
  let failApplies = false;
  let failUpgrade = true;
  let failUpdate = false;
  const fetchMock = fetchRoutes({
    '/api/v1/applies': () => (failApplies ? Promise.reject(new TypeError('offline')) : jsonResponse(200, unapplied)),
    '/api/v1/versions?': { state: 'saved', items: [{ id: '5', createdAt: '2026-11-21T11:00:00Z', actor: 'admin', changedObjectCount: 1 }] },
    '/api/v1/status/system': () => jsonResponse(200, { version: '0.5.0-a.4', hostname: 'edge', time: '2026-11-21T12:00:00Z', bootedAt: null }),
    '/api/v1/system/update': () => (failUpdate ? Promise.reject(new TypeError('offline')) : jsonResponse(200, {
      supported: true,
      state: 'staged',
      booted: { version: '0.5.0-a.4', signature: 'containerPolicy' },
      staged: { version: '0.5.0-a.5', signature: 'containerPolicy' },
      rebootNeeded: true,
    })),
    'POST /api/v1/system/update/check': () => Promise.reject(new TypeError('offline')),
    'POST /api/v1/config/upgrade/preview': () => (failUpgrade ? Promise.reject(new TypeError('offline')) : jsonResponse(200, { savedVersion: '5', questions: [] })),
  });
  const api = new ApiClient('csrf');
  const full = session(['configuration.read', 'status.read', 'configuration.apply']);
  const onLogout = vi.fn();
  const onSessionLost = vi.fn();
  const shell = (connected: boolean) => (
    <MemoryRouter initialEntries={['/system/updates']}>
      <Shell api={api} session={full} connected={connected} onLogout={onLogout} onSessionLost={onSessionLost} />
    </MemoryRouter>
  );
  const view = render(shell(true));
  expect(await screen.findByText('0.5.0-a.4')).toBeInTheDocument();

  // Trigger real failures on all three resources (changes, upgrade, and Loaded update) while connected=true first
  // so we prove each error actually exists before verifying connected=false suppresses all of them.
  failApplies = true;
  failUpdate = true;
  act(() => { openStreams()[0].fail(false); });
  await userEvent.click(screen.getByRole('button', { name: 'Check for updates' }));
  expect(await screen.findByText('Could not check pending changes')).toBeInTheDocument();
  expect(await screen.findByText('Could not check upgrade status')).toBeInTheDocument();
  // All four disconnect messages render while connected=true: changes, upgrade, actionError, and Loaded(update).
  await waitFor(() => expect(screen.getAllByText(/Cannot reach the Andaru service/i)).toHaveLength(4));

  // Once connected=false, only the header badge owns the offline state: redundant shell banners and Loaded error hide.
  view.rerender(shell(false));
  expect(within(screen.getByRole('banner')).getByRole('status')).toHaveTextContent('Connection lost');
  expect(screen.queryByText(/The box does not answer/i)).not.toBeInTheDocument();
  expect(screen.queryByText('Could not check pending changes')).not.toBeInTheDocument();
  expect(screen.queryByText('Could not check upgrade status')).not.toBeInTheDocument();
  // The Loaded region on UpdatesScreen hides its disconnect error while cached data is visible.
  expect(screen.queryAllByText(/Cannot reach the Andaru service/i)).toHaveLength(1); // only actionError from clicking Check

  // Reconnecting clears disconnect errors and refreshes applies, update, system, and upgrade exactly once each.
  const appliesReadsBefore = appliesReads(fetchMock);
  const updateReadsBefore = fetchMock.mock.calls.filter(([url]) => url === '/api/v1/system/update').length;
  const systemReadsBefore = fetchMock.mock.calls.filter(([url]) => url === '/api/v1/status/system').length;
  const upgradeReadsBefore = fetchMock.mock.calls.filter(([url]) => url === '/api/v1/config/upgrade/preview').length;
  failApplies = false;
  failUpgrade = false;
  failUpdate = false;
  view.rerender(shell(true));
  await waitFor(() => {
    expect(appliesReads(fetchMock)).toBe(appliesReadsBefore + 1);
    expect(fetchMock.mock.calls.filter(([url]) => url === '/api/v1/system/update').length).toBe(updateReadsBefore + 1);
    expect(fetchMock.mock.calls.filter(([url]) => url === '/api/v1/status/system').length).toBe(systemReadsBefore + 1);
    expect(fetchMock.mock.calls.filter(([url]) => url === '/api/v1/config/upgrade/preview').length).toBe(upgradeReadsBefore + 1);
  });
  expect(screen.queryByText('Could not check pending changes')).not.toBeInTheDocument();
  expect(screen.queryByText('Could not check upgrade status')).not.toBeInTheDocument();
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
  let hostname = 'edge';
  fetchRoutes({
    '/api/v1/status/system': () => jsonResponse(200, { version: '0.5.0-a.4', hostname, kernel: '6.17', architecture: 'x86_64', time: '2026-11-21T12:00:00Z', bootedAt: null, memoryBytes: 0, guest: '', iommu: false,
      cpu: { model: 'Test CPU', cores: 2, threads: 2, microcode: '', level: '', features: { cryptography: [], security: [], virtualization: [] }, vulnerabilities: [] },
      hardware: { vendor: '', product: '', serial: '', boardVendor: '', board: '', biosVendor: '', biosVersion: '', biosDate: '' } }),
    'POST /api/v1/system/hostname': () => {
      hostname = 'core-gw';
      return jsonResponse(200, { hostname });
    },
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
  });
  const { unmount } = render(<MemoryRouter initialEntries={['/system/details']}><Shell api={new ApiClient('csrf')} session={session(['status.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} /></MemoryRouter>);
  expect(await screen.findByRole('heading', { name: 'System details' })).toBeInTheDocument();
  expect(await screen.findByRole('region', { name: 'Processor' })).toHaveTextContent('Test CPU, 2 cores');
  expect(screen.queryByRole('button', { name: 'Change hostname' })).not.toBeInTheDocument();
  unmount();

  const user = userEvent.setup();
  render(<MemoryRouter initialEntries={['/system/details']}><Shell api={new ApiClient('csrf')} session={session(['status.read', 'configuration.apply'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} /></MemoryRouter>);
  await user.click(await screen.findByRole('button', { name: 'Change hostname' }));
  const input = await screen.findByLabelText(/Hostname/i, { selector: 'input' });
  await user.clear(input);
  await user.type(input, 'core-gw');
  await user.click(screen.getByRole('button', { name: 'Save hostname' }));
  expect(await screen.findByText('Hostname updated to core-gw.')).toBeInTheDocument();
  await waitFor(() => expect(document.title).toContain('core-gw'));
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
  expect(await screen.findByText('Could not check upgrade status')).toBeInTheDocument();
});

it('asks again every 15 seconds whether decisions wait, while it cannot tell', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  let down = true;
  const fetchMock = fetchRoutes({
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    'POST /api/v1/config/upgrade/preview': () => (down ? jsonResponse(503, { error: { code: 'STORE_UNAVAILABLE', message: 'Unavailable.' } }) : jsonResponse(200, { savedVersion: '5', questions: [question] })),
  });
  mount('/system/session');
  expect(await screen.findByText('Could not check upgrade status')).toBeInTheDocument();
  const previews = () => fetchMock.mock.calls.filter(([url]) => url === '/api/v1/config/upgrade/preview').length;
  const before = previews();
  down = false;
  act(() => { vi.advanceTimersByTime(15_000); });
  expect(previews()).toBe(before + 1);
  expect(await screen.findByRole('link', { name: 'Answer the questions' })).toBeInTheDocument();
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
  // The page is in no branch, so Changes is closed and carries the badge itself.
  await userEvent.click(await screen.findByRole('button', { name: /Changes.*Pending/i }));
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
  await vi.waitFor(() => expect(document.title).toBe('Device — edge — Andaru'));
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

it('shows Update and Reboot badges on the System -> Updates navigation link', async () => {
  fetchRoutes({
    '/api/v1/status/system': { version: '0.5.0-a.4', hostname: 'edge', time: '2026-11-21T12:00:00Z', bootedAt: null },
    '/api/v1/system/update': { supported: true, state: 'available', available: { version: '0.5.0-a.5' }, rebootNeeded: false },
  });
  const first = render(
    <MemoryRouter initialEntries={['/system/session']}>
      <Shell api={new ApiClient('csrf')} session={session(['status.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(await screen.findByRole('button', { name: /System.*Update/i })).toBeInTheDocument();
  first.unmount();

  fetchRoutes({
    '/api/v1/status/system': { version: '0.5.0-a.4', hostname: 'edge', time: '2026-11-21T12:00:00Z', bootedAt: null },
    '/api/v1/system/update': { supported: true, state: 'staged', staged: { version: '0.5.0-a.5' }, rebootNeeded: true },
  });
  render(
    <MemoryRouter initialEntries={['/system/session']}>
      <Shell api={new ApiClient('csrf')} session={session(['status.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(await screen.findByRole('button', { name: /System.*Reboot/i })).toBeInTheDocument();
});

it('lets the user pick a display time zone from the account menu and updates timestamps immediately', async () => {
  const user = userEvent.setup();
  routes();
  mount('/system/session');
  // The header button uses an icon instead of printing the display name; user details appear at the top of the menu.
  expect(screen.queryByRole('button', { name: 'Administrator' })).not.toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: 'Account (admin)' }));
  const menu = await screen.findByRole('menu');
  expect(within(menu).getByText('admin')).toBeInTheDocument();
  expect(within(menu).getByText('Administrator')).toBeInTheDocument();
  await user.click(await screen.findByRole('menuitem', { name: 'Time zone…' }, { timeout: 5000 }));
  const input = await screen.findByRole('combobox', { name: 'Time zone' }, { timeout: 5000 });
  await user.clear(input);
  await user.paste('Europe/Berlin');
  await user.keyboard('{ArrowDown}{Enter}');
  await user.click(screen.getByRole('button', { name: 'Done' }));
  expect(screen.getByText('2026-11-16 13:00:00 +01:00')).toBeInTheDocument();
}, 15_000);

it('does not repeat the login in the account menu when displayName matches login or is blank', async () => {
  const user = userEvent.setup();
  routes();
  const sameName = { ...reader, account: { ...reader.account, login: 'admin', displayName: ' admin ' } };
  render(
    <MemoryRouter initialEntries={['/system/session']}>
      <Shell api={new ApiClient('csrf')} session={sameName} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  await user.click(screen.getByRole('button', { name: 'Account (admin)' }));
  const menu = await screen.findByRole('menu');
  expect(within(menu).getAllByText('admin')).toHaveLength(1);
});

const notification = (id: number, severity: 'info' | 'warning' | 'critical', subject = `Notice ${id}`) => ({
  id,
  source: 'disk',
  key: `disk/${id}`,
  severity,
  subject,
  raised: '2026-11-23T10:00:00Z',
  updated: '2026-11-23T10:00:00Z',
});

it('dispatches stream events by type so notifications.changed refreshes only notifications and change events refresh only changes', async () => {
  const answered = () => act(async () => { await new Promise((resolve) => setTimeout(resolve)); });
  const fetchMock = fetchRoutes({
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    '/api/v1/notifications': { items: [notification(1, 'info')] },
  });
  const notificationsReads = () => fetchMock.mock.calls.filter(([url]) => url === '/api/v1/notifications').length;

  render(
    <MemoryRouter initialEntries={['/system/session']}>
      <Shell api={new ApiClient('csrf')} session={session(['configuration.read', 'notifications.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  await answered();
  expect(appliesReads(fetchMock)).toBe(1);
  expect(notificationsReads()).toBe(1);

  const [stream] = openStreams();
  act(() => { stream.open(); });
  await answered();
  expect(appliesReads(fetchMock)).toBe(2);
  expect(notificationsReads()).toBe(2);

  act(() => { stream.message({ type: 'notifications.changed' }); });
  await answered();
  expect(appliesReads(fetchMock)).toBe(2);
  expect(notificationsReads()).toBe(3);

  act(() => { stream.message({ type: 'config.head.changed' }); });
  await answered();
  expect(appliesReads(fetchMock)).toBe(3);
  expect(notificationsReads()).toBe(3);

  act(() => { stream.message({ type: 'apply.changed' }); });
  await answered();
  expect(appliesReads(fetchMock)).toBe(4);
  expect(notificationsReads()).toBe(3);

  // A type it does not know, or a message it cannot read, reloads everything rather than miss a change.
  act(() => { stream.message({ type: 'other.event' }); });
  await answered();
  expect(appliesReads(fetchMock)).toBe(5);
  expect(notificationsReads()).toBe(4);

  act(() => { stream.message('not json'); });
  await answered();
  expect(appliesReads(fetchMock)).toBe(6);
  expect(notificationsReads()).toBe(5);
});

it('reads the notifications again when the connection comes back, and every 15 seconds while a read fails', async () => {
  vi.useFakeTimers();
  try {
    let fail = true;
    const fetchMock = fetchRoutes({
      '/api/v1/notifications': () => (fail ? jsonResponse(503, { error: { code: 'NOTIFICATIONS_UNAVAILABLE', message: 'Unavailable.' } }) : jsonResponse(200, { items: [] })),
    });
    const reads = () => fetchMock.mock.calls.filter(([url]) => url === '/api/v1/notifications').length;
    const api = new ApiClient('csrf');
    const readNotifications = session(['notifications.read']);
    const [onLogout, onSessionLost] = [vi.fn(), vi.fn()];
    const view = (connected: boolean) => (
      <MemoryRouter initialEntries={['/system/session']}>
        <Shell api={api} session={readNotifications} connected={connected} onLogout={onLogout} onSessionLost={onSessionLost} />
      </MemoryRouter>
    );
    const { rerender } = render(view(true));
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(reads()).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
    expect(reads()).toBe(2);
    fail = false;
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
    expect(reads()).toBe(3);
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(reads()).toBe(3);

    rerender(view(false));
    expect(reads()).toBe(3);
    rerender(view(true));
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(reads()).toBe(4);
  } finally {
    vi.useRealTimers();
  }
});

it('does not show an out-of-date count as current once a read fails', async () => {
  let fail = false;
  fetchRoutes({
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    '/api/v1/notifications': () => (fail ? jsonResponse(503, { error: { code: 'NOTIFICATIONS_UNAVAILABLE', message: 'Unavailable.' } }) : jsonResponse(200, { items: [] })),
  });
  render(
    <MemoryRouter initialEntries={['/system/session']}>
      <Shell api={new ApiClient('csrf')} session={session(['configuration.read', 'notifications.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(await screen.findByRole('link', { name: '0 notifications' })).toBeInTheDocument();
  fail = true;
  const [stream] = openStreams();
  act(() => { stream.message({ type: 'notifications.changed' }); });
  expect(await screen.findByRole('link', { name: 'Notifications unavailable' })).toHaveTextContent('—');
});

it('opens the event stream with only notifications.read without requesting changes', async () => {
  const fetchMock = fetchRoutes({
    '/api/v1/notifications': { items: [] },
  });
  render(
    <MemoryRouter initialEntries={['/system/session']}>
      <Shell api={new ApiClient('csrf')} session={session(['notifications.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(openStreams()).toHaveLength(1);
  expect(await screen.findByRole('link', { name: '0 notifications' })).toBeInTheDocument();
  expect(appliesReads(fetchMock)).toBe(0);
});

it.each([
  { label: 'critical wins over warning and info', items: [notification(1, 'info'), notification(2, 'warning'), notification(3, 'critical')], severity: 'critical', color: 'red', count: '3' },
  { label: 'warning wins over info', items: [notification(1, 'info'), notification(2, 'warning')], severity: 'warning', color: 'orange', count: '2' },
  { label: 'info is blue', items: [notification(1, 'info')], severity: 'info', color: 'blue', count: '1' },
  { label: 'zero is neutral', items: [], severity: 'none', color: null, count: '0' },
])('colours the header notification count by highest severity: $label', async ({ items, severity, count }) => {
  fetchRoutes({ '/api/v1/notifications': { items } });
  render(
    <MemoryRouter initialEntries={['/system/session']}>
      <Shell api={new ApiClient('csrf')} session={session(['notifications.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  const link = await screen.findByRole('link', { name: /^\d+ notification/ });
  expect(link).toHaveTextContent(count);
  expect(link).toHaveAttribute('data-severity', severity);
});

it('hides header notifications and nav link without notifications.read, and shares one fetch across header, dashboard and page', async () => {
  const withoutCap = fetchRoutes({});
  const first = render(
    <MemoryRouter initialEntries={['/system/session']}>
      <Shell api={new ApiClient('csrf')} session={session([])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(screen.queryByRole('link', { name: /notification/i })).not.toBeInTheDocument();
  expect(withoutCap.mock.calls.map(([url]) => url)).not.toContain('/api/v1/notifications');
  first.unmount();

  const fetchMock = fetchRoutes({
    '/api/v1/notifications': { items: [notification(1, 'warning', 'Disk almost full')] },
    '/api/v1/session/preferences': { dashboard: {} },
  });
  render(
    <MemoryRouter initialEntries={['/']}>
      <Shell api={new ApiClient('csrf')} session={session(['notifications.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  const bell = await screen.findByRole('link', { name: '1 notification, highest severity warning' });
  expect(await screen.findByRole('region', { name: 'Notifications' })).toHaveTextContent('Disk almost full');
  expect(fetchMock.mock.calls.filter(([url]) => url === '/api/v1/notifications')).toHaveLength(1);

  await userEvent.click(bell);
  expect(await screen.findByRole('heading', { name: 'Notifications' })).toBeInTheDocument();
  expect(document.title).toBe('Notifications — Andaru');
  expect(fetchMock.mock.calls.filter(([url]) => url === '/api/v1/notifications')).toHaveLength(1);
});

it('shows the 503 state in the header and on the Notifications page', async () => {
  fetchRoutes({
    '/api/v1/notifications': () => jsonResponse(503, { error: { code: 'NOTIFICATIONS_UNAVAILABLE', message: 'Notifications are temporarily unavailable.' } }),
  });
  render(
    <MemoryRouter initialEntries={['/system/notifications']}>
      <Shell api={new ApiClient('csrf')} session={session(['notifications.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(await screen.findByRole('link', { name: 'Notifications unavailable' })).toHaveAttribute('data-severity', 'unavailable');
  const alert = await screen.findByRole('alert');
  expect(alert).toHaveTextContent('The notifications could not be read');
  expect(alert).toHaveTextContent('Notifications are temporarily unavailable.');
});

it('lets a configuration.read-only account open /system/notifications to view channels without the header bell', async () => {
  fetchRoutes({
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    '/api/v1/config/notifications/channels': { state: 'saved', savedVersion: '10', items: [] },
  });
  render(
    <MemoryRouter initialEntries={['/system/notifications']}>
      <Shell api={new ApiClient('csrf')} session={session(['configuration.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(await screen.findByRole('heading', { name: 'Notifications' })).toBeInTheDocument();
  expect(await screen.findByText('No notification channels')).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Notifications' })).toBeInTheDocument();
  expect(document.querySelector('.notification-button')).toBeNull();
});

it('shows System -> Backups only when the session has backup capabilities', async () => {
  fetchRoutes({});
  const first = render(
    <MemoryRouter initialEntries={['/system/session']}>
      <Shell api={new ApiClient('csrf')} session={session(['status.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(screen.queryByRole('link', { name: 'Backups' })).not.toBeInTheDocument();
  first.unmount();

  render(
    <MemoryRouter initialEntries={['/system/backups']}>
      <Shell api={new ApiClient('csrf')} session={session(['backups.create'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(await screen.findByRole('heading', { name: 'Backup & restore' })).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Backups' })).toHaveAttribute('aria-current', 'page');
  expect(document.title).toBe('Backup & restore — Andaru');
});

it('shows Network -> Virtual IPs when the session can read configuration', async () => {
  fetchRoutes({
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    '/api/v1/upgrade/preview': { questions: [] },
    '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '1', items: [] },
    '/api/v1/config': { state: 'saved', savedVersion: '1', value: { links: [], segments: [], zones: [] } },
  });
  render(
    <MemoryRouter initialEntries={['/network/virtual-ips']}>
      <Shell api={new ApiClient('csrf')} session={session(['configuration.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(await screen.findByRole('heading', { name: 'Virtual IPs' })).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Virtual IPs' })).toHaveAttribute('aria-current', 'page');
  expect(document.title).toBe('Virtual IPs — Andaru');
});

it('shows Diagnostics -> Traffic simulator when the session can read configuration', async () => {
  fetchRoutes({
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    '/api/v1/upgrade/preview': { questions: [] },
    '/api/v1/config': { state: 'saved', savedVersion: '1', value: { links: [], segments: [], zones: [] } },
  });
  render(
    <MemoryRouter initialEntries={['/diagnostics/traffic-flow']}>
      <Shell api={new ApiClient('csrf')} session={session(['configuration.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(await screen.findByRole('heading', { name: 'Traffic simulator' })).toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Traffic simulator' })).toHaveAttribute('aria-current', 'page');
  expect(document.title).toBe('Traffic simulator — Andaru');
});

it('keeps the event stream open while hidden when browser notifications are enabled and granted, and delivers native alerts', async () => {
  const answered = () => act(async () => { await new Promise((resolve) => setTimeout(resolve)); });
  const shown: string[] = [];
  class FakeNotification {
    close = vi.fn();
    onclick: (() => void) | null = null;
    constructor(title: string) { shown.push(title); }
    static get permission(): NotificationPermission { return 'granted'; }
    static requestPermission = vi.fn(async () => 'granted' as NotificationPermission);
  }
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
  Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: FakeNotification });
  setNotificationPrefs({ browser: true });

  let list = [notification(1, 'info', 'Initial alert')];
  fetchRoutes({
    '/api/v1/notifications': () => jsonResponse(200, { items: list }),
  });

  render(
    <MemoryRouter initialEntries={['/system/session']}>
      <Shell api={new ApiClient('csrf')} session={session(['notifications.read'])} connected onLogout={vi.fn()} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  await answered();
  expect(openStreams()).toHaveLength(1);
  expect(shown).toHaveLength(0);

  // Hiding the tab keeps the stream open because browser notifications are active.
  visibility = 'hidden';
  act(() => { document.dispatchEvent(new Event('visibilitychange')); });
  expect(openStreams()).toHaveLength(1);

  list = [notification(2, 'critical', 'WAN link down'), notification(1, 'info', 'Initial alert')];
  act(() => { openStreams()[0].message({ type: 'notifications.changed' }); });
  await answered();
  expect(shown).toEqual(['WAN link down']);

  // Turning browser notifications off closes the hidden tab's stream.
  act(() => { setNotificationPrefs({ browser: false }); });
  expect(openStreams()).toHaveLength(0);
});
