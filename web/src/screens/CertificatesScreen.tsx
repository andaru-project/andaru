import {
  Alert,
  Badge,
  Button,
  Group,
  Modal,
  NumberInput,
  ScrollArea,
  Select,
  Stack,
  Table,
  Tabs,
  Text,
  Textarea,
  TextInput,
  Title,
} from '@mantine/core';
import { useCallback, useMemo, useRef, useState, type FormEvent } from 'react';
import { ApiClient, ApiError, fieldErrors, reportError, saveFailure } from '../api';
import { triggerDownload } from '../download';
import { formatTimestamp, useTimeZone } from '../timezone';
import type {
  PKICA,
  PKICertificate,
  PkiKeyAlgorithm,
  SavedObject,
  Session,
  TlsCryptKey,
} from '../types';
import { useResource } from '../useResource';
import { Loaded } from './Loaded';

interface CertificatesScreenProps {
  api: ApiClient;
  session: Session;
  onSaved(): void;
  onSessionLost(): void;
}

interface LoadedPki {
  cas: Array<SavedObject<PKICA>>;
  certificates: Array<SavedObject<PKICertificate>>;
  tlsCryptKeys: Array<SavedObject<TlsCryptKey>>;
}

type DeleteTarget =
  | { kind: 'ca'; item: SavedObject<PKICA> }
  | { kind: 'certificate'; item: SavedObject<PKICertificate> }
  | { kind: 'tls-crypt'; item: SavedObject<TlsCryptKey> };

const algorithmOptions: Array<{ value: PkiKeyAlgorithm; label: string }> = [
  { value: 'ecdsa-p256', label: 'ECDSA P-256 (recommended)' },
  { value: 'ecdsa-p384', label: 'ECDSA P-384' },
  { value: 'rsa-2048', label: 'RSA 2048' },
  { value: 'rsa-4096', label: 'RSA 4096' },
];

const usageOptions: Array<{ value: 'server' | 'client' | 'both'; label: string }> = [
  { value: 'server', label: 'Server TLS (HTTPS, VPN server)' },
  { value: 'client', label: 'Client auth (VPN client)' },
  { value: 'both', label: 'Server & Client' },
];

const pkiFieldPattern = /^\/?(name|commonName|organization|caId|parentCaId|sans|usage|keyAlgorithm|validDays|certPem|chainPem|privateKeyPem)(?:\/|$)/;

function parseSanInput(raw: string): string[] {
  return Array.from(
    new Set(
      raw
        .split(/[\n,]+/)
        .map((s) => s.trim())
        .filter(Boolean),
    ),
  );
}

function expiryBadge(notAfter: string): { color: string; label: string } {
  const ts = Date.parse(notAfter);
  if (!Number.isFinite(ts)) return { color: 'gray', label: 'Unknown' };
  const days = Math.ceil((ts - Date.now()) / 86_400_000);
  if (days <= 0) return { color: 'red', label: 'Expired' };
  if (days <= 30) return { color: 'orange', label: `Expires in ${days}d` };
  return { color: 'green', label: 'Valid' };
}

function shortFingerprint(fp: string): string {
  const clean = fp.replace(/:/g, '').toLowerCase();
  if (clean.length <= 16) return clean || '—';
  return `${clean.slice(0, 8)}…${clean.slice(-8)}`;
}

