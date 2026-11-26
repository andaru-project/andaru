import type { BackupStore, RestorePreviewResponse } from './types';

// Leaves 80 KiB (backupOptionsMax + multipart framing) below internal/web/proxy.go's 17 MiB maxProxyBody.
export const maxBackupUploadBytes = 17 * 1024 * 1024 - 80 * 1024;
// Floored display label so the stated limit never exceeds the enforced byte cap.
export const maxBackupUploadLabel = `${(Math.floor((maxBackupUploadBytes / (1024 * 1024)) * 10) / 10).toFixed(1)} MiB`;
export const maxPassphraseBytes = 1024;

export interface BackupStoreInfo {
  label: string;
  description: string;
  changes: 'configurationChanges' | 'secretChanges' | 'userChanges' | 'credentialChanges';
}

export const backupStores: Record<BackupStore, BackupStoreInfo> = {
  configuration: {
    label: 'Configuration',
    description: 'Interfaces, segments, zones, firewall rules, NAT, aliases, DHCP, DNS, and roles.',
    changes: 'configurationChanges',
  },
  configurationSecrets: {
    label: 'Configuration secrets',
    description: 'Stored secret values such as PPPoE passwords.',
    changes: 'secretChanges',
  },
  users: {
    label: 'Users',
    description: 'Local administrator and operator accounts and role assignments.',
    changes: 'userChanges',
  },
  userCredentials: {
    label: 'User credentials',
    description: 'Password hashes and API token hashes.',
    changes: 'credentialChanges',
  },
};

export const backupStoreOrder = Object.keys(backupStores) as BackupStore[];

const utf8Encoder = new TextEncoder();

export function validateBackupPassphrase(passphrase: string): string | undefined {
  if (passphrase.length === 0) return 'Enter an encryption passphrase.';
  if (passphrase.includes('\0')) return 'Passphrase cannot contain null bytes.';
  if (utf8Encoder.encode(passphrase).length > maxPassphraseBytes) {
    return `Passphrase must be ${maxPassphraseBytes} bytes or fewer.`;
  }
  return undefined;
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
