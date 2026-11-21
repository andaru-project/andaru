import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'vitest';
import { fetchQueue, fetchRoutes, jsonResponse } from './test/fetch';
import { render, screen, within } from './test/render';
import { App } from './App';

const account = { id: '0657cce4-d4f9-4d1e-9cae-53a99855bb10', login: 'admin', displayName: 'Administrator' };
const session = (capabilities: string[]) => ({ account, capabilities, csrfToken: 'csrf', expiresAt: '2026-11-16T12:00:00Z', idleTimeoutSeconds: 1800 });

beforeEach(() => {
  window.history.pushState({}, '', '/');
  // jsdom here has no storage; the shell must work without one, and one test needs it to remember.
  const stored = new Map<string, string>();
  vi.stubGlobal('localStorage', { getItem: (key: string) => stored.get(key) ?? null, setItem: (key: string, value: string) => { stored.set(key, value); } });
});

it('shows a session-expiry state instead of leaving stale privileged controls', async () => {
  fetchQueue(
    jsonResponse(200, session(['audit.read'])),
    jsonResponse(401, { error: { code: 'SESSION_EXPIRED', message: 'Sign in again.', details: [] } }),
  );
  window.history.pushState({}, '', '/system/audit');
  render(<App />);

  expect(await screen.findByRole('alert')).toHaveTextContent('Your session expired');
  expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument();
});

it('offers a bounded retry when the web service is disconnected', async () => {
  fetchQueue(new TypeError('connection refused'));
  render(<App />);

  expect(await screen.findByRole('alert')).toHaveTextContent('Cannot reach the Andaru service');
  expect(screen.getByRole('button', { name: 'Retry connection' })).toBeInTheDocument();
});

it('shows only the navigation the capabilities allow and routes between pages', async () => {
  fetchRoutes({
    '/api/v1/session': session(['users.read', 'audit.read']),
    '/api/v1/users': { items: [] },
    '/api/v1/roles': { items: [] },
    '/api/v1/audit/events': { items: [] },
  });
  render(<App />);

  const navigation = await screen.findByRole('navigation', { name: 'Main navigation' });
  expect(within(navigation).getAllByRole('link').map((link) => link.textContent)).toEqual(['Dashboard', 'Users', 'Roles', 'Audit']);
  expect(within(navigation).queryByText('Network')).not.toBeInTheDocument();
  expect(within(navigation).queryByText('Changes')).not.toBeInTheDocument();
  expect(window.location.pathname).toBe('/');
  expect(within(navigation).getByRole('link', { name: 'Dashboard' })).toHaveAttribute('aria-current', 'page');
  expect(screen.getByText('Connected')).toBeInTheDocument();

  await userEvent.click(within(navigation).getByRole('link', { name: 'Audit' }));
  expect(await screen.findByRole('heading', { name: 'Security audit' })).toBeInTheDocument();
  expect(window.location.pathname).toBe('/system/audit');
  expect(within(navigation).getByRole('link', { name: 'Audit' })).toHaveAttribute('aria-current', 'page');
});

it('shows every branch to an administrator and starts on the dashboard', async () => {
  fetchRoutes({
    '/api/v1/session': session(['status.read', 'configuration.read', 'configuration.apply', 'users.read', 'tokens.manageSelf', 'audit.read']),
    '/api/v1/applies': { pointers: { confirmed: null, lastKnownGood: null, pending: null, live: null }, items: [] },
    '/api/v1/versions?': { state: 'saved', items: [{ id: '0', createdAt: '2026-11-21T10:00:00Z', actor: 'system.root', changedObjectCount: 0 }] },
    '/api/v1/status/links': { items: [] },
    '/api/v1/status/prefixes': { wans: [] },
    '/api/v1/audit/events': { items: [] },
    '/api/v1/config': { state: 'saved', savedVersion: '0', value: { links: [], segments: [], zones: [] }, unusedSecrets: [] },
    '/api/v1/preview/network': { state: 'preview', savedVersion: '0', realizable: true, resolved: [], files: [], findings: [] },
  });
  render(<App />);

  const navigation = await screen.findByRole('navigation', { name: 'Main navigation' });
  // A branch label is a button, so the keyboard reaches it; it says whether it is open.
  for (const label of ['Network', 'Changes', 'System']) expect(within(navigation).getByRole('button', { name: label })).toHaveAttribute('aria-expanded', 'true');
  expect(within(navigation).getAllByRole('link').map((link) => link.textContent)).toEqual(['Dashboard', 'Interfaces', 'Review and apply', 'Users', 'Roles', 'Tokens', 'Audit']);
  expect(await screen.findByRole('heading', { name: 'Dashboard' })).toBeInTheDocument();
  expect(window.location.pathname).toBe('/');

  await userEvent.click(within(navigation).getByRole('link', { name: 'Interfaces' }));
  expect(await screen.findByRole('heading', { name: 'Interfaces', level: 2 })).toBeInTheDocument();
});

it('keeps a closed branch closed across visits, and opens the branch of the page it lands on', async () => {
  const routes = {
    '/api/v1/session': session(['users.read', 'audit.read']),
    '/api/v1/users': { items: [] },
    '/api/v1/roles': { items: [] },
    '/api/v1/audit/events': { items: [] },
  };
  fetchRoutes(routes);
  const first = render(<App />);
  const system = await screen.findByRole('button', { name: 'System' });
  await userEvent.click(system);
  expect(system).toHaveAttribute('aria-expanded', 'false');
  first.unmount();

  fetchRoutes(routes);
  const second = render(<App />);
  expect(await screen.findByRole('button', { name: 'System' })).toHaveAttribute('aria-expanded', 'false');
  second.unmount();

  window.history.pushState({}, '', '/system/audit');
  fetchRoutes(routes);
  render(<App />);
  expect(await screen.findByRole('button', { name: 'System' })).toHaveAttribute('aria-expanded', 'true');
  expect(await screen.findByRole('heading', { name: 'Security audit' })).toBeInTheDocument();
});

it('opens a closed branch when a link inside the session leads into it, and Space toggles a branch', async () => {
  fetchRoutes({
    '/api/v1/session': session(['status.read', 'audit.read']),
    '/api/v1/status/links': { items: [] },
    '/api/v1/status/prefixes': { wans: [] },
    '/api/v1/audit/events': { items: [] },
  });
  render(<App />);
  const network = await screen.findByRole('button', { name: 'Network' });
  network.focus();
  await userEvent.keyboard(' ');
  expect(network).toHaveAttribute('aria-expanded', 'false');

  // The Interfaces widget's title leads into the closed branch.
  await userEvent.click(screen.getByRole('link', { name: 'Interfaces' }));
  expect(await screen.findByRole('heading', { name: 'Interfaces', level: 2 })).toBeInTheDocument();
  expect(network).toHaveAttribute('aria-expanded', 'true');
});

it('marks the connection lost when a request gets no response', async () => {
  let reachable = true;
  fetchRoutes({
    '/api/v1/session': session(['audit.read']),
    '/api/v1/audit/events': () => (reachable ? jsonResponse(200, { items: [], nextCursor: '1' }) : new TypeError('offline')),
  });
  window.history.pushState({}, '', '/system/audit');
  render(<App />);

  const more = await screen.findByRole('button', { name: 'Load more events' });
  reachable = false;
  await userEvent.click(more);
  expect(await screen.findByText('Connection lost', { selector: '.mantine-Badge-label' })).toBeInTheDocument();
});
