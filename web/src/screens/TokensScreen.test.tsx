import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import type { Session } from '../types';
import { fetchQueue, jsonResponse } from '../test/fetch';
import { render, screen } from '../test/render';
import { TokensScreen } from './TokensScreen';

const session: Session = {
  account: { id: '0657cce4-d4f9-4d1e-9cae-53a99855bb10', login: 'admin', displayName: 'Administrator' },
  capabilities: ['tokens.manageSelf', 'status.read'], csrfToken: 'csrf', expiresAt: '2026-11-16T12:00:00Z', idleTimeoutSeconds: 1800,
};

it('requires an explicit no-expiry choice and gives a persistent warning', async () => {
  fetchQueue(jsonResponse(200, { items: [] }));
  render(<TokensScreen api={new ApiClient('csrf')} session={session} onSessionLost={vi.fn()} />);

  await userEvent.click(await screen.findByRole('button', { name: 'Create token' }));
  await userEvent.click(screen.getByLabelText('Token never expires'));
  expect(screen.getByRole('alert')).toHaveTextContent('remains valid until it is revoked');
});

it('shows the created secret once and removes it when dismissed', async () => {
  fetchQueue(
    jsonResponse(200, { items: [] }),
    jsonResponse(201, { operationId: 'op', result: 'success', target: 'selector', metadata: {}, selector: 'selector', secret: 'one-time-secret', secretUnavailable: false, token: { selector: 'selector', accountId: session.account.id, name: 'automation', scope: ['status.read'], createdAt: '2026-11-16T10:00:00Z', noExpiry: false, expiresAt: '2027-02-14T23:59:59Z', revokeOnPasswordChange: true, revision: 1 } }),
    jsonResponse(200, { items: [] }),
  );
  render(<TokensScreen api={new ApiClient('csrf')} session={session} onSessionLost={vi.fn()} />);

  await userEvent.click(await screen.findByRole('button', { name: 'Create token' }));
  await userEvent.type(screen.getByLabelText('Token name'), 'automation');
  await userEvent.click(screen.getByLabelText('status.read'));
  await userEvent.click(screen.getByRole('button', { name: 'Create token', hidden: false }));

  expect(await screen.findByLabelText('One-time bearer token')).toHaveValue('selector.one-time-secret');
  await userEvent.click(screen.getByRole('button', { name: 'I saved the secret' }));
  expect(screen.queryByText('one-time-secret')).not.toBeInTheDocument();
});
