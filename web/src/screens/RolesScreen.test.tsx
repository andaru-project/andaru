import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import type { Session } from '../types';
import { fetchQueue, jsonResponse } from '../test/fetch';
import { render, screen } from '../test/render';
import { RolesScreen } from './RolesScreen';

const session: Session = {
  account: { id: '0657cce4-d4f9-4d1e-9cae-53a99855bb10', login: 'admin', displayName: 'Administrator' },
  capabilities: ['users.read', 'configuration.read', 'configuration.write', 'configuration.apply'],
  csrfToken: 'csrf', expiresAt: '2026-11-16T12:00:00Z', idleTimeoutSeconds: 1800,
};
const saved = { state: 'saved', savedVersion: '8', items: [{ version: '8', value: { id: 'rol_one', name: 'Operator', description: '', capabilities: ['status.read'] } }] };
const live = { items: [{ id: 'rol_one', name: 'Operator', description: '', capabilities: ['status.read', 'configuration.read'] }] };
const capabilities = { items: ['status.read', 'configuration.read'] };
const applies = { pointers: { confirmed: '6', lastKnownGood: '5', pending: null, live: '6' }, items: [{ id: '6', savedVersion: '6', state: 'confirmed', previous: '5', reason: '', createdAt: '2026-11-16T10:00:00Z', confirmTimeoutSeconds: 120, files: [] }] };

it('keeps saved role edits visibly separate from effective live permissions', async () => {
  fetchQueue(jsonResponse(200, saved), jsonResponse(200, live), jsonResponse(200, capabilities), jsonResponse(200, applies));
  render(<RolesScreen api={new ApiClient('csrf')} session={session} onSessionLost={vi.fn()} />);

  expect(await screen.findByText('Saved roles are not live permissions')).toBeInTheDocument();
  expect(screen.getByText(/saving a role never activates it/i)).toBeInTheDocument();
  expect(screen.getAllByText('Saved draft').length).toBeGreaterThan(0);
  expect(screen.getAllByText('Effective live').length).toBeGreaterThan(0);
});

it('warns that applying roles also applies every saved configuration change', async () => {
  fetchQueue(jsonResponse(200, saved), jsonResponse(200, live), jsonResponse(200, capabilities), jsonResponse(200, applies));
  render(<RolesScreen api={new ApiClient('csrf')} session={session} onSessionLost={vi.fn()} />);

  await userEvent.click(await screen.findByRole('button', { name: 'Apply saved configuration' }));
  const dialog = await screen.findByRole('dialog');
  expect(dialog).toHaveTextContent('every saved configuration change');
  expect(dialog).toHaveTextContent('network connectivity');
});
