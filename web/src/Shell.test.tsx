import { MemoryRouter } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { ApiClient } from './api';
import type { Apply } from './types';
import { Shell } from './Shell';
import { openStreams } from './test/events';
import { fetchRoutes, jsonResponse } from './test/fetch';
import { apply, pointers, session } from './test/fixtures';
import { act, render, screen } from './test/render';

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
    '/api/v1/applies': () => jsonResponse(200, { pointers, items: [apply({ state })] }),
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

it('asks again every 15 seconds whether decisions wait, while it cannot tell', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  let down = true;
  const fetchMock = fetchRoutes({
    '/api/v1/applies': unapplied,
    '/api/v1/versions?': { state: 'saved', items: [] },
    'POST /api/v1/config/upgrade/preview': () => (down ? jsonResponse(503, { error: { code: 'STORE_UNAVAILABLE', message: 'Unavailable.' } }) : jsonResponse(200, { savedVersion: '5', questions: [question] })),
  });
  mount('/system/session');
  expect(await screen.findByText('Whether the settings wait for decisions cannot be read')).toBeInTheDocument();
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
