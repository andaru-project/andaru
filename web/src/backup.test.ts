import { expect, it } from 'vitest';
import { backupFilename, restoreBlockers, validateBackupPassphrase } from './backup';
import type { RestorePreviewResponse } from './types';

it('validates backup passphrases against empty, null byte, and 1024-byte UTF-8 limits', () => {
  expect(validateBackupPassphrase('')).toBe('Enter an encryption passphrase.');
  expect(validateBackupPassphrase('bad\0pass')).toBe('Passphrase cannot contain null bytes.');
  expect(validateBackupPassphrase('a'.repeat(1024))).toBeUndefined();
  expect(validateBackupPassphrase('a'.repeat(1025))).toBe('Passphrase must be 1024 bytes or fewer.');
  expect(validateBackupPassphrase('é'.repeat(513))).toBe('Passphrase must be 1024 bytes or fewer.');
  expect(validateBackupPassphrase('correct horse battery staple')).toBeUndefined();
});

it('builds sanitized backup filenames with .zip or .age extensions', () => {
  expect(backupFilename('Edge GW 01!', '14', '2026-11-26T14:05:09.123456Z', false)).toBe('andaru-backup-edge-gw-01-v14-20261126-140509Z.zip');
  expect(backupFilename(undefined, '0', '2026-11-26T14:05:09Z', true)).toBe('andaru-backup-andaru-v0-20261126-140509Z.age');
  expect(backupFilename('', '', 'invalid', false)).toBe('andaru-backup-andaru.zip');
});

it('collects hard restore blockers from a restore preview', () => {
  const base: RestorePreviewResponse = {
    state: 'restore-preview',
    manifest: {
      formatVersion: 1,
      backupId: '0123456789abcdef0123456789abcdef',
      createdAt: '2026-11-26T12:00:00Z',
      andaruVersion: '0.5.0',
      savedVersion: '5',
      scopes: ['configuration', 'users'],
      payloads: {},
      network: { live: 1, pending: null, liveSavedVersion: '5' },
      savedButUnappliedAreas: [],
    },
    expectedSavedVersion: '7',
    expectedIdentityRevision: 3,
    configurationChanges: { created: 0, updated: 1, deleted: 0 },
    secretChanges: { created: 0, updated: 0, deleted: 0 },
    userChanges: { created: 0, updated: 1, deleted: 0 },
    credentialChanges: { created: 0, updated: 0, deleted: 0 },
    unresolvedSecrets: ['WAN_PASSWORD'],
    blockingSecrets: [],
    missingRoleIds: [],
    requiresRootAdministrator: false,
    recoveryAdministrators: [],
    hardwareFindings: [],
  };
  expect(restoreBlockers(base)).toEqual([]);

  const blocked = restoreBlockers({
    ...base,
    blockingSecrets: ['WAN_PASSWORD'],
    missingRoleIds: ['rol_custom'],
    requiresRootAdministrator: true,
  });
  expect(blocked).toHaveLength(3);
  expect(blocked[0]).toContain('WAN_PASSWORD');
  expect(blocked[1]).toContain('rol_custom');
  expect(blocked[2]).toContain('andaru ctl restore');
});
