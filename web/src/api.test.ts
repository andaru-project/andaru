import { describe, expect, it } from 'vitest';
import { ApiClient } from './api';
import { fetchQueue, jsonResponse } from './test/fetch';

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
