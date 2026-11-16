import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchQueue, jsonResponse } from '../test/fetch';
import { render, screen } from '../test/render';
import { AuditScreen } from './AuditScreen';

it('loads bounded audit pages using the server cursor', async () => {
  fetchQueue(
    jsonResponse(200, { items: [{ id: 1, at: '2026-11-16T10:00:00Z', actor: { kind: 'account', id: 'admin' }, source: '192.0.2.1', action: 'login.success', target: 'admin', result: 'success', category: 'general', count: 1, metadata: {} }], nextCursor: '1' }),
    jsonResponse(200, { items: [{ id: 2, at: '2026-11-16T11:00:00Z', actor: { kind: 'account', id: 'admin' }, source: '192.0.2.1', action: 'token.revoked', target: 'token', result: 'success', category: 'protected', count: 1, metadata: {} }] }),
  );
  const fetchMock = globalThis.fetch;
  render(<AuditScreen api={new ApiClient()} onSessionLost={vi.fn()} />);

  await userEvent.click(await screen.findByRole('button', { name: 'Load more events' }));
  expect(await screen.findByText('token.revoked')).toBeInTheDocument();
  expect(fetchMock).toHaveBeenLastCalledWith(expect.stringContaining('cursor=1'), expect.anything());
});
