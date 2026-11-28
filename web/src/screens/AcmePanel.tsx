import {
  Alert,
  Badge,
  Button,
  Divider,
  Group,
  Modal,
  MultiSelect,
  NumberInput,
  PasswordInput,
  ScrollArea,
  SegmentedControl,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  Textarea,
  TextInput,
  Title,
} from '@mantine/core';
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { ApiClient, ApiError, fieldErrors, reportError, saveFailure } from '../api';
import { formatTimestamp, useTimeZone } from '../timezone';
import type {
  AcmeAccount,
  AcmeAccountWrite,
  AcmeDnsProvider,
  AcmeKeyAlgorithm,
  AcmeOrder,
  AcmeOrderWrite,
  AcmeStatusResponse,
  PKICertificate,
  SavedObject,
  SecretView,
  SecretWrite,
  Segment,
} from '../types';
import { useResource, useVisiblePoll } from '../useResource';
import { Loaded } from './Loaded';

interface AcmePanelProps {
  api: ApiClient;
  certificates: PKICertificate[];
  canReadConfig: boolean;
  canReadCerts: boolean;
  canManageCerts: boolean;
  onSaved(): void;
  onSessionLost(): void;
}

interface LoadedAcme {
  accounts: Array<SavedObject<AcmeAccount>>;
  orders: Array<SavedObject<AcmeOrder>>;
  segments: Segment[];
  status?: AcmeStatusResponse;
}

type DeleteAcmeTarget =
  | { kind: 'account'; item: SavedObject<AcmeAccount> }
  | { kind: 'order'; item: SavedObject<AcmeOrder> };

const LETS_ENCRYPT_PROD = 'https://acme-v02.api.letsencrypt.org/directory';
const LETS_ENCRYPT_STAGING = 'https://acme-staging-v02.api.letsencrypt.org/directory';

const directoryPresets = [
  { value: LETS_ENCRYPT_PROD, label: "Let's Encrypt Production" },
  { value: LETS_ENCRYPT_STAGING, label: "Let's Encrypt Staging (testing)" },
  { value: 'custom', label: 'Custom ACME v2 URL' },
];

const acmeAlgorithms: Array<{ value: AcmeKeyAlgorithm; label: string }> = [
  { value: 'ecdsa-p256', label: 'ECDSA P-256 (recommended)' },
  { value: 'ecdsa-p384', label: 'ECDSA P-384' },
  { value: 'rsa-2048', label: 'RSA 2048' },
];

const dnsProviders: Array<{ value: AcmeDnsProvider; label: string }> = [
  { value: 'cloudflare', label: 'Cloudflare (API Token)' },
  { value: 'hetzner', label: 'Hetzner DNS (API Token)' },
  { value: 'digitalocean', label: 'DigitalOcean DNS (API Token)' },
  { value: 'porkbun', label: 'Porkbun (API Key + Secret)' },
  { value: 'acme-dns', label: 'acme-dns (CNAME delegation)' },
  { value: 'webhook', label: 'Webhook (Custom DNS bridge)' },
];

const acmeFieldPattern = /^\/?(name|directoryUrl|email|eabKeyId|eabHmacRef|accountId|domains|keyAlgorithm|renewBeforeDays|challenge)(?:\/|$)/;

function directoryLabel(url: string): string {
  if (url === LETS_ENCRYPT_PROD) return "Let's Encrypt Production";
  if (url === LETS_ENCRYPT_STAGING) return "Let's Encrypt Staging";
  return url;
}

function orderStateBadge(state?: string): { color: string; label: string } {
  switch (state) {
    case 'running':
      return { color: 'blue', label: 'Running' };
    case 'succeeded':
      return { color: 'green', label: 'Succeeded' };
    case 'failed':
      return { color: 'red', label: 'Failed' };
    default:
      return { color: 'gray', label: 'Idle' };
  }
}

function parseDomains(raw: string): string[] {
  return Array.from(
    new Set(
      raw
        .split(/[\n,]+/)
        .map((d) => d.trim().toLowerCase())
        .filter(Boolean),
    ),
  );
}