export function CertificatesScreen({ api, session, onSaved, onSessionLost }: CertificatesScreenProps) {
  const timeZone = useTimeZone();
  const capabilities = useMemo(() => new Set(session.capabilities), [session.capabilities]);
  const canReadConfig = capabilities.has('configuration.read');
  const canReadCerts = capabilities.has('certificates.read');
  const canManageCerts = capabilities.has('certificates.manage');

  const [activeTab, setActiveTab] = useState<string>('certificates');
  const [notice, setNotice] = useState('');
  const [actionError, setActionError] = useState('');
  const [downloadingId, setDownloadingId] = useState<string | null>(null);

  const [activeModal, setActiveModal] = useState<'ca' | 'cert' | 'import' | 'tls-crypt' | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState('');

  const load = useCallback(async (): Promise<LoadedPki> => {
    if (!canReadConfig) {
      return { cas: [], certificates: [], tlsCryptKeys: [] };
    }
    const [cas, certificates, tlsCryptKeys] = await Promise.all([
      api.pkiCAs(),
      api.pkiCertificates(),
      api.pkiTlsCryptKeys(),
    ]);
    return {
      cas: cas.items,
      certificates: certificates.items,
      tlsCryptKeys: tlsCryptKeys.items,
    };
  }, [api, canReadConfig]);

  const resource = useResource(load, onSessionLost);
  const cas = useMemo(() => resource.data?.cas ?? [], [resource.data]);
  const certificates = useMemo(() => resource.data?.certificates ?? [], [resource.data]);
  const tlsCryptKeys = useMemo(() => resource.data?.tlsCryptKeys ?? [], [resource.data]);

  const casById = useMemo(() => new Map(cas.map((c) => [c.value.id, c.value])), [cas]);
  const signingCAs = useMemo(
    () => cas.filter((c) => Boolean(c.value.keyRef && !c.value.keyRef.unresolved)).map((c) => c.value),
    [cas],
  );

  async function downloadPem(kind: 'ca' | 'certificate', id: string) {
    setDownloadingId(id);
    setActionError('');
    try {
      const result = kind === 'ca'
        ? await api.downloadPkiCAPem(id)
        : await api.downloadPkiCertificatePem(id);
      triggerDownload(result.blob, result.filename);
    } catch (error) {
      reportError(error, onSessionLost, setActionError);
    } finally {
      setDownloadingId(null);
    }
  }

  async function confirmDelete() {
    if (!deleteTarget || deleting) return;
    setDeleting(true);
    setDeleteError('');
    try {
      if (deleteTarget.kind === 'ca') {
        await api.deletePkiCA(deleteTarget.item.value.id, deleteTarget.item.version);
      } else if (deleteTarget.kind === 'certificate') {
        await api.deletePkiCertificate(deleteTarget.item.value.id, deleteTarget.item.version);
      } else {
        await api.deletePkiTlsCryptKey(deleteTarget.item.value.id, deleteTarget.item.version);
      }
      const deletedName = deleteTarget.item.value.name;
      setDeleteTarget(null);
      setNotice(`Deleted "${deletedName}".`);
      onSaved();
      void resource.reload();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (error instanceof ApiError && error.status === 412) {
        void resource.reload();
      }
      setDeleteError(saveFailure(error, new Map(), 'PKI object'));
    } finally {
      setDeleting(false);
    }
  }

  function handleCreated(message: string) {
    setNotice(message);
    setActionError('');
    onSaved();
    void resource.reload();
  }

  return (
    <Stack>
      <Group justify="space-between" align="flex-end" wrap="wrap">
        <div>
          <Title order={2}>Certificates &amp; PKI</Title>
          <Text c="dimmed">
            Manage internal certificate authorities, server and client certificates, and OpenVPN TLS-Crypt keys.
          </Text>
        </div>
        <Group gap="xs">
          {canManageCerts && activeTab === 'certificates' && (
            <>
              <Button size="xs" variant="default" onClick={() => setActiveModal('import')}>
                Import PEM
              </Button>
              <Button size="xs" onClick={() => setActiveModal('cert')}>
                Generate certificate
              </Button>
            </>
          )}
          {canManageCerts && activeTab === 'cas' && (
            <Button size="xs" onClick={() => setActiveModal('ca')}>
              Create CA
            </Button>
          )}
          {canManageCerts && activeTab === 'tls-crypt' && (
            <Button size="xs" onClick={() => setActiveModal('tls-crypt')}>
              Generate TLS-Crypt key
            </Button>
          )}
          <Button size="xs" variant="default" onClick={() => void resource.reload()}>
            Refresh
          </Button>
        </Group>
      </Group>

      {notice && (
        <Alert color="green" role="status" withCloseButton onClose={() => setNotice('')}>
          {notice}
        </Alert>
      )}
      {actionError && (
        <Alert color="red" role="alert" title="Certificate action failed" withCloseButton onClose={() => setActionError('')}>
          {actionError}
        </Alert>
      )}

      <Loaded resource={resource} what="certificates">
        {() => (
          <Tabs value={activeTab} onChange={(val) => setActiveTab(val ?? 'certificates')}>
            <Tabs.List>
              <Tabs.Tab value="certificates">Certificates ({certificates.length})</Tabs.Tab>
              <Tabs.Tab value="cas">Certificate Authorities ({cas.length})</Tabs.Tab>
              <Tabs.Tab value="tls-crypt">TLS-Crypt Keys ({tlsCryptKeys.length})</Tabs.Tab>
            </Tabs.List>

            <Tabs.Panel value="certificates" pt="md">
              {certificates.length === 0 ? (
                <Alert role="status" title="No managed certificates yet">
                  Generate a certificate from an internal CA or import an existing PEM certificate and private key.
                </Alert>
              ) : (
                <ScrollArea>
                  <Table className="data-table" striped highlightOnHover>
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Name</Table.Th>
                        <Table.Th>Subject &amp; SANs</Table.Th>
                        <Table.Th>Issuer</Table.Th>
                        <Table.Th>Algorithm</Table.Th>
                        <Table.Th>Validity</Table.Th>
                        <Table.Th>Actions</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {certificates.map((saved) => {
                        const cert = saved.value;
                        const issuer = cert.caId ? casById.get(cert.caId)?.name ?? cert.caId : 'External / Imported';
                        const exp = expiryBadge(cert.notAfter);
                        return (
                          <Table.Tr key={cert.id}>
                            <Table.Td data-label="Name">
                              <Stack gap={2}>
                                <Text fw={600} size="sm">{cert.name}</Text>
                                <Text size="xs" c="dimmed" ff="monospace" title={cert.fingerprintSha256}>
                                  {shortFingerprint(cert.fingerprintSha256)}
                                </Text>
                                {cert.keyRef?.unresolved && (
                                  <Badge size="xs" color="orange" variant="light">Key unresolved</Badge>
                                )}
                              </Stack>
                            </Table.Td>
                            <Table.Td data-label="Subject & SANs">
                              <Stack gap={2}>
                                <Text size="sm" ff="monospace">{cert.subject}</Text>
                                {(cert.sans?.length ?? 0) > 0 && (
                                  <Text size="xs" c="dimmed" ff="monospace">
                                    SANs: {cert.sans!.join(', ')}
                                  </Text>
                                )}
                              </Stack>
                            </Table.Td>
                            <Table.Td data-label="Issuer">
                              <Text size="sm">{issuer}</Text>
                            </Table.Td>
                            <Table.Td data-label="Algorithm">
                              <Badge size="sm" variant="default" tt="none">{cert.keyAlgorithm}</Badge>
                            </Table.Td>
                            <Table.Td data-label="Validity">
                              <Stack gap={2}>
                                <Badge size="sm" color={exp.color} variant="light">{exp.label}</Badge>
                                <Text size="xs" c="dimmed" className="tabular">
                                  Until {formatTimestamp(cert.notAfter, timeZone)}
                                </Text>
                              </Stack>
                            </Table.Td>
                            <Table.Td data-label="Actions">
                              <Group gap="xs" wrap="nowrap">
                                {canReadCerts && (
                                  <Button
                                    size="xs"
                                    variant="default"
                                    loading={downloadingId === cert.id}
                                    onClick={() => void downloadPem('certificate', cert.id)}
                                  >
                                    Download PEM
                                  </Button>
                                )}
                                {canManageCerts && (
                                  <Button
                                    size="xs"
                                    variant="light"
                                    color="red"
                                    onClick={() => {
                                      setDeleteError('');
                                      setDeleteTarget({ kind: 'certificate', item: saved });
                                    }}
                                  >
                                    Delete
                                  </Button>
                                )}
                              </Group>
                            </Table.Td>
                          </Table.Tr>
                        );
                      })}
                    </Table.Tbody>
                  </Table>
                </ScrollArea>
              )}
            </Tabs.Panel>

            <Tabs.Panel value="cas" pt="md">
              {cas.length === 0 ? (
                <Alert role="status" title="No certificate authorities yet">
                  Create an internal root or intermediate CA to sign certificates for HTTPS, IPsec, OpenVPN, and add-ons.
                </Alert>
              ) : (
                <ScrollArea>
                  <Table className="data-table" striped highlightOnHover>
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Name</Table.Th>
                        <Table.Th>Subject</Table.Th>
                        <Table.Th>Signing key</Table.Th>
                        <Table.Th>Validity</Table.Th>
                        <Table.Th>Actions</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {cas.map((saved) => {
                        const ca = saved.value;
                        const exp = expiryBadge(ca.notAfter);
                        const signingBadge = !ca.keyRef
                          ? { color: 'gray', label: 'Trust anchor only' }
                          : ca.keyRef.unresolved
                            ? { color: 'orange', label: 'Key unresolved' }
                            : { color: 'green', label: 'Can sign' };
                        return (
                          <Table.Tr key={ca.id}>
                            <Table.Td data-label="Name">
                              <Stack gap={2}>
                                <Text fw={600} size="sm">{ca.name}</Text>
                                <Text size="xs" c="dimmed" ff="monospace" title={ca.fingerprintSha256}>
                                  {shortFingerprint(ca.fingerprintSha256)}
                                </Text>
                              </Stack>
                            </Table.Td>
                            <Table.Td data-label="Subject">
                              <Text size="sm" ff="monospace">{ca.subject}</Text>
                            </Table.Td>
                            <Table.Td data-label="Signing key">
                              <Badge size="sm" color={signingBadge.color} variant="light">
                                {signingBadge.label}
                              </Badge>
                            </Table.Td>
                            <Table.Td data-label="Validity">
                              <Stack gap={2}>
                                <Badge size="sm" color={exp.color} variant="light">{exp.label}</Badge>
                                <Text size="xs" c="dimmed" className="tabular">
                                  Until {formatTimestamp(ca.notAfter, timeZone)}
                                </Text>
                              </Stack>
                            </Table.Td>
                            <Table.Td data-label="Actions">
                              <Group gap="xs" wrap="nowrap">
                                {canReadCerts && (
                                  <Button
                                    size="xs"
                                    variant="default"
                                    loading={downloadingId === ca.id}
                                    onClick={() => void downloadPem('ca', ca.id)}
                                  >
                                    Download PEM
                                  </Button>
                                )}
                                {canManageCerts && (
                                  <Button
                                    size="xs"
                                    variant="light"
                                    color="red"
                                    onClick={() => {
                                      setDeleteError('');
                                      setDeleteTarget({ kind: 'ca', item: saved });
                                    }}
                                  >
                                    Delete
                                  </Button>
                                )}
                              </Group>
                            </Table.Td>
                          </Table.Tr>
                        );
                      })}
                    </Table.Tbody>
                  </Table>
                </ScrollArea>
              )}
            </Tabs.Panel>

            <Tabs.Panel value="tls-crypt" pt="md">
              {tlsCryptKeys.length === 0 ? (
                <Alert role="status" title="No TLS-Crypt keys yet">
                  Generate a 2048-bit OpenVPN TLS-Crypt static key to authenticate and encrypt OpenVPN control channels.
                </Alert>
              ) : (
                <ScrollArea>
                  <Table className="data-table" striped highlightOnHover>
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Name</Table.Th>
                        <Table.Th>Key status</Table.Th>
                        <Table.Th>Actions</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {tlsCryptKeys.map((saved) => {
                        const key = saved.value;
                        return (
                          <Table.Tr key={key.id}>
                            <Table.Td data-label="Name">
                              <Text fw={600} size="sm">{key.name}</Text>
                            </Table.Td>
                            <Table.Td data-label="Key status">
                              <Badge size="sm" color={key.keyRef?.unresolved ? 'orange' : 'green'} variant="light">
                                {key.keyRef?.unresolved ? 'Unresolved' : 'Stored'}
                              </Badge>
                            </Table.Td>
                            <Table.Td data-label="Actions">
                              {canManageCerts && (
                                <Button
                                  size="xs"
                                  variant="light"
                                  color="red"
                                  onClick={() => {
                                    setDeleteError('');
                                    setDeleteTarget({ kind: 'tls-crypt', item: saved });
                                  }}
                                >
                                  Delete
                                </Button>
                              )}
                            </Table.Td>
                          </Table.Tr>
                        );
                      })}
                    </Table.Tbody>
                  </Table>
                </ScrollArea>
              )}
            </Tabs.Panel>
          </Tabs>
        )}
      </Loaded>

      {activeModal === 'ca' && (
        <CreateCAModal
          api={api}
          signingCAs={signingCAs}
          onClose={() => setActiveModal(null)}
          onCreated={(created) => {
            setActiveModal(null);
            handleCreated(`Created certificate authority "${created.value.name}".`);
          }}
          onSessionLost={onSessionLost}
        />
      )}

      {activeModal === 'cert' && (
        <GenerateCertModal
          api={api}
          signingCAs={signingCAs}
          onClose={() => setActiveModal(null)}
          onCreated={(created) => {
            setActiveModal(null);
            handleCreated(`Generated certificate "${created.value.name}".`);
          }}
          onSessionLost={onSessionLost}
        />
      )}

      {activeModal === 'import' && (
        <ImportCertModal
          api={api}
          onClose={() => setActiveModal(null)}
          onCreated={(created) => {
            setActiveModal(null);
            handleCreated(`Imported certificate "${created.value.name}".`);
          }}
          onSessionLost={onSessionLost}
        />
      )}

      {activeModal === 'tls-crypt' && (
        <GenerateTlsCryptModal
          api={api}
          onClose={() => setActiveModal(null)}
          onCreated={(created) => {
            setActiveModal(null);
            handleCreated(`Generated TLS-Crypt key "${created.value.name}".`);
          }}
          onSessionLost={onSessionLost}
        />
      )}

      <Modal
        opened={deleteTarget !== null}
        onClose={() => { if (!deleting) setDeleteTarget(null); }}
        title="Delete PKI object"
        centered
        lockScroll={false}
        closeButtonProps={{ 'aria-label': 'Close', disabled: deleting }}
      >
        <Stack>
          {deleteError && (
            <Alert color="red" role="alert" title="Could not delete">
              {deleteError}
            </Alert>
          )}
          <Text size="sm">
            Delete <strong>{deleteTarget?.item.value.name}</strong>? Objects referenced by management HTTPS, certificates, VPNs, or add-ons cannot be deleted while in use.
          </Text>
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={deleting} onClick={() => setDeleteTarget(null)}>
              Cancel
            </Button>
            <Button color="red" loading={deleting} onClick={() => void confirmDelete()}>
              Delete
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}

interface CreateCAModalProps {
  api: ApiClient;
  signingCAs: PKICA[];
  onClose(): void;
  onCreated(created: SavedObject<PKICA>): void;
  onSessionLost(): void;
}

function CreateCAModal({ api, signingCAs, onClose, onCreated, onSessionLost }: CreateCAModalProps) {
  const [name, setName] = useState('');
  const [commonName, setCommonName] = useState('');
  const [organization, setOrganization] = useState('');
  const [parentCaId, setParentCaId] = useState('');
  const [keyAlgorithm, setKeyAlgorithm] = useState<PkiKeyAlgorithm>('ecdsa-p384');
  const [validDays, setValidDays] = useState<number | string>(3650);
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;
    const nextErrors = new Map<string, string>();
    if (!name.trim()) nextErrors.set('name', 'Enter a name.');
    if (!commonName.trim()) nextErrors.set('commonName', 'Enter a common name.');
    const days = typeof validDays === 'number' ? validDays : Number(validDays);
    if (!Number.isInteger(days) || days < 1 || days > 36500) {
      nextErrors.set('validDays', 'Validity must be between 1 and 36500 days.');
    }
    if (nextErrors.size > 0) {
      setErrors(nextErrors);
      return;
    }

    submitting.current = true;
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      const created = await api.generatePkiCA({
        name: name.trim(),
        commonName: commonName.trim(),
        ...(organization.trim() ? { organization: organization.trim() } : {}),
        ...(parentCaId ? { parentCaId } : {}),
        keyAlgorithm,
        validDays: days,
      });
      onCreated(created);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      const fields = fieldErrors(error, pkiFieldPattern);
      setErrors(fields);
      setFailure(error instanceof ApiError && error.code === 'CA_KEY_UNAVAILABLE'
        ? error.message
        : saveFailure(error, fields, 'certificate authority'));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <Modal
      opened
      onClose={() => { if (!busy) onClose(); }}
      title="Create certificate authority"
      centered
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
    >
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert color="red" role="alert" title="CA was not created">{failure}</Alert>}
          <TextInput
            label="Name"
            placeholder="Internal Root CA"
            required
            disabled={busy}
            value={name}
            error={errors.get('name')}
            onChange={(e) => setName(e.currentTarget.value)}
          />
          <TextInput
            label="Common Name (CN)"
            placeholder="Andaru Internal Root CA"
            required
            disabled={busy}
            value={commonName}
            error={errors.get('commonName')}
            onChange={(e) => setCommonName(e.currentTarget.value)}
          />
          <TextInput
            label="Organization (optional)"
            placeholder="Example Corp"
            disabled={busy}
            value={organization}
            error={errors.get('organization')}
            onChange={(e) => setOrganization(e.currentTarget.value)}
          />
          {signingCAs.length > 0 && (
            <Select
              label="Parent CA (optional)"
              description="Leave empty to create a self-signed root CA, or select a parent CA for an intermediate CA."
              placeholder="Self-signed root CA"
              clearable
              searchable
              disabled={busy}
              data={signingCAs.map((ca) => ({ value: ca.id, label: ca.name }))}
              value={parentCaId || null}
              error={errors.get('parentCaId')}
              onChange={(val) => setParentCaId(val ?? '')}
            />
          )}
          <Select
            label="Key algorithm"
            disabled={busy}
            allowDeselect={false}
            data={algorithmOptions}
            value={keyAlgorithm}
            error={errors.get('keyAlgorithm')}
            onChange={(val) => { if (val) setKeyAlgorithm(val as PkiKeyAlgorithm); }}
          />
          <NumberInput
            label="Validity (days)"
            required
            disabled={busy}
            allowDecimal={false}
            min={1}
            max={36500}
            value={validDays}
            error={errors.get('validDays')}
            onChange={setValidDays}
          />
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy}>Create CA</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

