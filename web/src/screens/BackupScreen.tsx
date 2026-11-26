import {
  Alert,
  Anchor,
  Badge,
  Button,
  Checkbox,
  Divider,
  FileInput,
  Group,
  List,
  Paper,
  PasswordInput,
  Stack,
  Switch,
  Table,
  Text,
  Title,
} from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { Link } from 'react-router';
import { ApiClient, ApiError, apiErrorMessage, reportError } from '../api';
import {
  backupFilename,
  backupStoreDescription,
  backupStoreLabel,
  backupStoreOrder,
  formatByteSize,
  maxBackupUploadBytes,
  restoreBlockers,
  validateBackupPassphrase,
} from '../backup';
import { formatTimestamp, useTimeZone } from '../timezone';
import type {
  BackupManifest,
  BackupStore,
  RestoreCounts,
  RestorePreviewResponse,
  RestoreSavedResponse,
  Session,
} from '../types';

interface BackupScreenProps {
  api: ApiClient;
  session: Session;
  hostname?: string;
  onRestored(): void;
  onSessionLost(): void;
}

const mono = { input: 'mono-input' };

function triggerDownload(blob: Blob, filename: string) {
  if (typeof URL.createObjectURL !== 'function') return;
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.style.display = 'none';
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1_000);
}

function storeChangesOf(preview: RestorePreviewResponse, store: BackupStore): RestoreCounts {
  switch (store) {
    case 'configuration':
      return preview.configurationChanges;
    case 'configurationSecrets':
      return preview.secretChanges;
    case 'users':
      return preview.userChanges;
    case 'userCredentials':
      return preview.credentialChanges;
  }
}

