import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it } from 'vitest';
import { fetchQueue, fetchRoutes, jsonResponse } from './test/fetch';
import { render, screen, within } from './test/render';
import { App } from './App';

const account = { id: '0657cce4-d4f9-4d1e-9cae-53a99855bb10', login: 'admin', displayName: 'Administrator' };
const session = (capabilities: string[]) => ({ account, capabilities, csrfToken: 'csrf', expiresAt: '2026-11-16T12:00:00Z', idleTimeoutSeconds: 1800 });

beforeEach(() => window.history.pushState({}, '', '/'));

it('shows a session-expiry state instead of leaving stale privileged controls', async () => {
  fetchQueue(
    jsonResponse(200, session(['audit.read'])),
    jsonResponse(401, { error: { code: 'SESSION_EXPIRED', message: 'Sign in again.', details: [] } }),
  );
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
  expect(within(navigation).getAllByRole('link').map((link) => link.textContent)).toEqual(['Users', 'Roles', 'Audit']);
  expect(within(navigation).queryByText('Network')).not.toBeInTheDocument();
  expect(within(navigation).queryByText('Changes')).not.toBeInTheDocument();
  expect(window.location.pathname).toBe('/system/users');
  expect(screen.getByText('Connected')).toBeInTheDocument();

  await userEvent.click(within(navigation).getByRole('link', { name: 'Audit' }));
  expect(await screen.findByRole('heading', { name: 'Security audit' })).toBeInTheDocument();
  expect(window.location.pathname).toBe('/system/audit');
  expect(within(navigation).getByRole('link', { name: 'Audit' })).toHaveAttribute('aria-current', 'page');
});

it('shows every group to an administrator and starts on Interfaces', async () => {
  fetchRoutes({
    '/api/v1/session': session(['status.read', 'configuration.read', 'configuration.apply', 'users.read', 'tokens.manageSelf', 'audit.read']),
    '/api/v1/applies': { pointers: { confirmed: null, lastKnownGood: null, pending: null, live: null }, items: [] },
    '/api/v1/versions?': { state: 'saved', items: [{ id: '0', createdAt: '2026-11-21T10:00:00Z', actor: 'system.root', changedObjectCount: 0 }] },
    '/api/v1/status/links': { items: [] },
    '/api/v1/config': { state: 'saved', savedVersion: '0', value: { links: [], segments: [], zones: [] }, unusedSecrets: [] },
    '/api/v1/preview/network': { state: 'preview', savedVersion: '0', realizable: true, resolved: [], files: [], findings: [] },
  });
  render(<App />);

  const navigation = await screen.findByRole('navigation', { name: 'Main navigation' });
  for (const label of ['Network', 'Changes', 'System']) expect(within(navigation).getByText(label)).toBeInTheDocument();
  expect(within(navigation).getAllByRole('link').map((link) => link.textContent)).toEqual(['Interfaces', 'Review and apply', 'Users', 'Roles', 'Tokens', 'Audit']);
  expect(await screen.findByRole('heading', { name: 'Interfaces' })).toBeInTheDocument();
  expect(window.location.pathname).toBe('/network/interfaces');
});

it('marks the connection lost when a request gets no response', async () => {
  let reachable = true;
  fetchRoutes({
    '/api/v1/session': session(['audit.read']),
    '/api/v1/audit/events': () => (reachable ? jsonResponse(200, { items: [], nextCursor: '1' }) : new TypeError('offline')),
  });
  render(<App />);

  const more = await screen.findByRole('button', { name: 'Load more events' });
  reachable = false;
  await userEvent.click(more);
  expect(await screen.findByText('Connection lost', { selector: '.mantine-Badge-label' })).toBeInTheDocument();
});