interface GenerateCertModalProps {
  api: ApiClient;
  signingCAs: PKICA[];
  onClose(): void;
  onCreated(created: SavedObject<PKICertificate>): void;
  onSessionLost(): void;
}

function GenerateCertModal({ api, signingCAs, onClose, onCreated, onSessionLost }: GenerateCertModalProps) {
  const [name, setName] = useState('');
  const [caId, setCaId] = useState(signingCAs[0]?.id ?? '');
  const [commonName, setCommonName] = useState('');
  const [sansInput, setSansInput] = useState('');
  const [usage, setUsage] = useState<'server' | 'client' | 'both'>('server');
  const [keyAlgorithm, setKeyAlgorithm] = useState<PkiKeyAlgorithm>('ecdsa-p256');
  const [validDays, setValidDays] = useState<number | string>(365);
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;
    const nextErrors = new Map<string, string>();
    if (!name.trim()) nextErrors.set('name', 'Enter a name.');
    if (!caId) nextErrors.set('caId', 'Select a signing CA.');
    if (!commonName.trim()) nextErrors.set('commonName', 'Enter a common name.');
    const days = typeof validDays === 'number' ? validDays : Number(validDays);
    if (!Number.isInteger(days) || days < 1 || days > 3650) {
      nextErrors.set('validDays', 'Validity must be between 1 and 3650 days.');
    }
    if (nextErrors.size > 0) {
      setErrors(nextErrors);
      return;
    }

    submitting.current = true;
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      const sans = parseSanInput(sansInput);
      const created = await api.generatePkiCertificate({
        name: name.trim(),
        caId,
        commonName: commonName.trim(),
        ...(sans.length > 0 ? { sans } : {}),
        usage,
        keyAlgorithm,
        validDays: days,
      });
      onCreated(created);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      const fields = fieldErrors(error, pkiFieldPattern);
      setErrors(fields);
      setFailure(error instanceof ApiError && error.code === 'CA_KEY_UNAVAILABLE'
        ? error.message
        : saveFailure(error, fields, 'certificate'));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <Modal
      opened
      onClose={() => { if (!busy) onClose(); }}
      title="Generate certificate"
      centered
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
    >
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert color="red" role="alert" title="Certificate was not generated">{failure}</Alert>}
          {signingCAs.length === 0 && (
            <Alert color="orange" role="status" title="No signing CA available">
              Create an internal certificate authority first, or import an external PEM certificate instead.
            </Alert>
          )}
          <TextInput
            label="Name"
            placeholder="Gateway HTTPS"
            required
            disabled={busy}
            value={name}
            error={errors.get('name')}
            onChange={(e) => setName(e.currentTarget.value)}
          />
          <Select
            label="Issuing CA"
            required
            searchable
            allowDeselect={false}
            disabled={busy || signingCAs.length === 0}
            data={signingCAs.map((ca) => ({ value: ca.id, label: ca.name }))}
            value={caId || null}
            error={errors.get('caId')}
            onChange={(val) => setCaId(val ?? '')}
          />
          <TextInput
            label="Common Name (CN)"
            placeholder="fw.example.lan"
            required
            disabled={busy}
            value={commonName}
            error={errors.get('commonName')}
            onChange={(e) => setCommonName(e.currentTarget.value)}
          />
          <Textarea
            label="Subject Alternative Names (SANs)"
            description="DNS names or IP addresses, separated by commas or new lines."
            placeholder={'fw.example.lan\n192.168.1.1'}
            autosize
            minRows={2}
            maxRows={5}
            disabled={busy}
            value={sansInput}
            error={errors.get('sans')}
            onChange={(e) => setSansInput(e.currentTarget.value)}
          />
          <Select
            label="Certificate usage"
            disabled={busy}
            allowDeselect={false}
            data={usageOptions}
            value={usage}
            error={errors.get('usage')}
            onChange={(val) => { if (val) setUsage(val as 'server' | 'client' | 'both'); }}
          />
          <Select
            label="Key algorithm"
            disabled={busy}
            allowDeselect={false}
            data={algorithmOptions}
            value={keyAlgorithm}
            error={errors.get('keyAlgorithm')}
            onChange={(val) => { if (val) setKeyAlgorithm(val as PkiKeyAlgorithm); }}
          />
          <NumberInput
            label="Validity (days)"
            required
            disabled={busy}
            allowDecimal={false}
            min={1}
            max={3650}
            value={validDays}
            error={errors.get('validDays')}
            onChange={setValidDays}
          />
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy} disabled={signingCAs.length === 0}>Generate</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