export function BackupScreen({ api, session, hostname, onRestored, onSessionLost }: BackupScreenProps) {
  const timeZone = useTimeZone();
  const canCreate = session.capabilities.includes('backups.create');
  const canExportSecrets = session.capabilities.includes('backups.exportSecrets');
  const canRestore = session.capabilities.includes('backups.restore');

  // Create backup state.
  const [includeSecrets, setIncludeSecrets] = useState(false);
  const [includeCredentials, setIncludeCredentials] = useState(false);
  const [exportPassphrase, setExportPassphrase] = useState('');
  const [exportPassphraseConfirm, setExportPassphraseConfirm] = useState('');
  const [accountPassword, setAccountPassword] = useState('');
  const [createBusy, setCreateBusy] = useState(false);
  const [createError, setCreateError] = useState('');
  const [createSuccess, setCreateSuccess] = useState<{
    filename: string;
    encrypted: boolean;
    size: number;
    savedVersion: string;
    identityRevision: string;
    capturedAt: string;
  } | null>(null);

  // Restore backup state.
  const [archiveFile, setArchiveFile] = useState<File | null>(null);
  const [restorePassphrase, setRestorePassphrase] = useState('');
  const [inspectBusy, setInspectBusy] = useState(false);
  const [inspectError, setInspectError] = useState('');
  const [manifest, setManifest] = useState<BackupManifest | null>(null);
  const [selectedStores, setSelectedStores] = useState<BackupStore[]>([]);
  const [previewBusy, setPreviewBusy] = useState(false);
  const [previewError, setPreviewError] = useState('');
  const [preview, setPreview] = useState<RestorePreviewResponse | null>(null);
  const [operationId, setOperationId] = useState('');
  const [confirming, setConfirming] = useState(false);
  const [restoreBusy, setRestoreBusy] = useState(false);
  const [restoreError, setRestoreError] = useState('');
  const [restoreResult, setRestoreResult] = useState<{
    response: RestoreSavedResponse;
    restoredStores: BackupStore[];
  } | null>(null);

  const sensitiveExport = canExportSecrets && (includeSecrets || includeCredentials);
  const exportPassphraseErr = sensitiveExport && exportPassphrase ? validateBackupPassphrase(exportPassphrase) : undefined;
  const exportConfirmErr = sensitiveExport && exportPassphraseConfirm && exportPassphraseConfirm !== exportPassphrase
    ? 'Passphrases do not match.'
    : undefined;

  function toggleSensitive(kind: 'secrets' | 'credentials', checked: boolean) {
    const nextSecrets = kind === 'secrets' ? checked : includeSecrets;
    const nextCredentials = kind === 'credentials' ? checked : includeCredentials;
    setIncludeSecrets(nextSecrets);
    setIncludeCredentials(nextCredentials);
    setCreateError('');
    if (!nextSecrets && !nextCredentials) {
      setExportPassphrase('');
      setExportPassphraseConfirm('');
      setAccountPassword('');
    }
  }

  async function handleCreateBackup(event: FormEvent) {
    event.preventDefault();
    setCreateError('');
    setCreateSuccess(null);

    if (sensitiveExport) {
      const passErr = validateBackupPassphrase(exportPassphrase);
      if (passErr) {
        setCreateError(passErr);
        return;
      }
      if (exportPassphrase !== exportPassphraseConfirm) {
        setCreateError('Passphrases do not match.');
        return;
      }
      if (!accountPassword) {
        setCreateError('Enter your current account password to export secrets or credentials.');
        return;
      }
    }

    setCreateBusy(true);
    try {
      const download = await api.createBackup(
        sensitiveExport
          ? {
              includeSecrets,
              includeCredentials,
              passphrase: exportPassphrase,
              password: accountPassword,
            }
          : { includeSecrets: false },
      );
      const filename = backupFilename(hostname, download.savedVersion, download.capturedAt, download.encrypted);
      triggerDownload(download.blob, filename);
      setExportPassphrase('');
      setExportPassphraseConfirm('');
      setAccountPassword('');
      setCreateSuccess({
        filename,
        encrypted: download.encrypted,
        size: download.blob.size,
        savedVersion: download.savedVersion,
        identityRevision: download.identityRevision,
        capturedAt: download.capturedAt,
      });
    } catch (error) {
      reportError(error, onSessionLost, setCreateError);
    } finally {
      setCreateBusy(false);
    }
  }

  function resetRestoreFlow(clearResult = true) {
    setInspectError('');
    setManifest(null);
    setSelectedStores([]);
    setPreviewError('');
    setPreview(null);
    setOperationId('');
    setConfirming(false);
    setRestoreError('');
    if (clearResult) setRestoreResult(null);
  }

  function handleFileChange(file: File | null) {
    setArchiveFile(file);
    resetRestoreFlow(true);
    if (!file) {
      setRestorePassphrase('');
      return;
    }
    if (file.size === 0) {
      setInspectError('The selected archive file is empty.');
    } else if (file.size > maxBackupUploadBytes) {
      setInspectError(`Archive exceeds the ${formatByteSize(maxBackupUploadBytes)} upload limit.`);
    }
  }

  function handleRestorePassphraseChange(value: string) {
    setRestorePassphrase(value);
    resetRestoreFlow(false);
  }

  async function handleInspect(event: FormEvent) {
    event.preventDefault();
    if (!archiveFile) {
      setInspectError('Choose a backup archive file first.');
      return;
    }
    if (archiveFile.size === 0) {
      setInspectError('The selected archive file is empty.');
      return;
    }
    if (archiveFile.size > maxBackupUploadBytes) {
      setInspectError(`Archive exceeds the ${formatByteSize(maxBackupUploadBytes)} upload limit.`);
      return;
    }
    if (restorePassphrase) {
      const passErr = validateBackupPassphrase(restorePassphrase);
      if (passErr) {
        setInspectError(passErr);
        return;
      }
    }

    setInspectBusy(true);
    setInspectError('');
    setPreviewError('');
    setPreview(null);
    setOperationId('');
    setConfirming(false);
    setRestoreError('');
    setRestoreResult(null);
    try {
      const res = await api.inspectBackup(archiveFile, restorePassphrase || undefined);
      setManifest(res.manifest);
      const available = backupStoreOrder.filter((store) => res.manifest.scopes.includes(store));
      setSelectedStores(available);
    } catch (error) {
      setManifest(null);
      setSelectedStores([]);
      reportError(error, onSessionLost, setInspectError);
    } finally {
      setInspectBusy(false);
    }
  }

  function handleStoreToggle(store: BackupStore, checked: boolean) {
    setSelectedStores((current) => {
      const next = checked ? [...current, store] : current.filter((item) => item !== store);
      return backupStoreOrder.filter((item) => next.includes(item));
    });
    setPreview(null);
    setPreviewError('');
    setOperationId('');
    setConfirming(false);
    setRestoreError('');
  }

  async function handlePreviewRestore() {
    if (!archiveFile || selectedStores.length === 0) return;
    setPreviewBusy(true);
    setPreviewError('');
    setRestoreError('');
    setConfirming(false);
    try {
      const res = await api.previewRestore(archiveFile, {
        ...(restorePassphrase ? { passphrase: restorePassphrase } : {}),
        stores: selectedStores,
      });
      setPreview(res);
      setOperationId(crypto.randomUUID());
    } catch (error) {
      setPreview(null);
      setOperationId('');
      reportError(error, onSessionLost, setPreviewError);
    } finally {
      setPreviewBusy(false);
    }
  }

  async function handleExecuteRestore() {
    if (!archiveFile || !preview || selectedStores.length === 0 || !operationId) return;
    const includesConfig = selectedStores.includes('configuration') || selectedStores.includes('configurationSecrets');
    const includesIdentity = selectedStores.includes('users') || selectedStores.includes('userCredentials');
    setRestoreBusy(true);
    setRestoreError('');
    try {
      const saved = await api.executeRestore(archiveFile, {
        ...(restorePassphrase ? { passphrase: restorePassphrase } : {}),
        stores: selectedStores,
        operationId,
        ...(includesConfig && preview.expectedSavedVersion ? { expectedSavedVersion: preview.expectedSavedVersion } : {}),
        ...(includesIdentity ? { expectedIdentityRevision: preview.expectedIdentityRevision } : {}),
      });
      const restoredStores = [...selectedStores];
      setRestorePassphrase('');
      setArchiveFile(null);
      resetRestoreFlow(false);
      setRestoreResult({ response: saved, restoredStores });
      onRestored();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (error instanceof ApiError && error.status === 412) {
        setRestoreError('The appliance changed after this restore was previewed. Run Preview restore again before confirming.');
        setConfirming(false);
        return;
      }
      setRestoreError(apiErrorMessage(error));
    } finally {
      setRestoreBusy(false);
    }
  }

  const blockers = preview ? restoreBlockers(preview) : [];
  const availableStores = manifest ? backupStoreOrder.filter((store) => manifest.scopes.includes(store)) : [];
  const includesIdentity = selectedStores.includes('users') || selectedStores.includes('userCredentials');

  return (
    <Stack className="details" gap="md">
      <div>
        <Title order={2}>Backup &amp; restore</Title>
        <Text c="dimmed">Export logical backups of Andaru&apos;s configuration and accounts, or preview and restore an archive.</Text>
      </div>

      {canCreate && (
        <Paper withBorder p="lg">
          <form onSubmit={(event) => void handleCreateBackup(event)} noValidate>
            <Stack gap="sm">
              <Title order={3} size="h4">Create backup</Title>
              <Text size="sm" c="dimmed">
                A standard backup downloads a plain <Text span ff="monospace" inherit>.zip</Text> archive containing the saved configuration and user accounts, without secret values or password hashes.
              </Text>

              {createError && <Alert color="red" role="alert" title="Backup failed">{createError}</Alert>}
              {createSuccess && (
                <Alert color="green" role="status" title="Backup downloaded">
                  Saved <Text span ff="monospace" inherit>{createSuccess.filename}</Text> ({formatByteSize(createSuccess.size)},{' '}
                  {createSuccess.encrypted ? 'encrypted age archive' : 'plain ZIP'})
                  {createSuccess.savedVersion ? ` · config v${createSuccess.savedVersion}` : ''}
                  {createSuccess.identityRevision ? ` · identity rev ${createSuccess.identityRevision}` : ''}
                  {createSuccess.capturedAt ? ` · ${formatTimestamp(createSuccess.capturedAt, timeZone)}` : ''}.
                </Alert>
              )}

              {canExportSecrets ? (
                <Stack gap="xs">
                  <Switch
                    label="Include configuration secrets"
                    description="Includes secret values such as PPPoE passwords. Encrypts the archive with a passphrase."
                    checked={includeSecrets}
                    disabled={createBusy}
                    onChange={(event) => toggleSensitive('secrets', event.currentTarget.checked)}
                  />
                  <Switch
                    label="Include user credentials"
                    description="Includes account password hashes and API token hashes. Encrypts the archive with a passphrase."
                    checked={includeCredentials}
                    disabled={createBusy}
                    onChange={(event) => toggleSensitive('credentials', event.currentTarget.checked)}
                  />
                </Stack>
              ) : (
                <Text size="sm" c="dimmed">
                  Your account does not have the <Text span ff="monospace" inherit>backups.exportSecrets</Text> capability; configuration secrets and user credentials will not be included.
                </Text>
              )}

              {sensitiveExport && (
                <Stack gap="xs">
                  <Alert color="yellow" role="status" title="Encrypted secret export">
                    The archive will be encrypted with age (scrypt) using the passphrase below. Store the passphrase separately; Andaru cannot recover it. This export is recorded in the protected audit log.
                  </Alert>
                  <PasswordInput
                    classNames={mono}
                    label="Archive passphrase"
                    description="Used to encrypt and later decrypt this backup (up to 1024 bytes)."
                    autoComplete="new-password"
                    required
                    disabled={createBusy}
                    value={exportPassphrase}
                    error={exportPassphraseErr}
                    onChange={(event) => setExportPassphrase(event.currentTarget.value)}
                  />
                  <PasswordInput
                    classNames={mono}
                    label="Confirm passphrase"
                    autoComplete="new-password"
                    required
                    disabled={createBusy}
                    value={exportPassphraseConfirm}
                    error={exportConfirmErr}
                    onChange={(event) => setExportPassphraseConfirm(event.currentTarget.value)}
                  />
                  <PasswordInput
                    label="Your current password"
                    description="Re-authentication is required to export secrets or user credentials."
                    autoComplete="current-password"
                    required
                    disabled={createBusy}
                    value={accountPassword}
                    onChange={(event) => setAccountPassword(event.currentTarget.value)}
                  />
                </Stack>
              )}

              <Group>
                <Button type="submit" loading={createBusy}>
                  {sensitiveExport ? 'Download encrypted backup' : 'Download backup'}
                </Button>
              </Group>
            </Stack>
          </form>
        </Paper>
      )}

      {canRestore && (
        <Paper withBorder p="lg">
          <Stack gap="md">
            <div>
              <Title order={3} size="h4">Restore from backup</Title>
              <Text size="sm" c="dimmed">
                Inspect an archive, choose which stores to restore, and review the full impact before writing to the saved store.
              </Text>
            </div>

            {restoreResult && (
              <Alert color="green" role="status" title={restoreResult.response.replayed ? 'Restore already completed (replayed)' : 'Backup restored'}>
                <Stack gap="xs">
                  <Text size="sm">
                    Restored {restoreResult.restoredStores.map((store) => backupStoreLabel[store]).join(', ')}.
                    {restoreResult.response.savedVersion ? ` Saved configuration version: ${restoreResult.response.savedVersion}.` : ''}
                    {restoreResult.response.identityRevision !== undefined ? ` Identity revision: ${restoreResult.response.identityRevision}.` : ''}
                  </Text>
                  {(restoreResult.restoredStores.includes('configuration') || restoreResult.restoredStores.includes('configurationSecrets')) && (
                    <Text size="sm">
                      Network changes are saved but not yet applied. <Anchor component={Link} to="/changes">Open Review and apply</Anchor> to activate them.
                    </Text>
                  )}
                  {restoreResult.response.unresolvedSecrets.length > 0 && (
                    <Text size="sm">
                      Unresolved secrets must be re-entered before applying:{' '}
                      <Text span ff="monospace" inherit>{restoreResult.response.unresolvedSecrets.join(', ')}</Text>.{' '}
                      <Anchor component={Link} to="/network/interfaces">Open Interfaces</Anchor>
                    </Text>
                  )}
                </Stack>
              </Alert>
            )}

            <form onSubmit={(event) => void handleInspect(event)} noValidate>
              <Stack gap="xs">
                <FileInput
                  label="Backup archive"
                  description={`Select a .zip or encrypted .age backup file (up to ${formatByteSize(maxBackupUploadBytes)}).`}
                  placeholder="Choose a backup file…"
                  accept=".zip,.age,application/zip,application/octet-stream"
                  clearable
                  disabled={inspectBusy || previewBusy || restoreBusy}
                  value={archiveFile}
                  onChange={handleFileChange}
                />
                <PasswordInput
                  classNames={mono}
                  label="Archive passphrase (encrypted backups)"
                  description="Leave empty for plain .zip backups; required for encrypted .age backups."
                  autoComplete="off"
                  disabled={inspectBusy || previewBusy || restoreBusy}
                  value={restorePassphrase}
                  onChange={(event) => handleRestorePassphraseChange(event.currentTarget.value)}
                />
                {inspectError && <Alert color="red" role="alert" title="Could not inspect archive">{inspectError}</Alert>}
                <Group>
                  <Button type="submit" variant="default" loading={inspectBusy} disabled={!archiveFile || previewBusy || restoreBusy}>
                    Inspect archive
                  </Button>
                </Group>
              </Stack>
            </form>

            {manifest && (
              <>
                <Divider />
                <Stack gap="sm">
                  <Title order={4} size="h5">Archive details</Title>
                  <Table className="facts" withRowBorders={false} verticalSpacing={2} horizontalSpacing={0} aria-label="Archive manifest">
                    <Table.Tbody>
                      <Table.Tr><Table.Th scope="row">Backup ID</Table.Th><Table.Td><Text ff="monospace" size="sm">{manifest.backupId}</Text></Table.Td></Table.Tr>
                      <Table.Tr><Table.Th scope="row">Captured</Table.Th><Table.Td>{formatTimestamp(manifest.createdAt, timeZone)}</Table.Td></Table.Tr>
                      <Table.Tr><Table.Th scope="row">Andaru version</Table.Th><Table.Td><Text ff="monospace" size="sm">{manifest.andaruVersion}</Text></Table.Td></Table.Tr>
                      <Table.Tr><Table.Th scope="row">Saved version</Table.Th><Table.Td><Text ff="monospace" size="sm">{manifest.savedVersion}</Text></Table.Td></Table.Tr>
                      <Table.Tr>
                        <Table.Th scope="row">Included stores</Table.Th>
                        <Table.Td>
                          <Group gap={6}>
                            {availableStores.map((store) => <Badge key={store} size="sm" variant="light">{backupStoreLabel[store]}</Badge>)}
                          </Group>
                        </Table.Td>
                      </Table.Tr>
                    </Table.Tbody>
                  </Table>

                  {manifest.savedButUnappliedAreas.length > 0 && (
                    <Alert color="yellow" role="status" title="Backup contained unapplied changes">
                      When this backup was captured, the following areas had saved changes that were not live: {manifest.savedButUnappliedAreas.join(', ')}.
                    </Alert>
                  )}

                  <Stack gap="xs">
                    <Text size="sm" fw={500}>Stores to restore</Text>
                    {availableStores.map((store) => (
                      <Checkbox
                        key={store}
                        label={backupStoreLabel[store]}
                        description={backupStoreDescription[store]}
                        checked={selectedStores.includes(store)}
                        disabled={previewBusy || restoreBusy}
                        onChange={(event) => handleStoreToggle(store, event.currentTarget.checked)}
                      />
                    ))}
                  </Stack>

                  {previewError && <Alert color="red" role="alert" title="Could not preview restore">{previewError}</Alert>}
                  <Group>
                    <Button
                      loading={previewBusy}
                      disabled={selectedStores.length === 0 || restoreBusy}
                      onClick={() => void handlePreviewRestore()}
                    >
                      Preview restore
                    </Button>
                  </Group>
                </Stack>
              </>
            )}

            {preview && (
              <>
                <Divider />
                <Stack gap="sm">
                  <Title order={4} size="h5">Restore preview</Title>
                  <Table withTableBorder verticalSpacing="xs" className="data-table" aria-label="Restore change summary">
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th scope="col">Store</Table.Th>
                        <Table.Th scope="col">Created</Table.Th>
                        <Table.Th scope="col">Updated</Table.Th>
                        <Table.Th scope="col">Deleted</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {selectedStores.map((store) => {
                        const counts = storeChangesOf(preview, store);
                        return (
                          <Table.Tr key={store}>
                            <Table.Td data-label="Store">{backupStoreLabel[store]}</Table.Td>
                            <Table.Td data-label="Created" className="tabular">{counts.created}</Table.Td>
                            <Table.Td data-label="Updated" className="tabular">{counts.updated}</Table.Td>
                            <Table.Td data-label="Deleted" className="tabular">{counts.deleted}</Table.Td>
                          </Table.Tr>
                        );
                      })}
                    </Table.Tbody>
                  </Table>

                  {preview.hardwareFindings.length > 0 && (
                    <Alert color="yellow" role="status" title="Hardware differences detected">
                      <Stack gap={4}>
                        <Text size="sm">You can still restore, then rebind or update interfaces before applying.</Text>
                        <List size="sm">
                          {preview.hardwareFindings.map((finding, idx) => (
                            <List.Item key={`${finding.code}-${finding.path}-${idx}`}>
                              <Text span ff="monospace" inherit>{finding.path || finding.code}</Text>: {finding.message}
                            </List.Item>
                          ))}
                        </List>
                      </Stack>
                    </Alert>
                  )}

                  {preview.unresolvedSecrets.length > 0 && (
                    <Alert color="yellow" role="status" title="Secrets will be marked unresolved">
                      The following secrets are not included in this restore and must be re-entered before applying:{' '}
                      <Text span ff="monospace" inherit>{preview.unresolvedSecrets.join(', ')}</Text>.
                    </Alert>
                  )}

                  {blockers.length > 0 && (
                    <Alert color="red" role="alert" title="Restore cannot proceed">
                      <List size="sm">
                        {blockers.map((blocker) => <List.Item key={blocker}>{blocker}</List.Item>)}
                      </List>
                    </Alert>
                  )}

                  {restoreError && <Alert color="red" role="alert" title="Restore failed">{restoreError}</Alert>}

                  {blockers.length === 0 && (
                    <>
                      <Text size="sm" c="dimmed">
                        Restoring writes to the saved store without immediately applying network changes.
                        {includesIdentity ? ' Restoring users or credentials may sign out active sessions.' : ''}
                      </Text>
                      {confirming ? (
                        <Alert color="red" role="alert" title="Confirm backup restore">
                          <Stack gap="xs">
                            <Text size="sm">
                              This overwrites the selected saved stores ({selectedStores.map((store) => backupStoreLabel[store]).join(', ')}) on this appliance.
                            </Text>
                            <Group>
                              <Button color="red" loading={restoreBusy} onClick={() => void handleExecuteRestore()}>Confirm restore</Button>
                              <Button variant="default" disabled={restoreBusy} onClick={() => setConfirming(false)}>Cancel</Button>
                            </Group>
                          </Stack>
                        </Alert>
                      ) : (
                        <Group>
                          <Button color="red" variant="light" onClick={() => setConfirming(true)}>Restore selected data…</Button>
                        </Group>
                      )}
                    </>
                  )}
                </Stack>
              </>
            )}
          </Stack>
        </Paper>
      )}
    </Stack>
  );
}
