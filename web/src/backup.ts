import type { BackupStore, RestorePreviewResponse } from './types';

// Matches internal/web/proxy.go maxProxyBody (17 MiB) and internal/backup/crypto.go MaxPassphraseBytes (1024).
export const maxBackupUploadBytes = 17 * 1024 * 1024;
export const maxPassphraseBytes = 1024;

export const backupStoreOrder: readonly BackupStore[] = [
  'configuration',
  'configurationSecrets',
  'users',
  'userCredentials',
];

export const backupStoreLabel: Record<BackupStore, string> = {
  configuration: 'Configuration',
  configurationSecrets: 'Configuration secrets',
  users: 'Users',
  userCredentials: 'User credentials',
};

export const backupStoreDescription: Record<BackupStore, string> = {
  configuration: 'Interfaces, segments, zones, firewall rules, NAT, aliases, DHCP, DNS, and roles.',
  configurationSecrets: 'Stored secret values such as PPPoE passwords.',
  users: 'Local administrator and operator accounts and role assignments.',
  userCredentials: 'Password hashes and API token hashes.',
};

const utf8Encoder = new TextEncoder();

export function validateBackupPassphrase(passphrase: string): string | undefined {
  if (passphrase.length === 0) return 'Enter an encryption passphrase.';
  if (passphrase.includes('\0')) return 'Passphrase cannot contain null bytes.';
  if (utf8Encoder.encode(passphrase).length > maxPassphraseBytes) {
    return `Passphrase must be ${maxPassphraseBytes} bytes or fewer.`;
  }
  return undefined;
}

export function formatByteSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

export function backupFilename(
  hostname: string | undefined,
  savedVersion: string,
  capturedAt: string,
  encrypted: boolean,
): string {
  const hostSlug = (hostname ?? '')
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, '-')
    .replace(/^-+|-+$/g, '') || 'andaru';
  const parsed = capturedAt ? new Date(capturedAt) : null;
  const stamp = parsed && !Number.isNaN(parsed.getTime())
    ? parsed.toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z').replace('T', '-')
    : '';
  const parts = ['andaru-backup', hostSlug, ...(savedVersion ? [`v${savedVersion}`] : []), ...(stamp ? [stamp] : [])];
  return `${parts.join('-')}.${encrypted ? 'age' : 'zip'}`;
}

export function restoreBlockers(preview: RestorePreviewResponse): string[] {
  const blockers: string[] = [];
  if (preview.blockingSecrets.length > 0) {
    blockers.push(`Missing secrets required by bound references: ${preview.blockingSecrets.join(', ')}.`);
  }
  if (preview.missingRoleIds.length > 0) {
    blockers.push(`Restored users reference roles missing from the resulting configuration: ${preview.missingRoleIds.join(', ')}.`);
  }
  if (preview.requiresRootAdministrator) {
    blockers.push('The restored user store has no active administrator; recovery administrators can only be created from the local root console (andaru ctl restore).');
  }
  return blockers;
}