interface ImportCertModalProps {
  api: ApiClient;
  onClose(): void;
  onCreated(created: SavedObject<PKICertificate>): void;
  onSessionLost(): void;
}

function ImportCertModal({ api, onClose, onCreated, onSessionLost }: ImportCertModalProps) {
  const [name, setName] = useState('');
  const [certPem, setCertPem] = useState('');
  const [chainPem, setChainPem] = useState('');
  const [privateKeyPem, setPrivateKeyPem] = useState('');
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;
    const nextErrors = new Map<string, string>();
    if (!name.trim()) nextErrors.set('name', 'Enter a name.');
    if (!certPem.trim()) nextErrors.set('certPem', 'Paste the certificate PEM.');
    if (!privateKeyPem.trim()) nextErrors.set('privateKeyPem', 'Paste the matching private key PEM.');
    if (nextErrors.size > 0) {
      setErrors(nextErrors);
      return;
    }

    submitting.current = true;
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      const created = await api.importPkiCertificate({
        name: name.trim(),
        certPem: certPem.trim(),
        ...(chainPem.trim() ? { chainPem: chainPem.trim() } : {}),
        privateKeyPem: privateKeyPem.trim(),
      });
      setPrivateKeyPem('');
      onCreated(created);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      const fields = fieldErrors(error, pkiFieldPattern);
      setErrors(fields);
      setFailure(error instanceof ApiError && error.code === 'CA_KEY_UNAVAILABLE'
        ? error.message
        : saveFailure(error, fields, 'certificate'));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <Modal
      opened
      onClose={() => { if (!busy) onClose(); }}
      title="Import PEM certificate"
      size="lg"
      centered
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
    >
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert color="red" role="alert" title="Certificate was not imported">{failure}</Alert>}
          <TextInput
            label="Name"
            placeholder="Edge wildcard TLS"
            required
            disabled={busy}
            value={name}
            error={errors.get('name')}
            onChange={(e) => setName(e.currentTarget.value)}
          />
          <Textarea
            label="Certificate PEM"
            placeholder="-----BEGIN CERTIFICATE-----"
            required
            autosize
            minRows={4}
            maxRows={8}
            ff="monospace"
            disabled={busy}
            value={certPem}
            error={errors.get('certPem')}
            onChange={(e) => setCertPem(e.currentTarget.value)}
          />
          <Textarea
            label="Intermediate chain PEM (optional)"
            placeholder="-----BEGIN CERTIFICATE-----"
            autosize
            minRows={3}
            maxRows={6}
            ff="monospace"
            disabled={busy}
            value={chainPem}
            error={errors.get('chainPem')}
            onChange={(e) => setChainPem(e.currentTarget.value)}
          />
          <Textarea
            label="Private key PEM"
            description="Stored write-only in the appliance secret store. Never displayed or returned after import."
            placeholder="-----BEGIN PRIVATE KEY-----"
            required
            autosize
            minRows={4}
            maxRows={8}
            ff="monospace"
            disabled={busy}
            value={privateKeyPem}
            error={errors.get('privateKeyPem')}
            onChange={(e) => setPrivateKeyPem(e.currentTarget.value)}
          />
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy}>Import certificate</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

interface GenerateTlsCryptModalProps {
  api: ApiClient;
  onClose(): void;
  onCreated(created: SavedObject<TlsCryptKey>): void;
  onSessionLost(): void;
}

function GenerateTlsCryptModal({ api, onClose, onCreated, onSessionLost }: GenerateTlsCryptModalProps) {
  const [name, setName] = useState('');
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;
    if (!name.trim()) {
      setErrors(new Map([['name', 'Enter a name.']]));
      return;
    }
    submitting.current = true;
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      const created = await api.generatePkiTlsCryptKey({ name: name.trim() });
      onCreated(created);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      const fields = fieldErrors(error, pkiFieldPattern);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'TLS-Crypt key'));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <Modal
      opened
      onClose={() => { if (!busy) onClose(); }}
      title="Generate OpenVPN TLS-Crypt key"
      centered
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
    >
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert color="red" role="alert" title="TLS-Crypt key was not generated">{failure}</Alert>}
          <TextInput
            label="Name"
            placeholder="Office OpenVPN TLS-Crypt"
            required
            disabled={busy}
            value={name}
            error={errors.get('name')}
            onChange={(e) => setName(e.currentTarget.value)}
          />
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy}>Generate key</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
