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

it('keeps saved role edits visibly separate from effective live permissions', async () => {
  fetchQueue(jsonResponse(200, saved), jsonResponse(200, live), jsonResponse(200, capabilities));
  render(<RolesScreen api={new ApiClient('csrf')} session={session} onSessionLost={vi.fn()} />);

  expect(await screen.findByText('Saved roles are not live permissions')).toBeInTheDocument();
  expect(screen.getByText(/saving a role never activates it/i)).toBeInTheDocument();
  expect(screen.getAllByText('Saved draft')).toHaveLength(1);
  expect(screen.getAllByText('Effective live')).toHaveLength(1);
  expect(screen.getByText('status.read').closest('td')).toHaveAttribute('data-label', 'Saved draft');
  expect(screen.getByText('status.read, configuration.read').closest('td')).toHaveAttribute('data-label', 'Effective live');
});

it('leaves applying to the Review and apply page', async () => {
  fetchQueue(jsonResponse(200, saved), jsonResponse(200, live), jsonResponse(200, capabilities));
  render(<RolesScreen api={new ApiClient('csrf')} session={session} onSessionLost={vi.fn()} />);

  expect(await screen.findByText('Operator')).toBeInTheDocument();
  expect(screen.getByText(/review and apply activates the entire saved configuration/i)).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /apply/i })).not.toBeInTheDocument();
  expect(globalThis.fetch).toHaveBeenCalledTimes(3);
});
