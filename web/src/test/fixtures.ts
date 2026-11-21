import type { Apply, Session } from '../types';

export const session = (capabilities: string[]): Session => ({
  account: { id: '0657cce4-d4f9-4d1e-9cae-53a99855bb10', login: 'admin', displayName: 'Administrator' },
  capabilities, csrfToken: 'csrf', expiresAt: '2026-11-16T12:00:00Z', idleTimeoutSeconds: 1800,
});

export const apply = (fields: Partial<Apply>): Apply => ({
  id: '1', savedVersion: '3', state: 'confirmed', previous: null, reason: '', createdAt: '2026-11-21T10:00:00Z', confirmTimeoutSeconds: 120, files: [], ...fields,
});

export const pointers = { confirmed: null, lastKnownGood: null, pending: null, live: null };
