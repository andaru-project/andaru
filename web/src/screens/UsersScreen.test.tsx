import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import type { Session } from '../types';
import { render, screen } from '../test/render';
import { fetchQueue, jsonResponse } from '../test/fetch';
import { UsersScreen } from './UsersScreen';

const baseSession: Session = {
  account: { id: '0657cce4-d4f9-4d1e-9cae-53a99855bb10', login: 'admin', displayName: 'Administrator' },
  capabilities: ['users.read'], csrfToken: 'csrf', expiresAt: '2026-11-16T12:00:00Z', idleTimeoutSeconds: 1800,
};
const userPage = { items: [{ id: baseSession.account.id, login: 'admin', displayName: 'Administrator', kind: 'human', enabled: true, roleIds: ['administrator'], revision: 4, createdAt: 1, updatedAt: 1 }] };
const roles = { items: [{ id: 'administrator', name: 'Administrator', capabilities: ['users.read', 'users.manage'], builtIn: 'administrator' }] };

it('hides mutation controls when the session lacks users.manage', async () => {
  fetchQueue(jsonResponse(200, userPage), jsonResponse(200, roles));
  render(<UsersScreen api={new ApiClient()} session={baseSession} onSessionLost={vi.fn()} />);

  expect(await screen.findByText('Administrator')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Create user' })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /edit admin/i })).not.toBeInTheDocument();
});

it('shows server refusal for deleting the recovery administrator', async () => {
  fetchQueue(
    jsonResponse(200, userPage), jsonResponse(200, roles),
    jsonResponse(409, { error: { code: 'RECOVERY_ADMIN_REQUIRED', message: 'At least one enabled password-capable built-in administrator is required.', details: [] } }),
  );
  render(<UsersScreen api={new ApiClient('csrf')} session={{ ...baseSession, capabilities: ['users.read', 'users.manage'] }} onSessionLost={vi.fn()} />);

  await userEvent.click(await screen.findByRole('button', { name: /delete admin/i }));
  await userEvent.click(screen.getByRole('button', { name: 'Delete user' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('At least one enabled password-capable built-in administrator is required.');
});

it('shows a server 403 even when the capability advertised a control', async () => {
  fetchQueue(
    jsonResponse(200, userPage), jsonResponse(200, roles),
    jsonResponse(403, { error: { code: 'FORBIDDEN', message: 'The server denied this user change.', details: [] } }),
  );
  render(<UsersScreen api={new ApiClient('csrf')} session={{ ...baseSession, capabilities: ['users.read', 'users.manage'] }} onSessionLost={vi.fn()} />);

  await userEvent.click(await screen.findByRole('button', { name: /delete admin/i }));
  await userEvent.click(screen.getByRole('button', { name: 'Delete user' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('The server denied this user change.');
});

it('states the password policy and keeps built-in administrator resets console-only', async () => {
  fetchQueue(jsonResponse(200, userPage), jsonResponse(200, roles));
  render(<UsersScreen api={new ApiClient()} session={{ ...baseSession, capabilities: ['users.read', 'users.manage'] }} onSessionLost={vi.fn()} />);

  await userEvent.click(await screen.findByRole('button', { name: /reset password for admin/i }));
  expect(screen.getByRole('dialog')).toHaveTextContent('Built-in administrator passwords can only be reset from the root console.');
  expect(screen.getByRole('dialog')).toHaveTextContent('15–128 Unicode characters');
  expect(screen.queryByLabelText('New password')).toBeDisabled();
});
