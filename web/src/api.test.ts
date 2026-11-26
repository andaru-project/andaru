import { describe, expect, it, vi } from 'vitest';
import { ApiClient } from './api';
import { fetchQueue, fetchRoutes, jsonResponse } from './test/fetch';

const session = {
  account: { id: '0657cce4-d4f9-4d1e-9cae-53a99855bb10', login: 'admin', displayName: 'Administrator' },
  capabilities: ['users.read', 'users.manage'],
  csrfToken: 'csrf-secret',
  expiresAt: '2026-11-16T12:00:00Z',
  idleTimeoutSeconds: 1800,
};

describe('ApiClient', () => {
  it('uses same-origin cookies and the rotated CSRF token only for state changes', async () => {
    const fetchMock = fetchQueue(
      jsonResponse(200, session),
      jsonResponse(200, { operationId: 'op', result: 'success', target: 'id', metadata: {} }),
    );
    const api = new ApiClient();

    await api.currentSession();
    await api.updateUser('id', { displayName: 'New', expectedRevision: 3 });

    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ credentials: 'same-origin' });
    expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).has('X-Andaru-CSRF')).toBe(false);
    expect(new Headers(fetchMock.mock.calls[1]?.[1]?.headers).get('X-Andaru-CSRF')).toBe('csrf-secret');
    expect(fetchMock.mock.calls[1]?.[1]).toMatchObject({ credentials: 'same-origin', method: 'PATCH' });
  });

  it('does not expose response internals and classifies a lost session', async () => {
    fetchQueue(jsonResponse(401, { error: { code: 'SESSION_EXPIRED', message: 'Sign in again.', details: [] } }));
    const api = new ApiClient();

    await expect(api.currentSession()).rejects.toEqual(
      expect.objectContaining({ status: 401, code: 'SESSION_EXPIRED', message: 'Sign in again.' }),
    );
  });

  it('classifies a disconnected backend without fabricating a server response', async () => {
    fetchQueue(new TypeError('network failed'));
    await expect(new ApiClient().currentSession()).rejects.toEqual(
      expect.objectContaining({ status: 0, code: 'DISCONNECTED' }),
    );
  });
});

it('gives a read a time limit, so a box that never answers cannot hold a page, and a write none', async () => {
  const fetchMock = fetchRoutes({ '/api/v1/status/links': { items: [] }, 'POST /api/v1/applies/1/rollback': {} });
  const api = new ApiClient('csrf');
  await api.links();
  await api.finishApply('1', 'rollback');
  expect(fetchMock.mock.calls[0][1]?.signal).toBeInstanceOf(AbortSignal);
  expect(fetchMock.mock.calls[1][1]?.signal).toBeUndefined();
});

it('reports a body that stops arriving at the time limit as a lost connection, not as an empty answer', async () => {
  vi.spyOn(AbortSignal, 'timeout').mockReturnValue(AbortSignal.abort());
  const connection = vi.fn();
  // The headers came; reading the body fails.
  fetchQueue(new Response('{"items":', { status: 200 }));
  await expect(new ApiClient('csrf', connection).links()).rejects.toMatchObject({ status: 0, code: 'DISCONNECTED' });
  expect(connection).toHaveBeenLastCalledWith(false);
  vi.restoreAllMocks();
});

it('marks a read with no input since the previous request as a poll, and never a write', async () => {
  const fetchMock = fetchRoutes({ '/api/v1/status/links': { items: [] }, 'POST /api/v1/applies/1/rollback': {} });
  const api = new ApiClient('csrf');
  const background = (call: number) => new Headers(fetchMock.mock.calls[call]?.[1]?.headers).get('X-Andaru-Background');
  document.dispatchEvent(new KeyboardEvent('keydown'));
  await api.links();
  expect(background(0)).toBeNull();
  await api.links();
  expect(background(1)).toBe('1');
  await api.finishApply('1', 'rollback');
  expect(background(2)).toBeNull();
  document.dispatchEvent(new KeyboardEvent('keydown'));
  await api.finishApply('1', 'rollback');
  await api.links();
  expect(background(4)).toBe('1'); // the write carried that input
  for (const event of [new MouseEvent('pointerdown'), new WheelEvent('wheel')]) {
    document.dispatchEvent(event);
    await api.links();
    expect(background(fetchMock.mock.calls.length - 1)).toBeNull();
    await api.links();
    expect(background(fetchMock.mock.calls.length - 1)).toBe('1');
  }
});

it('keeps input for the next read when a request fails to connect', async () => {
  const fetchMock = fetchQueue(new TypeError('network failed'), jsonResponse(200, { items: [] }));
  const api = new ApiClient();
  document.dispatchEvent(new KeyboardEvent('keydown'));
  await expect(api.links()).rejects.toEqual(expect.objectContaining({ code: 'DISCONNECTED' }));
  await api.links();
  expect(new Headers(fetchMock.mock.calls[1]?.[1]?.headers).get('X-Andaru-Background')).toBeNull();
});

it('omits Content-Type for FormData restore uploads and sends X-Andaru-CSRF', async () => {
  const fetchMock = fetchQueue(jsonResponse(200, { state: 'inspection', manifest: { backupId: 'abc', scopes: [] } }));
  const api = new ApiClient('csrf-tok');
  await api.inspectBackup(new Blob(['zip']), 'pass');

  const init = fetchMock.mock.calls[0]?.[1];
  const headers = new Headers(init?.headers);
  expect(headers.get('X-Andaru-CSRF')).toBe('csrf-tok');
  expect(headers.has('Content-Type')).toBe(false);
  expect(init?.body).toBeInstanceOf(FormData);
});

it('handles createBackup binary streaming, 401 session clear, and mid-stream blob disconnect', async () => {
  const connection = vi.fn();
  const brokenResponse = new Response('partial', { status: 200, headers: { 'Content-Type': 'application/zip' } });
  vi.spyOn(brokenResponse, 'blob').mockRejectedValue(new TypeError('stream aborted'));

  const fetchMock = fetchQueue(
    brokenResponse,
    jsonResponse(401, { error: { code: 'SESSION_EXPIRED', message: 'Sign in again.' } }),
    jsonResponse(200, { items: [] }),
  );
  const api = new ApiClient('csrf-tok', connection);

  await expect(api.createBackup({ includeSecrets: false })).rejects.toMatchObject({ status: 0, code: 'DISCONNECTED' });
  expect(connection).toHaveBeenLastCalledWith(false);
  expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get('X-Andaru-CSRF')).toBe('csrf-tok');

  await expect(api.createBackup({ includeSecrets: false })).rejects.toMatchObject({ status: 401, code: 'SESSION_EXPIRED' });
  // 401 cleared the CSRF token.
  await api.finishApply('1', 'rollback').catch(() => {});
  expect(new Headers(fetchMock.mock.calls[2]?.[1]?.headers).has('X-Andaru-CSRF')).toBe(false);
  vi.restoreAllMocks();
});
