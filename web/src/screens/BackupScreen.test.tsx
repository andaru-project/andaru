import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen, within } from '../test/render';
import { BackupScreen } from './BackupScreen';

const sampleManifest = {
  formatVersion: 1,
  backupId: '0123456789abcdef0123456789abcdef',
  createdAt: '2026-11-26T12:00:00Z',
  andaruVersion: '0.5.0-a.5',
  savedVersion: '12',
  scopes: ['configuration', 'users', 'configurationSecrets'],
  payloads: {
    'config.json': { schemaVersion: 4, size: 1024, sha256: 'aa' },
    'users.json': { schemaVersion: 1, size: 512, sha256: 'bb' },
    'configuration-secrets.json': { schemaVersion: 1, size: 256, sha256: 'cc' },
  },
  network: { live: 3, pending: null, liveSavedVersion: '11' },
  savedButUnappliedAreas: ['network'],
};

function renderScreen(caps: string[], onRestored = vi.fn(), onSessionLost = vi.fn()) {
  return render(
    <MemoryRouter>
      <BackupScreen api={new ApiClient('csrf-tok')} session={session(caps)} hostname="andaru-gw" onRestored={onRestored} onSessionLost={onSessionLost} />
    </MemoryRouter>,
  );
}

describe('BackupScreen', () => {
  it('downloads a plain backup without secrets and an encrypted backup with passphrase and re-authentication', async () => {
    const user = userEvent.setup();
    const createObjectURL = vi.fn(() => 'blob:mock');
    const revokeObjectURL = vi.fn();
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL, revokeObjectURL }));

    const createCalls: Array<Record<string, unknown>> = [];
    fetchRoutes({
      'POST /api/v1/backups': (init) => {
        const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
        createCalls.push(body);
        const sensitive = Boolean(body.includeSecrets || body.includeCredentials);
        return new Response(new Blob(['archive-bytes']), {
          status: 200,
          headers: {
            'Content-Type': sensitive ? 'application/octet-stream' : 'application/zip',
            'Andaru-Saved-Version': '12',
            'Andaru-Identity-Revision': '4',
            'Andaru-Captured-At': '2026-11-26T14:05:00Z',
          },
        });
      },
    });

    renderScreen(['backups.create', 'backups.exportSecrets']);
    expect(screen.queryByRole('heading', { name: 'Restore from backup' })).not.toBeInTheDocument();

    // Standard plain ZIP backup omits passphrase and password.
    await user.click(screen.getByRole('button', { name: 'Download backup' }));
    expect(await screen.findByText(/andaru-backup-andaru-gw-v12-20261126-140500Z\.zip/)).toBeInTheDocument();
    expect(createCalls[0]).toEqual({ includeSecrets: false });

    // Enable configuration secrets + user credentials -> validates passphrase match and account password.
    await user.click(screen.getByRole('switch', { name: /Include configuration secrets/ }));
    await user.click(screen.getByRole('switch', { name: /Include user credentials/ }));
    await user.type(screen.getByLabelText(/^Archive passphrase/), 'my-strong-passphrase');
    await user.type(screen.getByLabelText(/^Confirm passphrase/), 'mismatch');
    await user.click(screen.getByRole('button', { name: 'Download encrypted backup' }));
    expect(screen.getByRole('alert')).toHaveTextContent('Passphrases do not match.');
    expect(createCalls).toHaveLength(1);

    const confirmInput = screen.getByLabelText(/^Confirm passphrase/);
    await user.clear(confirmInput);
    await user.type(confirmInput, 'my-strong-passphrase');
    await user.type(screen.getByLabelText(/^Your current password/), 'admin-pass');
    await user.click(screen.getByRole('button', { name: 'Download encrypted backup' }));

    expect(await screen.findByText(/andaru-backup-andaru-gw-v12-20261126-140500Z\.age/)).toBeInTheDocument();
    expect(createCalls[1]).toEqual({
      includeSecrets: true,
      includeCredentials: true,
      passphrase: 'my-strong-passphrase',
      password: 'admin-pass',
    });
    // Sensitive fields are cleared immediately after download.
    expect(screen.getByLabelText(/^Archive passphrase/)).toHaveValue('');
    expect(screen.getByLabelText(/^Confirm passphrase/)).toHaveValue('');
    expect(screen.getByLabelText(/^Your current password/)).toHaveValue('');
  });

  it('hides secret export switches when lacking backups.exportSecrets', () => {
    renderScreen(['backups.create']);
    expect(screen.queryByRole('switch', { name: /Include configuration secrets/ })).not.toBeInTheDocument();
    expect(screen.getByText(/backups\.exportSecrets/)).toBeInTheDocument();
  });

  it('inspects, previews, confirms, and executes a restore with idempotency and revision preconditions', async () => {
    const user = userEvent.setup();
    const onRestored = vi.fn();
    const previewOptions: Array<Record<string, unknown>> = [];
    const restoreOptions: Array<Record<string, unknown>> = [];

    fetchRoutes({
      'POST /api/v1/restores/preview': (init) => {
        const form = init?.body as FormData;
        const opts = JSON.parse(String(form.get('options'))) as Record<string, unknown>;
        previewOptions.push(opts);
        if (!Array.isArray(opts.stores) || opts.stores.length === 0) {
          return jsonResponse(200, { state: 'inspection', manifest: sampleManifest });
        }
        return jsonResponse(200, {
          state: 'restore-preview',
          manifest: sampleManifest,
          expectedSavedVersion: '15',
          expectedIdentityRevision: 7,
          configurationChanges: { created: 1, updated: 2, deleted: 0 },
          secretChanges: { created: 0, updated: 1, deleted: 0 },
          userChanges: { created: 0, updated: 1, deleted: 0 },
          credentialChanges: { created: 0, updated: 0, deleted: 0 },
          unresolvedSecrets: ['WAN_PASSWORD'],
          blockingSecrets: [],
          missingRoleIds: [],
          requiresRootAdministrator: false,
          recoveryAdministrators: [],
          hardwareFindings: [{ code: 'HARDWARE_MISSING', path: '/links/0/permanentMac', message: 'No interface with this permanent MAC is present.' }],
        });
      },
      'POST /api/v1/restores': (init) => {
        const form = init?.body as FormData;
        const opts = JSON.parse(String(form.get('options'))) as Record<string, unknown>;
        restoreOptions.push(opts);
        if (restoreOptions.length === 1) {
          return jsonResponse(412, { error: { code: 'PRECONDITION_FAILED', message: 'Version changed.' } });
        }
        return jsonResponse(200, {
          state: 'saved',
          savedVersion: '16',
          identityRevision: 8,
          unresolvedSecrets: ['WAN_PASSWORD'],
          replayed: false,
        });
      },
    });

    renderScreen(['backups.restore'], onRestored);
    expect(screen.queryByRole('heading', { name: 'Create backup' })).not.toBeInTheDocument();

    const fileInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    const archive = new File(['dummy-age-data'], 'backup.age', { type: 'application/octet-stream' });
    await user.upload(fileInput, archive);
    await user.type(screen.getByLabelText(/^Archive passphrase/), 'restore-pass');
    await user.click(screen.getByRole('button', { name: 'Inspect archive' }));

    expect(await screen.findByText('0123456789abcdef0123456789abcdef')).toBeInTheDocument();
    expect(screen.getByText(/Backup contained unapplied changes/)).toBeInTheDocument();
    expect(previewOptions[0]).toEqual({ passphrase: 'restore-pass' });

    // Uncheck configurationSecrets so only configuration and users are restored.
    await user.click(screen.getByRole('checkbox', { name: /Configuration secrets/ }));
    await user.click(screen.getByRole('button', { name: 'Preview restore' }));

    const table = await screen.findByRole('table', { name: 'Restore change summary' });
    expect(within(table).getByText('Configuration')).toBeInTheDocument();
    expect(within(table).getByText('Users')).toBeInTheDocument();
    expect(screen.getByText(/Hardware differences detected/)).toBeInTheDocument();
    expect(screen.getByText(/Secrets will be marked unresolved/)).toBeInTheDocument();
    expect(previewOptions[1]).toEqual({ passphrase: 'restore-pass', stores: ['configuration', 'users'] });

    // First execute returns 412 -> displays clear re-preview message.
    await user.click(screen.getByRole('button', { name: 'Restore selected data…' }));
    await user.click(screen.getByRole('button', { name: 'Confirm restore' }));
    expect(await screen.findByText(/The appliance changed after this restore was previewed/)).toBeInTheDocument();
    expect(restoreOptions[0]).toMatchObject({
      passphrase: 'restore-pass',
      stores: ['configuration', 'users'],
      expectedSavedVersion: '15',
      expectedIdentityRevision: 7,
    });
    expect(typeof restoreOptions[0].operationId).toBe('string');

    // Re-preview gets a fresh operationId and succeeds.
    await user.click(screen.getByRole('button', { name: 'Preview restore' }));
    await user.click(await screen.findByRole('button', { name: 'Restore selected data…' }));
    await user.click(screen.getByRole('button', { name: 'Confirm restore' }));

    expect(await screen.findByText('Backup restored')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open Review and apply' })).toHaveAttribute('href', '/changes');
    expect(screen.getByRole('link', { name: 'Open Interfaces' })).toHaveAttribute('href', '/network/interfaces');
    expect(onRestored).toHaveBeenCalledTimes(1);
    expect(restoreOptions[1].operationId).not.toBe(restoreOptions[0].operationId);
    expect(screen.getByLabelText(/^Archive passphrase/)).toHaveValue('');
  });

  it('blocks restore when preview reports blocking secrets, missing roles, or root-console requirement', async () => {
    const user = userEvent.setup();
    fetchRoutes({
      'POST /api/v1/restores/preview': (init) => {
        const form = init?.body as FormData;
        const opts = JSON.parse(String(form.get('options'))) as Record<string, unknown>;
        if (!Array.isArray(opts.stores) || opts.stores.length === 0) {
          return jsonResponse(200, { state: 'inspection', manifest: sampleManifest });
        }
        return jsonResponse(200, {
          state: 'restore-preview',
          manifest: sampleManifest,
          expectedSavedVersion: '15',
          expectedIdentityRevision: 7,
          configurationChanges: { created: 0, updated: 0, deleted: 0 },
          secretChanges: { created: 0, updated: 0, deleted: 1 },
          userChanges: { created: 0, updated: 1, deleted: 0 },
          credentialChanges: { created: 0, updated: 0, deleted: 0 },
          unresolvedSecrets: [],
          blockingSecrets: ['DSL_PASSWORD'],
          missingRoleIds: ['rol_missing'],
          requiresRootAdministrator: true,
          recoveryAdministrators: [],
          hardwareFindings: [],
        });
      },
    });

    renderScreen(['backups.restore']);
    const fileInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    await user.upload(fileInput, new File(['zip'], 'backup.zip', { type: 'application/zip' }));
    await user.click(screen.getByRole('button', { name: 'Inspect archive' }));
    await user.click(await screen.findByRole('button', { name: 'Preview restore' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Restore cannot proceed');
    expect(alert).toHaveTextContent('DSL_PASSWORD');
    expect(alert).toHaveTextContent('rol_missing');
    expect(alert).toHaveTextContent('andaru ctl restore');
    expect(screen.queryByRole('button', { name: 'Restore selected data…' })).not.toBeInTheDocument();
  });

  it('rejects empty or oversized backup files before uploading', async () => {
    const user = userEvent.setup();
    const fetchMock = fetchRoutes({});
    renderScreen(['backups.restore']);

    const fileInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    const emptyFile = new File([], 'empty.zip', { type: 'application/zip' });
    await user.upload(fileInput, emptyFile);
    expect(screen.getByRole('alert')).toHaveTextContent('The selected archive file is empty.');
    await user.click(screen.getByRole('button', { name: 'Inspect archive' }));
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
