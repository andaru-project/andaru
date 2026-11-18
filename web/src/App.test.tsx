import userEvent from '@testing-library/user-event';
import { expect, it } from 'vitest';
import { fetchQueue, jsonResponse } from './test/fetch';
import { render, screen } from './test/render';
import { App } from './App';

it('shows a session-expiry state instead of leaving stale privileged controls', async () => {
  fetchQueue(
    jsonResponse(200, { account: { id: '0657cce4-d4f9-4d1e-9cae-53a99855bb10', login: 'admin', displayName: 'Administrator' }, capabilities: ['audit.read'], csrfToken: 'csrf', expiresAt: '2026-11-16T12:00:00Z', idleTimeoutSeconds: 1800 }),
    jsonResponse(401, { error: { code: 'SESSION_EXPIRED', message: 'Sign in again.', details: [] } }),
    jsonResponse(200, { setupRequired: false }),
  );
  render(<App />);

  await userEvent.click(await screen.findByRole('button', { name: 'Audit' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Your session expired');
  expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument();
});

it('offers a bounded retry when the web service is disconnected', async () => {
  fetchQueue(new TypeError('connection refused'));
  render(<App />);

  expect(await screen.findByRole('alert')).toHaveTextContent('Cannot reach the Andaru service');
  expect(screen.getByRole('button', { name: 'Retry connection' })).toBeInTheDocument();
});