function newSecretName(prefix: string): string {
  const bytes = new Uint8Array(6);
  globalThis.crypto.getRandomValues(bytes);
  const suffix = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${prefix}_${suffix}`;
}

function secretWriteIfSet(
  typed: string,
  existing: SecretView | undefined,
  prefix: string,
): SecretWrite | undefined {
  if (typed.trim() !== '') {
    return {
      secret: existing?.secret || newSecretName(prefix),
      set: typed.trim(),
      expectedVersion: existing?.version || '0',
    };
  }
  return undefined;
}

export function AcmePanel({
  api,
  certificates,
  canReadConfig,
  canReadCerts,
  canManageCerts,
  onSaved,
  onSessionLost,
}: AcmePanelProps) {
  const timeZone = useTimeZone();
  const [notice, setNotice] = useState('');
  const [actionError, setActionError] = useState('');
  const [busyOrderId, setBusyOrderId] = useState<string | null>(null);

  const [accountModal, setAccountModal] = useState<{ mode: 'create' } | { mode: 'edit'; item: SavedObject<AcmeAccount> } | null>(null);
  const [orderModal, setOrderModal] = useState<{ mode: 'create' } | { mode: 'edit'; item: SavedObject<AcmeOrder> } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<DeleteAcmeTarget | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState('');

  const load = useCallback(async (): Promise<LoadedAcme> => {
    const [accountsRes, ordersRes, segmentsRes, statusRes] = await Promise.all([
      canReadConfig ? api.acmeAccounts() : undefined,
      canReadConfig ? api.acmeOrders() : undefined,
      canReadConfig ? api.segments() : undefined,
      canReadCerts ? api.acmeStatus() : undefined,
    ]);
    return {
      accounts: accountsRes?.items ?? [],
      orders: ordersRes?.items ?? [],
      segments: (segmentsRes?.items ?? []).map((item) => item.value),
      status: statusRes,
    };
  }, [api, canReadConfig, canReadCerts]);

  const resource = useResource(load, onSessionLost);
  const accounts = useMemo(() => resource.data?.accounts ?? [], [resource.data]);
  const orders = useMemo(() => resource.data?.orders ?? [], [resource.data]);
  const segments = useMemo(() => resource.data?.segments ?? [], [resource.data]);
  const status = resource.data?.status;

  const anyRunning = Boolean(status?.items.some((s) => s.state === 'running'));
  useVisiblePoll(resource.refresh, 3_000, canReadCerts && anyRunning);

  // Refresh the parent Certificates list when an in-flight ACME order completes successfully.
  const prevRunningRef = useRef<Set<string>>(new Set());
  useEffect(() => {
    const items = status?.items ?? [];
    const nextRunning = new Set<string>();
    let completed = false;
    for (const item of items) {
      if (item.state === 'running') {
        nextRunning.add(item.orderId);
      } else if (item.state === 'succeeded' && prevRunningRef.current.has(item.orderId)) {
        completed = true;
      }
    }
    prevRunningRef.current = nextRunning;
    if (completed) {
      onSaved();
    }
  }, [status, onSaved]);

  const accountsById = useMemo(() => new Map(accounts.map((a) => [a.value.id, a.value])), [accounts]);
  const certsById = useMemo(() => new Map(certificates.map((c) => [c.id, c])), [certificates]);
  const segmentsById = useMemo(() => new Map(segments.map((s) => [s.id, s.name])), [segments]);
  const orderStatusById = useMemo(
    () => new Map((status?.items ?? []).map((s) => [s.orderId, s])),
    [status],
  );
  const accountStatusById = useMemo(
    () => new Map((status?.accounts ?? []).map((s) => [s.accountId, s])),
    [status],
  );

  async function triggerOrder(id: string, staging: boolean) {
    setBusyOrderId(id);
    setActionError('');
    setNotice('');
    try {
      if (staging) {
        await api.testStagingAcmeOrder(id);
        setNotice('Started Let\'s Encrypt staging dry-run test. The live certificate will not be modified.');
      } else {
        await api.renewAcmeOrder(id);
        setNotice('Triggered certificate issuance / renewal.');
      }
      void resource.refresh();
    } catch (error) {
      reportError(error, onSessionLost, setActionError);
    } finally {
      setBusyOrderId(null);
    }
  }

  async function confirmDelete() {
    if (!deleteTarget || deleting) return;
    setDeleting(true);
    setDeleteError('');
    try {
      if (deleteTarget.kind === 'account') {
        await api.deleteAcmeAccount(deleteTarget.item.value.id, deleteTarget.item.version);
      } else {
        await api.deleteAcmeOrder(deleteTarget.item.value.id, deleteTarget.item.version);
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
      setDeleteError(saveFailure(error, new Map(), 'ACME resource'));
    } finally {
      setDeleting(false);
    }
  }

  function handleSaved(message: string) {
    setNotice(message);
    setActionError('');
    onSaved();
    void resource.reload();
  }

  return (
    <Stack gap="lg">
      {notice && (
        <Alert color="green" role="status" withCloseButton onClose={() => setNotice('')}>
          {notice}
        </Alert>
      )}
      {actionError && (
        <Alert color="red" role="alert" title="ACME action failed" withCloseButton onClose={() => setActionError('')}>
          {actionError}
        </Alert>
      )}

      <Loaded resource={resource} what="ACME configuration">
        {() => (
          <Stack gap="lg">
            {/* ACME Accounts */}
            <Stack gap="sm">
              <Group justify="space-between" align="flex-end" wrap="wrap">
                <div>
                  <Title order={3} size="h4">ACME Accounts</Title>
                  <Text size="sm" c="dimmed">
                    Let&apos;s Encrypt or custom RFC 8555 ACME v2 accounts used to issue and renew certificates.
                  </Text>
                </div>
                {canManageCerts && (
                  <Button size="xs" onClick={() => setAccountModal({ mode: 'create' })}>
                    Add ACME account
                  </Button>
                )}
              </Group>

              {accounts.length === 0 ? (
                <Alert role="status" title="No ACME accounts configured">
                  Add a Let&apos;s Encrypt Production, Staging, or custom ACME v2 account to start issuing automated certificates.
                </Alert>
              ) : (
                <ScrollArea>
                  <Table className="data-table" striped highlightOnHover>
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Name</Table.Th>
                        <Table.Th>Directory</Table.Th>
                        <Table.Th>Email</Table.Th>
                        <Table.Th>Registration</Table.Th>
                        <Table.Th>Actions</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {accounts.map((saved) => {
                        const acc = saved.value;
                        const accSt = accountStatusById.get(acc.id);
                        return (
                          <Table.Tr key={acc.id}>
                            <Table.Td data-label="Name">
                              <Group gap="xs">
                                <Text fw={600} size="sm">{acc.name}</Text>
                                {acc.eabKeyId && <Badge size="xs" variant="light">EAB</Badge>}
                              </Group>
                            </Table.Td>
                            <Table.Td data-label="Directory">
                              <Text size="sm">{directoryLabel(acc.directoryUrl)}</Text>
                            </Table.Td>
                            <Table.Td data-label="Email">
                              <Text size="sm" ff="monospace">{acc.email}</Text>
                            </Table.Td>
                            <Table.Td data-label="Registration">
                              <Badge size="sm" color={accSt?.status === 'valid' ? 'green' : 'gray'} variant="light">
                                {accSt?.status === 'valid' ? 'Registered' : 'Pending first order'}
                              </Badge>
                            </Table.Td>
                            <Table.Td data-label="Actions">
                              {canManageCerts && (
                                <Group gap="xs" wrap="nowrap">
                                  <Button size="xs" variant="default" onClick={() => setAccountModal({ mode: 'edit', item: saved })}>
                                    Edit
                                  </Button>
                                  <Button
                                    size="xs"
                                    variant="light"
                                    color="red"
                                    onClick={() => {
                                      setDeleteError('');
                                      setDeleteTarget({ kind: 'account', item: saved });
                                    }}
                                  >
                                    Delete
                                  </Button>
                                </Group>
                              )}
                            </Table.Td>
                          </Table.Tr>
                        );
                      })}
                    </Table.Tbody>
                  </Table>
                </ScrollArea>
              )}
            </Stack>

            <Divider />

            {/* ACME Certificate Orders */}
            <Stack gap="sm">
              <Group justify="space-between" align="flex-end" wrap="wrap">
                <div>
                  <Title order={3} size="h4">ACME Certificate Orders</Title>
                  <Text size="sm" c="dimmed">
                    Automated issuance and renewal via HTTP-01 or DNS-01 challenges. Renewed certificates notify core services and subscribed add-ons automatically.
                  </Text>
                </div>
                {canManageCerts && (
                  <Button
                    size="xs"
                    disabled={accounts.length === 0}
                    onClick={() => setOrderModal({ mode: 'create' })}
                  >
                    Add ACME order
                  </Button>
                )}
              </Group>

              {orders.length === 0 ? (
                <Alert role="status" title="No ACME orders configured">
                  {accounts.length === 0
                    ? 'Create an ACME account above before adding a certificate order.'
                    : 'Add an ACME order to issue and automatically renew a certificate.'}
                </Alert>
              ) : (
                <ScrollArea>
                  <Table className="data-table" striped highlightOnHover>
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Name</Table.Th>
                        <Table.Th>Domains</Table.Th>
                        <Table.Th>Challenge</Table.Th>
                        <Table.Th>Certificate</Table.Th>
                        <Table.Th>Status</Table.Th>
                        <Table.Th>Actions</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {orders.map((saved) => {
                        const order = saved.value;
                        const st = orderStatusById.get(order.id);
                        const badge = orderStateBadge(st?.state);
                        const linkedCert = order.certificateId ? certsById.get(order.certificateId) : undefined;
                        const account = accountsById.get(order.accountId);
                        const challengeSummary = order.challenge.type === 'http-01'
                          ? `HTTP-01 (${(order.challenge.http01?.listenSegments ?? []).map((id) => segmentsById.get(id) ?? id).join(', ') || 'none'})`
                          : `DNS-01 (${order.challenge.dns01?.provider ?? 'unknown'})`;
                        const isRunning = st?.state === 'running' || busyOrderId === order.id;

                        return (
                          <Table.Tr key={order.id}>
                            <Table.Td data-label="Name">
                              <Stack gap={2}>
                                <Group gap="xs">
                                  <Text fw={600} size="sm">{order.name}</Text>
                                  {!order.enabled && <Badge size="xs" color="gray" variant="light">Disabled</Badge>}
                                </Group>
                                <Text size="xs" c="dimmed">{account?.name ?? order.accountId}</Text>
                              </Stack>
                            </Table.Td>
                            <Table.Td data-label="Domains">
                              <Text size="sm" ff="monospace">{order.domains.join(', ')}</Text>
                            </Table.Td>
                            <Table.Td data-label="Challenge">
                              <Text size="sm">{challengeSummary}</Text>
                            </Table.Td>
                            <Table.Td data-label="Certificate">
                              {linkedCert ? (
                                <Text size="sm" fw={500}>{linkedCert.name}</Text>
                              ) : order.certificateId ? (
                                <Text size="sm" ff="monospace">{order.certificateId}</Text>
                              ) : (
                                <Text size="xs" c="dimmed">Created on first issuance</Text>
                              )}
                            </Table.Td>
                            <Table.Td data-label="Status">
                              <Stack gap={2}>
                                <Badge size="sm" color={badge.color} variant="light">{badge.label}</Badge>
                                {st?.lastError && (
                                  <Text size="xs" c="red" lineClamp={2} title={st.lastError}>{st.lastError}</Text>
                                )}
                                {st?.nextRenewAt && (
                                  <Text size="xs" c="dimmed" className="tabular">
                                    Next: {formatTimestamp(st.nextRenewAt, timeZone)}
                                  </Text>
                                )}
                              </Stack>
                            </Table.Td>
                            <Table.Td data-label="Actions">
                              {canManageCerts && (
                                <Group gap="xs" wrap="nowrap">
                                  <Button
                                    size="xs"
                                    variant="default"
                                    disabled={!order.enabled || isRunning}
                                    loading={isRunning}
                                    onClick={() => void triggerOrder(order.id, false)}
                                  >
                                    Renew now
                                  </Button>
                                  <Button
                                    size="xs"
                                    variant="subtle"
                                    disabled={isRunning || (!order.enabled && order.challenge.type === 'http-01')}
                                    title={!order.enabled && order.challenge.type === 'http-01' ? 'Enable and apply this HTTP-01 order before running a staging test' : undefined}
                                    onClick={() => void triggerOrder(order.id, true)}
                                  >
                                    Test staging
                                  </Button>
                                  <Button size="xs" variant="default" onClick={() => setOrderModal({ mode: 'edit', item: saved })}>
                                    Edit
                                  </Button>
                                  <Button
                                    size="xs"
                                    variant="light"
                                    color="red"
                                    onClick={() => {
                                      setDeleteError('');
                                      setDeleteTarget({ kind: 'order', item: saved });
                                    }}
                                  >
                                    Delete
                                  </Button>
                                </Group>
                              )}
                            </Table.Td>
                          </Table.Tr>
                        );
                      })}
                    </Table.Tbody>
                  </Table>
                </ScrollArea>
              )}
            </Stack>
          </Stack>
        )}
      </Loaded>

      {accountModal && (
        <AcmeAccountModal
          api={api}
          existing={accountModal.mode === 'edit' ? accountModal.item : undefined}
          onClose={() => setAccountModal(null)}
          onSaved={(saved) => {
            setAccountModal(null);
            handleSaved(`Saved ACME account "${saved.value.name}".`);
          }}
          onSessionLost={onSessionLost}
        />
      )}

      {orderModal && (
        <AcmeOrderModal
          api={api}
          existing={orderModal.mode === 'edit' ? orderModal.item : undefined}
          accounts={accounts.map((a) => a.value)}
          segments={segments}
          onClose={() => setOrderModal(null)}
          onSaved={(saved) => {
            setOrderModal(null);
            handleSaved(`Saved ACME order "${saved.value.name}".`);
          }}
          onSessionLost={onSessionLost}
        />
      )}

      <Modal
        opened={deleteTarget !== null}
        onClose={() => { if (!deleting) setDeleteTarget(null); }}
        title={deleteTarget?.kind === 'account' ? 'Delete ACME account' : 'Delete ACME order'}
        centered
        lockScroll={false}
        closeButtonProps={{ 'aria-label': 'Close', disabled: deleting }}
      >
        <Stack>
          {deleteError && <Alert color="red" role="alert" title="Could not delete">{deleteError}</Alert>}
          <Text size="sm">
            Delete <strong>{deleteTarget?.item.value.name}</strong>?
            {deleteTarget?.kind === 'account'
              ? ' Accounts still referenced by an ACME order cannot be deleted.'
              : ' Previously issued certificates in System -> Certificates remain stored.'}
          </Text>
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={deleting} onClick={() => setDeleteTarget(null)}>Cancel</Button>
            <Button color="red" loading={deleting} onClick={() => void confirmDelete()}>Delete</Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}

interface AcmeAccountModalProps {
  api: ApiClient;
  existing?: SavedObject<AcmeAccount>;
  onClose(): void;
  onSaved(saved: SavedObject<AcmeAccount>): void;
  onSessionLost(): void;
}

function AcmeAccountModal({ api, existing, onClose, onSaved, onSessionLost }: AcmeAccountModalProps) {
  const initialUrl = existing?.value.directoryUrl ?? LETS_ENCRYPT_PROD;
  const initialPreset = initialUrl === LETS_ENCRYPT_PROD || initialUrl === LETS_ENCRYPT_STAGING ? initialUrl : 'custom';

  const [name, setName] = useState(existing?.value.name ?? '');
  const [preset, setPreset] = useState(initialPreset);
  const [customUrl, setCustomUrl] = useState(initialPreset === 'custom' ? initialUrl : '');
  const [email, setEmail] = useState(existing?.value.email ?? '');
  const [eabKeyId, setEabKeyId] = useState(existing?.value.eabKeyId ?? '');
  const [eabHmac, setEabHmac] = useState('');
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;
    const directoryUrl = preset === 'custom' ? customUrl.trim() : preset;
    const nextErrors = new Map<string, string>();
    if (!name.trim()) nextErrors.set('name', 'Enter an account name.');
    if (!directoryUrl.startsWith('https://')) nextErrors.set('directoryUrl', 'Enter an https:// ACME directory URL.');
    if (!email.trim() || !email.includes('@')) nextErrors.set('email', 'Enter a valid contact email.');
    if (eabKeyId.trim() && !eabHmac.trim() && !existing?.value.eabHmacRef?.isSet) {
      nextErrors.set('eabHmacRef', 'Enter the EAB HMAC key.');
    }
    if (nextErrors.size > 0) {
      setErrors(nextErrors);
      return;
    }

    submitting.current = true;
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    const eabWrite = secretWriteIfSet(eabHmac, existing?.value.eabHmacRef, 'acme_eab_hmac');
    const payload: AcmeAccountWrite = {
      name: name.trim(),
      directoryUrl,
      email: email.trim(),
      ...(eabKeyId.trim() ? { eabKeyId: eabKeyId.trim() } : {}),
      ...(eabWrite ? { eabHmacRef: eabWrite } : {}),
    };
    try {
      const saved = existing
        ? await api.replaceAcmeAccount(existing.value.id, payload, existing.version)
        : await api.createAcmeAccount(payload);
      onSaved(saved);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      const fields = fieldErrors(error, acmeFieldPattern);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'ACME account'));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <Modal
      opened
      onClose={() => { if (!busy) onClose(); }}
      title={existing ? 'Edit ACME account' : 'Add ACME account'}
      centered
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
    >
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert color="red" role="alert" title="ACME account was not saved">{failure}</Alert>}
          <TextInput
            label="Name"
            placeholder="Let's Encrypt Production"
            required
            disabled={busy}
            value={name}
            error={errors.get('name')}
            onChange={(e) => setName(e.currentTarget.value)}
          />
          <Select
            label="ACME directory"
            allowDeselect={false}
            disabled={busy}
            data={directoryPresets}
            value={preset}
            onChange={(val) => { if (val) setPreset(val); }}
          />
          {preset === 'custom' && (
            <TextInput
              label="Custom directory URL"
              placeholder="https://acme.example.com/directory"
              required
              disabled={busy}
              value={customUrl}
              error={errors.get('directoryUrl')}
              onChange={(e) => setCustomUrl(e.currentTarget.value)}
            />
          )}
          <TextInput
            label="Contact email"
            placeholder="admin@example.com"
            required
            disabled={busy}
            value={email}
            error={errors.get('email')}
            onChange={(e) => setEmail(e.currentTarget.value)}
          />
          <Divider label="External Account Binding (optional)" labelPosition="left" />
          <TextInput
            label="EAB Key ID (kid)"
            placeholder="Optional — required by ZeroSSL / Google Trust Services"
            disabled={busy}
            value={eabKeyId}
            error={errors.get('eabKeyId')}
            onChange={(e) => setEabKeyId(e.currentTarget.value)}
          />
          <PasswordInput
            label="EAB HMAC Key (base64url)"
            placeholder={existing?.value.eabHmacRef?.isSet ? 'Leave blank to keep stored HMAC key' : 'Optional write-only secret'}
            autoComplete="new-password"
            disabled={busy}
            value={eabHmac}
            error={errors.get('eabHmacRef')}
            onChange={(e) => setEabHmac(e.currentTarget.value)}
          />
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy}>{existing ? 'Save account' : 'Add account'}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

interface AcmeOrderModalProps {
  api: ApiClient;
  existing?: SavedObject<AcmeOrder>;
  accounts: AcmeAccount[];
  segments: Segment[];
  onClose(): void;
  onSaved(saved: SavedObject<AcmeOrder>): void;
  onSessionLost(): void;
}

function AcmeOrderModal({ api, existing, accounts, segments, onClose, onSaved, onSessionLost }: AcmeOrderModalProps) {
  const initChallenge = existing?.value.challenge;
  const initDns = initChallenge?.dns01;

  const [name, setName] = useState(existing?.value.name ?? '');
  const [accountId, setAccountId] = useState(existing?.value.accountId ?? accounts[0]?.id ?? '');
  const [domainsInput, setDomainsInput] = useState((existing?.value.domains ?? []).join('\n'));
  const [keyAlgorithm, setKeyAlgorithm] = useState<AcmeKeyAlgorithm>(existing?.value.keyAlgorithm ?? 'ecdsa-p256');
  const [renewBeforeDays, setRenewBeforeDays] = useState<number | string>(existing?.value.renewBeforeDays ?? 30);
  const [enabled, setEnabled] = useState(existing?.value.enabled ?? true);

  const [challengeType, setChallengeType] = useState<'http-01' | 'dns-01'>(initChallenge?.type ?? 'dns-01');
  const [listenSegments, setListenSegments] = useState<string[]>(initChallenge?.http01?.listenSegments ?? []);

  const [provider, setProvider] = useState<AcmeDnsProvider>(initDns?.provider ?? 'cloudflare');
  const [propagationTimeout, setPropagationTimeout] = useState<number | string>(initDns?.propagationTimeoutSeconds ?? 120);

  // Provider inputs
  const [apiToken, setApiToken] = useState('');
  const [porkbunApiKey, setPorkbunApiKey] = useState('');
  const [porkbunSecretKey, setPorkbunSecretKey] = useState('');
  const [acmeDnsServerUrl, setAcmeDnsServerUrl] = useState(initDns?.acmeDns?.serverUrl ?? 'https://auth.acme-dns.io');
  const [acmeDnsUsername, setAcmeDnsUsername] = useState(initDns?.acmeDns?.username ?? '');
  const [acmeDnsPassword, setAcmeDnsPassword] = useState('');
  const [acmeDnsSubdomain, setAcmeDnsSubdomain] = useState(initDns?.acmeDns?.subdomain ?? '');
  const [acmeDnsFullDomain, setAcmeDnsFullDomain] = useState(initDns?.acmeDns?.fullDomain ?? '');
  const [webhookUrl, setWebhookUrl] = useState(initDns?.webhook?.url ?? '');
  const [webhookAuth, setWebhookAuth] = useState('');
  const [webhookAllowPrivateIp, setWebhookAllowPrivateIp] = useState(Boolean(initDns?.webhook?.allowPrivateIp));

  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);

  const sameProvider = initDns?.provider === provider;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;

    const parsedDomains = parseDomains(domainsInput);
    const hasWildcard = parsedDomains.some((d) => d.startsWith('*.'));
    const nextErrors = new Map<string, string>();
    if (!name.trim()) nextErrors.set('name', 'Enter an order name.');
    if (!accountId) nextErrors.set('accountId', 'Select an ACME account.');
    if (parsedDomains.length === 0) nextErrors.set('domains', 'Enter at least one domain name.');
    if (challengeType === 'http-01' && hasWildcard) {
      nextErrors.set('domains', 'Wildcard domains require DNS-01 validation.');
    }
    if (challengeType === 'http-01' && listenSegments.length === 0) {
      nextErrors.set('challenge', 'Select at least one listening segment for HTTP-01.');
    }
    const renewDays = typeof renewBeforeDays === 'number' ? renewBeforeDays : Number(renewBeforeDays);
    if (!Number.isInteger(renewDays) || renewDays < 1 || renewDays > 60) {
      nextErrors.set('renewBeforeDays', 'Must be between 1 and 60 days.');
    }
    const propSecs = typeof propagationTimeout === 'number' ? propagationTimeout : Number(propagationTimeout);
    if (challengeType === 'dns-01' && (!Number.isInteger(propSecs) || propSecs < 10 || propSecs > 600)) {
      nextErrors.set('challenge', 'Propagation timeout must be between 10 and 600 seconds.');
    }
    if (nextErrors.size > 0) {
      setErrors(nextErrors);
      return;
    }

    let challenge: AcmeOrderWrite['challenge'];
    if (challengeType === 'http-01') {
      challenge = {
        type: 'http-01',
        http01: { listenSegments },
      };
    } else {
      const dns01: NonNullable<AcmeOrderWrite['challenge']['dns01']> = {
        provider,
        propagationTimeoutSeconds: propSecs,
      };
      if (provider === 'cloudflare' || provider === 'hetzner' || provider === 'digitalocean') {
        const prefix = provider === 'cloudflare' ? 'acme_cf_token' : provider === 'hetzner' ? 'acme_hetzner_token' : 'acme_do_token';
        const ref = secretWriteIfSet(apiToken, sameProvider ? initDns?.[provider]?.apiTokenRef : undefined, prefix);
        dns01[provider] = ref ? { apiTokenRef: ref } : {};
      } else if (provider === 'porkbun') {
        const kRef = secretWriteIfSet(porkbunApiKey, sameProvider ? initDns?.porkbun?.apiKeyRef : undefined, 'acme_porkbun_key');
        const sRef = secretWriteIfSet(porkbunSecretKey, sameProvider ? initDns?.porkbun?.secretKeyRef : undefined, 'acme_porkbun_secret');
        dns01.porkbun = {
          ...(kRef ? { apiKeyRef: kRef } : {}),
          ...(sRef ? { secretKeyRef: sRef } : {}),
        };
      } else if (provider === 'acme-dns') {
        const pRef = secretWriteIfSet(acmeDnsPassword, sameProvider ? initDns?.acmeDns?.passwordRef : undefined, 'acme_acmedns_pass');
        dns01.acmeDns = {
          serverUrl: acmeDnsServerUrl.trim(),
          username: acmeDnsUsername.trim(),
          subdomain: acmeDnsSubdomain.trim(),
          ...(acmeDnsFullDomain.trim() ? { fullDomain: acmeDnsFullDomain.trim() } : {}),
          ...(pRef ? { passwordRef: pRef } : {}),
        };
      } else if (provider === 'webhook') {
        const aRef = secretWriteIfSet(webhookAuth, sameProvider ? initDns?.webhook?.authorizationRef : undefined, 'acme_webhook_auth');
        dns01.webhook = {
          url: webhookUrl.trim(),
          ...(aRef ? { authorizationRef: aRef } : {}),
          ...(webhookAllowPrivateIp ? { allowPrivateIp: true } : {}),
        };
      }
      challenge = { type: 'dns-01', dns01 };
    }

    const payload: AcmeOrderWrite = {
      name: name.trim(),
      accountId,
      ...(existing?.value.certificateId ? { certificateId: existing.value.certificateId } : {}),
      domains: parsedDomains,
      keyAlgorithm,
      renewBeforeDays: renewDays,
      enabled,
      challenge,
    };

    submitting.current = true;
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      const saved = existing
        ? await api.replaceAcmeOrder(existing.value.id, payload, existing.version)
        : await api.createAcmeOrder(payload);
      onSaved(saved);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      const fields = fieldErrors(error, acmeFieldPattern);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'ACME order'));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <Modal
      opened
      onClose={() => { if (!busy) onClose(); }}
      title={existing ? 'Edit ACME order' : 'Add ACME order'}
      size="lg"
      centered
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
    >
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert color="red" role="alert" title="ACME order was not saved">{failure}</Alert>}
          <TextInput
            label="Name"
            placeholder="Edge HTTPS Certificate"
            required
            disabled={busy}
            value={name}
            error={errors.get('name')}
            onChange={(e) => setName(e.currentTarget.value)}
          />
          <Select
            label="ACME account"
            required
            searchable
            allowDeselect={false}
            disabled={busy}
            data={accounts.map((a) => ({ value: a.id, label: `${a.name} (${directoryLabel(a.directoryUrl)})` }))}
            value={accountId || null}
            error={errors.get('accountId')}
            onChange={(val) => setAccountId(val ?? '')}
          />
          <Textarea
            label="Domains"
            description="DNS names separated by commas or new lines. Wildcards (*.example.com) require DNS-01."
            placeholder={'fw.example.com\n*.example.com'}
            required
            autosize
            minRows={2}
            maxRows={5}
            disabled={busy}
            value={domainsInput}
            error={errors.get('domains')}
            onChange={(e) => setDomainsInput(e.currentTarget.value)}
          />
          <Group grow>
            <Select
              label="Key algorithm"
              allowDeselect={false}
              disabled={busy}
              data={acmeAlgorithms}
              value={keyAlgorithm}
              onChange={(val) => { if (val) setKeyAlgorithm(val as AcmeKeyAlgorithm); }}
            />
            <NumberInput
              label="Renew before expiry (days)"
              required
              disabled={busy}
              allowDecimal={false}
              min={1}
              max={60}
              value={renewBeforeDays}
              error={errors.get('renewBeforeDays')}
              onChange={setRenewBeforeDays}
            />
          </Group>
          <Switch
            label="Enable automatic issuance and renewal"
            checked={enabled}
            disabled={busy}
            onChange={(e) => setEnabled(e.currentTarget.checked)}
          />

          <Divider label="Challenge validation" labelPosition="left" />
          <SegmentedControl
            disabled={busy}
            value={challengeType}
            onChange={(val) => setChallengeType(val as 'http-01' | 'dns-01')}
            data={[
              { value: 'dns-01', label: 'DNS-01 (supports wildcards)' },
              { value: 'http-01', label: 'HTTP-01 (port 80)' },
            ]}
          />
          {errors.get('challenge') && (
            <Alert color="red" role="alert">{errors.get('challenge')}</Alert>
          )}

          {challengeType === 'http-01' ? (
            <Stack gap="xs">
              <MultiSelect
                label="Listening segments"
                description="Port 80 is redirected to the temporary ACME responder ONLY while a challenge is actively running."
                placeholder="Select WAN or ingress segments"
                required
                searchable
                disabled={busy}
                data={segments.map((s) => ({ value: s.id, label: s.name }))}
                value={listenSegments}
                onChange={setListenSegments}
              />
              <Alert color="blue" role="status">
                Enabling HTTP-01 or changing listening segments updates the firewall ruleset and requires Apply before issuing.
              </Alert>
            </Stack>
          ) : (
            <Stack gap="sm">
              <Group grow>
                <Select
                  label="DNS provider"
                  allowDeselect={false}
                  disabled={busy}
                  data={dnsProviders}
                  value={provider}
                  onChange={(val) => { if (val) setProvider(val as AcmeDnsProvider); }}
                />
                <NumberInput
                  label="Propagation timeout (seconds)"
                  disabled={busy}
                  allowDecimal={false}
                  min={10}
                  max={600}
                  value={propagationTimeout}
                  onChange={setPropagationTimeout}
                />
              </Group>

              {(provider === 'cloudflare' || provider === 'hetzner' || provider === 'digitalocean') && (
                <PasswordInput
                  label="API Token"
                  required={!sameProvider}
                  placeholder={sameProvider ? 'Leave blank to keep stored API token' : 'Enter write-only API token'}
                  autoComplete="new-password"
                  disabled={busy}
                  value={apiToken}
                  onChange={(e) => setApiToken(e.currentTarget.value)}
                />
              )}

              {provider === 'porkbun' && (
                <>
                  <PasswordInput
                    label="Porkbun API Key"
                    required={!sameProvider}
                    placeholder={sameProvider ? 'Leave blank to keep stored API key' : 'Enter write-only API key'}
                    autoComplete="new-password"
                    disabled={busy}
                    value={porkbunApiKey}
                    onChange={(e) => setPorkbunApiKey(e.currentTarget.value)}
                  />
                  <PasswordInput
                    label="Porkbun Secret API Key"
                    required={!sameProvider}
                    placeholder={sameProvider ? 'Leave blank to keep stored secret key' : 'Enter write-only secret key'}
                    autoComplete="new-password"
                    disabled={busy}
                    value={porkbunSecretKey}
                    onChange={(e) => setPorkbunSecretKey(e.currentTarget.value)}
                  />
                </>
              )}

              {provider === 'acme-dns' && (
                <>
                  <TextInput
                    label="acme-dns Server URL"
                    required
                    disabled={busy}
                    value={acmeDnsServerUrl}
                    onChange={(e) => setAcmeDnsServerUrl(e.currentTarget.value)}
                  />
                  <Group grow>
                    <TextInput
                      label="Username"
                      required
                      disabled={busy}
                      value={acmeDnsUsername}
                      onChange={(e) => setAcmeDnsUsername(e.currentTarget.value)}
                    />
                    <TextInput
                      label="Subdomain"
                      required
                      disabled={busy}
                      value={acmeDnsSubdomain}
                      onChange={(e) => setAcmeDnsSubdomain(e.currentTarget.value)}
                    />
                  </Group>
                  <PasswordInput
                    label="Password / API Key"
                    required={!sameProvider}
                    placeholder={sameProvider ? 'Leave blank to keep stored password' : 'Enter write-only password'}
                    autoComplete="new-password"
                    disabled={busy}
                    value={acmeDnsPassword}
                    onChange={(e) => setAcmeDnsPassword(e.currentTarget.value)}
                  />
                  <TextInput
                    label="Full delegated domain (optional)"
                    placeholder="Optional — used for propagation lookup"
                    disabled={busy}
                    value={acmeDnsFullDomain}
                    onChange={(e) => setAcmeDnsFullDomain(e.currentTarget.value)}
                  />
                </>
              )}

              {provider === 'webhook' && (
                <>
                  <TextInput
                    label="Webhook URL"
                    placeholder="https://dns-bridge.example.com/acme"
                    required
                    disabled={busy}
                    value={webhookUrl}
                    onChange={(e) => setWebhookUrl(e.currentTarget.value)}
                  />
                  <PasswordInput
                    label="Authorization header (optional)"
                    placeholder={initDns?.webhook?.authorizationRef?.isSet ? 'Leave blank to keep stored header' : 'Bearer ...'}
                    autoComplete="new-password"
                    disabled={busy}
                    value={webhookAuth}
                    onChange={(e) => setWebhookAuth(e.currentTarget.value)}
                  />
                  <Switch
                    label="Allow private / LAN IP targets"
                    description="Disabled by default to prevent SSRF; enable only for a trusted internal DNS bridge."
                    checked={webhookAllowPrivateIp}
                    disabled={busy}
                    onChange={(e) => setWebhookAllowPrivateIp(e.currentTarget.checked)}
                  />
                </>
              )}
            </Stack>
          )}

          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy}>{existing ? 'Save order' : 'Add order'}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
