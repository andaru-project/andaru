import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchQueue, jsonResponse } from '../test/fetch';
import { render, screen } from '../test/render';
import { AuditScreen } from './AuditScreen';

it('loads bounded audit pages newest first, then older ones by the server cursor', async () => {
  fetchQueue(
    jsonResponse(200, { items: [{ id: 2, at: '2026-11-16T11:00:00Z', actor: { kind: 'account', id: 'admin' }, source: '192.0.2.1', action: 'login.success', target: 'admin', result: 'success', category: 'general', count: 1, metadata: {} }], nextCursor: '2' }),
    jsonResponse(200, { items: [{ id: 1, at: '2026-11-16T10:00:00Z', actor: { kind: 'account', id: 'admin' }, source: '192.0.2.1', action: 'token.revoked', target: 'token', result: 'success', category: 'protected', count: 1, metadata: {} }] }),
  );
  const fetchMock = globalThis.fetch;
  render(<AuditScreen api={new ApiClient()} onSessionLost={vi.fn()} />);

  await screen.findByText('login.success');
  expect(fetchMock).toHaveBeenLastCalledWith(expect.stringContaining('order=desc'), expect.anything());
  await userEvent.click(await screen.findByRole('button', { name: 'Load older events' }));
  expect(await screen.findByText('token.revoked')).toBeInTheDocument();
  expect(fetchMock).toHaveBeenLastCalledWith(expect.stringContaining('cursor=2'), expect.anything());
});

it('filters loaded audit events by action, actor or result', async () => {
  fetchQueue(jsonResponse(200, { items: [
    { id: 2, at: '2026-11-16T11:00:00Z', actor: { kind: 'account', id: 'admin' }, source: '192.0.2.1', action: 'login.failed', target: 'admin', result: 'denied', category: 'general', count: 1, metadata: {} },
    { id: 1, at: '2026-11-16T10:00:00Z', actor: { kind: 'account', id: 'operator' }, source: '192.0.2.2', action: 'rule.created', target: 'rul_1', result: 'success', category: 'protected', count: 1, metadata: {} },
  ] }));
  render(<AuditScreen api={new ApiClient()} onSessionLost={vi.fn()} />);

  await screen.findByText('login.failed');
  await userEvent.type(screen.getByLabelText('Filter events'), 'denied');
  expect(screen.getByText('login.failed')).toBeInTheDocument();
  expect(screen.queryByText('rule.created')).not.toBeInTheDocument();
});

it('exports audit JSON via /api/v1/audit/export and handles export errors and 401', async () => {
  const user = userEvent.setup();
  const onSessionLost = vi.fn();
  const createObjectURL = vi.fn(() => 'blob:audit-export');
  const revokeObjectURL = vi.fn();
  vi.stubGlobal('URL', Object.assign(URL, { createObjectURL, revokeObjectURL }));
  const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});

  fetchQueue(
    jsonResponse(200, { items: [{ id: 2, at: '2026-11-16T11:00:00Z', actor: { kind: 'account', id: 'admin' }, source: '192.0.2.1', action: 'login.success', target: 'admin', result: 'success', category: 'general', count: 1, metadata: {} }] }),
    new Response(JSON.stringify({ items: [] }), {
      status: 200,
      headers: {
        'Content-Type': 'application/json',
        'Content-Disposition': 'attachment; filename="audit-2026.json"',
      },
    }),
    jsonResponse(503, { error: { code: 'UNAVAILABLE', message: 'Audit store offline.' } }),
    jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Session expired.' } }),
  );
  const fetchMock = globalThis.fetch;

  render(<AuditScreen api={new ApiClient()} onSessionLost={onSessionLost} />);
  await screen.findByText('login.success');

  await user.click(screen.getByRole('button', { name: 'Export JSON' }));
  expect(fetchMock).toHaveBeenLastCalledWith('/api/v1/audit/export?order=desc&limit=100', expect.anything());
  expect(createObjectURL).toHaveBeenCalledTimes(1);
  expect(clickSpy).toHaveBeenCalledTimes(1);

  await user.click(screen.getByRole('button', { name: 'Export JSON' }));
  expect(await screen.findByText('Audit store offline.')).toBeInTheDocument();

  await user.click(screen.getByRole('button', { name: 'Export JSON' }));
  expect(onSessionLost).toHaveBeenCalledTimes(1);
  clickSpy.mockRestore();
});
