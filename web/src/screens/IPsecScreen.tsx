import {
  Alert,
  Badge,
  Button,
  Card,
  Fieldset,
  Group,
  Modal,
  NumberInput,
  PasswordInput,
  ScrollArea,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  Textarea,
  TextInput,
  Title,
} from '@mantine/core';
import { useCallback, useMemo, useRef, useState, type FormEvent } from 'react';
import { ApiClient, ApiError, fieldErrors, reportError, saveFailure } from '../api';
import { bytes, duration, plural } from '../format';
import type {
  IPsecConnection,
  IPsecConnectionWrite,
  IPsecEapUser,
  IPsecEapUserWrite,
  IPsecIkeSAStatus,
  IPsecLocalAuthType,
  IPsecMode,
  IPsecRemoteAuthType,
  IPsecStartAction,
  IPsecStatusItem,
  PKICA,
  PKICertificate,
  SavedObject,
  SecretView,
  SecretWrite,
  Segment,
  Session,
  Zone,
} from '../types';
import { useResource, useVisiblePoll } from '../useResource';
import { Loaded } from './Loaded';

interface IPsecScreenProps {
  api: ApiClient;
  session: Session;
  onSaved(): void;
  onSessionLost(): void;
}

interface LoadedIPsecConfig {
  connections: Array<SavedObject<IPsecConnection>>;
  zones: Zone[];
  segments: Segment[];
  cas: PKICA[];
  certificates: PKICertificate[];
}

const STATUS_POLL_MS = 15_000;
const ipsecIfnamePattern = /^ipsec([0-9]|[1-9][0-9])$/;
const connFieldPattern =
  /^\/?(name|description|mode|ifname|xfrmIfId|zone|tunnelAddresses|localSegment|localAddress|remoteAddress|proposals|espProposals|localSubnets|remoteSubnets|virtualPool|dpdDelaySeconds|startAction|auth\/localType|auth\/remoteType|auth\/localId|auth\/remoteId|auth\/pskRef|auth\/localCertId|auth\/caId|auth\/eapUsers|auth)(?:\/|$)/;

const modeLabels: Record<IPsecMode, string> = {
  'route-based': 'Route-based (XFRM)',
  'policy-based': 'Policy-based',
  'road-warrior': 'Road-warrior',
};

const modeColors: Record<IPsecMode, string> = {
  'route-based': 'blue',
  'policy-based': 'violet',
  'road-warrior': 'teal',
};

const startActionLabels: Record<IPsecStartAction, string> = {
  trap: 'On demand (trap)',
  start: 'Initiate on start',
  none: 'Responder only (none)',
};

function parseList(raw: string): string[] {
  return raw
    .split(/[\s,]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function nextIfname(connections: Array<SavedObject<IPsecConnection>>): string {
  const used = new Set(connections.map((item) => item.value.ifname).filter(Boolean));
  for (let i = 0; i <= 99; i++) {
    const candidate = `ipsec${i}`;
    if (!used.has(candidate)) return candidate;
  }
  return 'ipsec0';
}

function nextXfrmIfId(connections: Array<SavedObject<IPsecConnection>>): number {
  const used = new Set(
    connections
      .map((item) => item.value.xfrmIfId)
      .filter((id): id is number => id !== undefined && id > 0),
  );
  for (let id = 1; id <= 65535; id++) {
    if (!used.has(id)) return id;
  }
  return 1;
}

function newSecretName(prefix: string): string {
  const random = new Uint8Array(6);
  globalThis.crypto.getRandomValues(random);
  const suffix = Array.from(random, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${prefix}_${suffix}`;
}

function buildSecretWrite(
  typed: string,
  existing: SecretView | undefined,
  prefix: string,
): SecretWrite | Record<string, never> {
  if (typed !== '') {
    return {
      secret: existing?.secret || newSecretName(prefix),
      set: typed,
      expectedVersion: existing?.version || '0',
    };
  }
  return {};
}

function saStateColor(state: string): string {
  const upper = state.toUpperCase();
  if (upper === 'ESTABLISHED' || upper === 'INSTALLED') return 'green';
  if (upper === 'CONNECTING' || upper === 'REKEYING' || upper === 'INSTALLING') return 'blue';
  if (upper === 'DELETING' || upper === 'DESTROYING') return 'orange';
  return 'gray';
}

function hasUnresolvedSecrets(conn: IPsecConnection, certsById: Map<string, PKICertificate>): boolean {
  if (conn.auth.pskRef?.unresolved) return true;
  if (conn.auth.localCertId && certsById.get(conn.auth.localCertId)?.keyRef?.unresolved) return true;
  return (conn.auth.eapUsers ?? []).some((u) => u.passwordRef?.unresolved);
}

function formatDurationSec(seconds: number | undefined): string {
  if (seconds === undefined || seconds < 0) return '—';
  if (seconds < 60) return `${Math.round(seconds)}s`;
  return duration(seconds);
}

function IPsecSATable({ ikeSAs }: { ikeSAs: IPsecIkeSAStatus[] }) {
  if (ikeSAs.length === 0) {
    return (
      <Text size="xs" c="dimmed">
        No active IKE or Child security associations.
      </Text>
    );
  }

  return (
    <ScrollArea>
      <Table className="data-table" striped highlightOnHover>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>IKE SA</Table.Th>
            <Table.Th>Endpoints</Table.Th>
            <Table.Th>Established</Table.Th>
            <Table.Th>Child SAs & Traffic</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {ikeSAs.map((ike) => {
            const children = ike.childSAs ?? [];
            return (
              <Table.Tr key={ike.uniqueId}>
                <Table.Td data-label="IKE SA">
                  <Group gap="xs" wrap="nowrap">
                    <Text size="xs" ff="monospace">#{ike.uniqueId}</Text>
                    <Badge size="xs" color={saStateColor(ike.state)} variant="light">
                      {ike.state}
                    </Badge>
                  </Group>
                </Table.Td>
                <Table.Td data-label="Endpoints">
                  <Text size="xs" ff="monospace">
                    {ike.localHost || '—'} ↔ {ike.remoteHost || '—'}
                  </Text>
                </Table.Td>
                <Table.Td data-label="Established">
                  <Text size="xs" className="tabular">
                    {formatDurationSec(ike.establishedSec)}
                  </Text>
                </Table.Td>
                <Table.Td data-label="Child SAs & Traffic">
                  {children.length === 0 ? (
                    <Text size="xs" c="dimmed">No Child SAs</Text>
                  ) : (
                    <Stack gap={4}>
                      {children.map((child, idx) => (
                        <Group key={`${child.name}-${idx}`} gap="xs" wrap="wrap">
                          <Text size="xs" fw={600}>{child.name}</Text>
                          <Badge size="xs" color={saStateColor(child.state)} variant="light">
                            {child.state}
                          </Badge>
                          <Text size="xs" ff="monospace" c="dimmed">
                            {(child.localTs ?? []).join(', ') || '—'} ↔ {(child.remoteTs ?? []).join(', ') || '—'}
                          </Text>
                          <Text size="xs" className="tabular">
                            RX {bytes(child.bytesIn)} / TX {bytes(child.bytesOut)}
                          </Text>
                          {child.rekeySec !== undefined && (
                            <Text size="xs" c="dimmed" className="tabular">
                              Rekey in {formatDurationSec(child.rekeySec)}
                            </Text>
                          )}
                        </Group>
                      ))}
                    </Stack>
                  )}
                </Table.Td>
              </Table.Tr>
            );
          })}
        </Table.Tbody>
      </Table>
    </ScrollArea>
  );
}

export function IPsecScreen({ api, session, onSaved, onSessionLost }: IPsecScreenProps) {
  const canReadConfig = session.capabilities.includes('configuration.read');
  const canWrite = session.capabilities.includes('configuration.write');
  const canReadStatus = session.capabilities.includes('vpn.read');
  const canControl = session.capabilities.includes('vpn.control');

  const [notice, setNotice] = useState('');
  const [actionError, setActionError] = useState('');
  const [actionBusy, setActionBusy] = useState<string | null>(null);
  const [editor, setEditor] = useState<SavedObject<IPsecConnection> | 'new' | undefined>(undefined);
  const [deleteTarget, setDeleteTarget] = useState<SavedObject<IPsecConnection> | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState('');
  const [deleteStale, setDeleteStale] = useState(false);

  const loadConfig = useCallback(async (): Promise<LoadedIPsecConfig | undefined> => {
    if (!canReadConfig) return undefined;
    const [ipsecRes, zonesRes, segmentsRes, casRes, certsRes] = await Promise.all([
      api.ipsecConnections(),
      api.zones(),
      api.segments(),
      api.pkiCAs(),
      api.pkiCertificates(),
    ]);
    return {
      connections: ipsecRes.items,
      zones: zonesRes.items.map((item) => item.value),
      segments: segmentsRes.items.map((item) => item.value),
      cas: casRes.items.map((item) => item.value),
      certificates: certsRes.items.map((item) => item.value),
    };
  }, [api, canReadConfig]);

  const loadStatus = useCallback(async () => {
    if (!canReadStatus) return undefined;
    return api.ipsecStatus();
  }, [api, canReadStatus]);

  const configResource = useResource(loadConfig, onSessionLost);
  const statusResource = useResource(loadStatus, onSessionLost);
  useVisiblePoll(statusResource.refresh, STATUS_POLL_MS, canReadStatus);

  const statusById = useMemo(() => {
    const map = new Map<string, IPsecStatusItem>();
    for (const item of statusResource.data?.items ?? []) {
      map.set(item.id, item);
    }
    return map;
  }, [statusResource.data]);

  function reloadAll() {
    if (canReadConfig) void configResource.reload();
    if (canReadStatus) void statusResource.reload();
  }

  function handleSaved(message: string) {
    setNotice(message);
    setActionError('');
    onSaved();
    reloadAll();
  }

  async function runControl(connId: string, connName: string, action: 'initiate' | 'terminate') {
    if (actionBusy !== null) return;
    setActionBusy(`${action}:${connId}`);
    setActionError('');
    try {
      if (action === 'initiate') {
        await api.initiateIpsec(connId);
        setNotice(`Initiated IPsec connection "${connName}".`);
      } else {
        await api.terminateIpsec(connId);
        setNotice(`Terminated IPsec connection "${connName}".`);
      }
      if (canReadStatus) void statusResource.reload();
    } catch (error) {
      reportError(error, onSessionLost, setActionError);
    } finally {
      setActionBusy(null);
    }
  }

  async function confirmDelete() {
    if (!deleteTarget || deleting) return;
    setDeleting(true);
    setDeleteError('');
    try {
      await api.deleteIpsecConnection(deleteTarget.value.id, deleteTarget.version);
      const deletedName = deleteTarget.value.name;
      setDeleteTarget(null);
      setDeleteStale(false);
      handleSaved(`Deleted IPsec connection "${deletedName}".`);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (error instanceof ApiError && (error.status === 404 || error.status === 412)) {
        setDeleteStale(true);
        void configResource.reload();
      }
      setDeleteError(saveFailure(error, new Map(), 'IPsec connection'));
    } finally {
      setDeleting(false);
    }
  }

  const loadedConnections = configResource.data?.connections ?? [];
  const loadedZones = configResource.data?.zones ?? [];
  const loadedSegments = configResource.data?.segments ?? [];
  const loadedCAs = configResource.data?.cas ?? [];
  const loadedCerts = configResource.data?.certificates ?? [];

  const serviceActive = statusResource.data?.serviceActive ?? false;

  return (
    <Stack gap="lg">
      <Group justify="space-between" align="flex-end" wrap="wrap">
        <div>
          <Group gap="xs" align="center">
            <Title order={2}>IPsec</Title>
            {canReadStatus && (
              statusResource.data === undefined ? (
                statusResource.loading ? (
                  <Badge size="sm" variant="light" color="gray">Loading…</Badge>
                ) : (
                  <Badge size="sm" variant="light" color="orange">Status unavailable</Badge>
                )
              ) : (
                <>
                  <Badge size="sm" variant="light" color={serviceActive ? 'green' : 'gray'}>
                    {serviceActive ? 'strongSwan Active' : 'strongSwan Stopped'}
                  </Badge>
                  {statusResource.error && (
                    <Badge size="sm" variant="light" color="orange">Stale</Badge>
                  )}
                </>
              )
            )}
          </Group>
          <Text size="sm" c="dimmed">
            Configure IKEv2 route-based (XFRM), policy-based, and road-warrior IPsec VPN connections.
          </Text>
        </div>
        <Group gap="xs">
          <Button
            size="sm"
            variant="default"
            loading={(configResource.loading && configResource.data !== undefined) || (statusResource.loading && statusResource.data !== undefined)}
            onClick={reloadAll}
          >
            Refresh
          </Button>
          {canWrite && canReadConfig && (
            <Button size="sm" disabled={configResource.data === undefined || configResource.loading} onClick={() => setEditor('new')}>
              Add connection
            </Button>
          )}
        </Group>
      </Group>

      {notice && (
        <Alert color="green" role="status" withCloseButton onClose={() => setNotice('')}>
          {notice}
        </Alert>
      )}

      {actionError && (
        <Alert color="red" role="alert" title="IPsec control failed" withCloseButton onClose={() => setActionError('')}>
          {actionError}
        </Alert>
      )}

      {canReadStatus && statusResource.error && (
        <Alert color="orange" role="alert" title="Live IPsec status unavailable">
          {statusResource.error}
        </Alert>
      )}

      {canReadConfig ? (
        <Loaded resource={configResource} what="IPsec connections">
          {(loaded) => {
            if (!loaded) return null;
            const { connections, zones, segments, cas, certificates } = loaded;
            const zonesById = new Map(zones.map((z) => [z.id, z.name]));
            const segmentsById = new Map(segments.map((s) => [s.id, s.name]));
            const casById = new Map(cas.map((c) => [c.id, c.name]));
            const certsById = new Map(certificates.map((c) => [c.id, c]));
            const sorted = [...connections].sort((a, b) =>
              a.value.name.localeCompare(b.value.name, undefined, { sensitivity: 'base' })
              || a.value.id.localeCompare(b.value.id));

            if (sorted.length === 0) {
              return (
                <Alert role="status" title="No IPsec connections configured">
                  {canWrite
                    ? 'Add an IPsec connection to establish route-based XFRM, policy-based, or road-warrior IKEv2 tunnels.'
                    : 'No IPsec connections are currently configured.'}
                </Alert>
              );
            }

            return (
              <Stack gap="md">
                {sorted.map((saved) => {
                  const conn = saved.value;
                  const live = statusById.get(conn.id);
                  const ikeSAs = live?.ikeSAs ?? [];
                  const establishedCount = ikeSAs.filter((sa) => sa.state.toUpperCase() === 'ESTABLISHED').length;
                  const applied = live !== undefined && live.applied !== false;
                  const unresolved = hasUnresolvedSecrets(conn, certsById);
                  const canControlConn = canControl && conn.enabled && (!canReadStatus || (serviceActive && applied));
                  const localAuthDesc = conn.auth.localType === 'cert'
                    ? `Cert (${conn.auth.localCertId ? (certsById.get(conn.auth.localCertId)?.name ?? conn.auth.localCertId) : 'unset'})`
                    : 'PSK';
                  const remoteAuthDesc = conn.auth.remoteType === 'cert'
                    ? `Cert (CA: ${conn.auth.caId ? (casById.get(conn.auth.caId) ?? conn.auth.caId) : 'unset'})`
                    : conn.auth.remoteType === 'eap-mschapv2'
                      ? `EAP-MSCHAPv2 (${plural((conn.auth.eapUsers ?? []).length, 'user')})`
                      : 'PSK';
                  const localSegName = conn.localSegment ? (segmentsById.get(conn.localSegment) ?? conn.localSegment) : '';
                  const localEndpoint = localSegName && conn.localAddress
                    ? `${localSegName} (${conn.localAddress})`
                    : localSegName || conn.localAddress || 'Any';

                  return (
                    <Card key={conn.id} withBorder padding="md">
                      <Stack gap="sm">
                        <Group justify="space-between" align="flex-start" wrap="wrap">
                          <Stack gap={4}>
                            <Group gap="xs" wrap="wrap">
                              <Text fw={600} size="md">{conn.name}</Text>
                              <Badge size="xs" color={modeColors[conn.mode]} variant="light">
                                {modeLabels[conn.mode]}
                              </Badge>
                              <Badge size="xs" color={conn.enabled ? 'green' : 'gray'} variant="light">
                                {conn.enabled ? 'Enabled' : 'Disabled'}
                              </Badge>
                              {conn.ifname && (
                                <Badge size="xs" variant="outline" ff="monospace">
                                  {conn.ifname}{conn.xfrmIfId !== undefined ? ` (xfrm ${conn.xfrmIfId})` : ''}
                                </Badge>
                              )}
                              {canReadStatus && conn.enabled && (
                                statusResource.data === undefined ? (
                                  <Badge size="xs" color={statusResource.loading ? 'gray' : 'orange'} variant="light">
                                    {statusResource.loading ? 'Loading…' : 'Status unavailable'}
                                  </Badge>
                                ) : !serviceActive ? (
                                  <Badge size="xs" color="gray" variant="light">Service stopped</Badge>
                                ) : !applied ? (
                                  <Badge size="xs" color="yellow" variant="light">Not applied</Badge>
                                ) : establishedCount > 0 ? (
                                  <Badge size="xs" color="green" variant="light">
                                    {plural(establishedCount, 'SA')} established
                                  </Badge>
                                ) : (
                                  <Badge size="xs" color="gray" variant="light">Idle</Badge>
                                )
                              )}
                              {unresolved && (
                                <Badge size="xs" color="orange" variant="light">
                                  Unresolved secret
                                </Badge>
                              )}
                            </Group>
                            {conn.description && (
                              <Text size="xs" c="dimmed">{conn.description}</Text>
                            )}
                          </Stack>

                          <Group gap="xs" wrap="wrap">
                            {canControl && conn.mode !== 'road-warrior' && (
                              <Button
                                size="xs"
                                variant="light"
                                disabled={!canControlConn || actionBusy !== null}
                                loading={actionBusy === `initiate:${conn.id}`}
                                aria-label={`Initiate ${conn.name}`}
                                onClick={() => void runControl(conn.id, conn.name, 'initiate')}
                              >
                                Initiate
                              </Button>
                            )}
                            {canControl && (
                              <Button
                                size="xs"
                                variant="default"
                                disabled={!canControlConn || actionBusy !== null}
                                loading={actionBusy === `terminate:${conn.id}`}
                                aria-label={`Terminate ${conn.name}`}
                                onClick={() => void runControl(conn.id, conn.name, 'terminate')}
                              >
                                Terminate
                              </Button>
                            )}
                            {canWrite && (
                              <>
                                <Button
                                  size="xs"
                                  variant="default"
                                  aria-label={`Edit IPsec connection ${conn.name}`}
                                  onClick={() => setEditor(saved)}
                                >
                                  Edit
                                </Button>
                                <Button
                                  size="xs"
                                  variant="light"
                                  color="red"
                                  aria-label={`Delete IPsec connection ${conn.name}`}
                                  onClick={() => {
                                    setDeleteError('');
                                    setDeleteStale(false);
                                    setDeleteTarget(saved);
                                  }}
                                >
                                  Delete
                                </Button>
                              </>
                            )}
                          </Group>
                        </Group>

                        <Group gap="md" wrap="wrap">
                          <Text size="xs" c="dimmed">
                            Zone: <Text span c="var(--mantine-color-text)">{conn.zone ? (zonesById.get(conn.zone) ?? conn.zone) : 'None'}</Text>
                          </Text>
                          <Text size="xs" c="dimmed">
                            Local: <Text span ff="monospace" c="var(--mantine-color-text)">{localEndpoint}</Text>
                          </Text>
                          <Text size="xs" c="dimmed">
                            Remote: <Text span ff="monospace" c="var(--mantine-color-text)">{conn.remoteAddress || '%any'}</Text>
                          </Text>
                          <Text size="xs" c="dimmed">
                            Auth: <Text span c="var(--mantine-color-text)">{localAuthDesc} / {remoteAuthDesc}</Text>
                          </Text>
                          <Text size="xs" c="dimmed">
                            Start: <Text span c="var(--mantine-color-text)">{startActionLabels[conn.startAction ?? 'trap']}</Text>
                          </Text>
                          <Text size="xs" c="dimmed">
                            DPD: <Text span c="var(--mantine-color-text)">{conn.dpdDelaySeconds ?? 30}s</Text>
                          </Text>
                          <Text size="xs" c="dimmed">
                            NAT-T: <Text span c="var(--mantine-color-text)">{(conn.natTraversal ?? true) ? 'On' : 'Off'}</Text>
                          </Text>
                        </Group>

                        {(conn.mode === 'route-based' || conn.mode === 'road-warrior') && (conn.tunnelAddresses ?? []).length > 0 && (
                          <Text size="xs" c="dimmed">
                            Tunnel addresses: <Text span ff="monospace" c="var(--mantine-color-text)">{(conn.tunnelAddresses ?? []).join(', ')}</Text>
                          </Text>
                        )}
                        {(conn.mode === 'policy-based' || ((conn.localSubnets ?? []).length > 0 || (conn.remoteSubnets ?? []).length > 0)) && (
                          <Text size="xs" c="dimmed">
                            Subnets: <Text span ff="monospace" c="var(--mantine-color-text)">
                              {(conn.localSubnets ?? []).join(', ') || '0.0.0.0/0, ::/0'} ↔ {conn.mode === 'road-warrior' ? 'dynamic' : ((conn.remoteSubnets ?? []).join(', ') || '0.0.0.0/0, ::/0')}
                            </Text>
                          </Text>
                        )}
                        {conn.mode === 'road-warrior' && (
                          <Text size="xs" c="dimmed">
                            Virtual pool: <Text span ff="monospace" c="var(--mantine-color-text)">{(conn.virtualPool ?? []).join(', ') || '—'}</Text>
                          </Text>
                        )}

                        {canReadStatus && statusResource.data !== undefined && (
                          <IPsecSATable ikeSAs={ikeSAs} />
                        )}
                      </Stack>
                    </Card>
                  );
                })}
              </Stack>
            );
          }}
        </Loaded>
      ) : canReadStatus ? (
        <Loaded resource={statusResource} what="IPsec status">
          {(status) => {
            if (!status) return null;
            if (status.items.length === 0) {
              return (
                <Alert role="status" title="No active IPsec connections">
                  {status.serviceActive
                    ? 'strongSwan is active with no applied IPsec connections.'
                    : 'strongSwan is currently stopped.'}
                </Alert>
              );
            }
            return (
              <Stack gap="md">
                {status.items.map((item) => {
                  const ikeSAs = item.ikeSAs ?? [];
                  const establishedCount = ikeSAs.filter((sa) => sa.state.toUpperCase() === 'ESTABLISHED').length;
                  return (
                    <Card key={item.id} withBorder padding="md">
                      <Stack gap="sm">
                        <Group justify="space-between" align="center" wrap="wrap">
                          <Group gap="xs">
                            <Text fw={600} size="md">{item.name || item.id}</Text>
                            {item.mode && (
                              <Badge size="xs" color={modeColors[item.mode]} variant="light">
                                {modeLabels[item.mode]}
                              </Badge>
                            )}
                            {item.applied === false ? (
                              <Badge size="xs" color="yellow" variant="light">Not applied</Badge>
                            ) : establishedCount > 0 ? (
                              <Badge size="xs" color="green" variant="light">
                                {plural(establishedCount, 'SA')} established
                              </Badge>
                            ) : (
                              <Badge size="xs" color="gray" variant="light">Idle</Badge>
                            )}
                          </Group>
                          {canControl && (
                            <Group gap="xs">
                              {item.mode !== 'road-warrior' && (
                                <Button
                                  size="xs"
                                  variant="light"
                                  disabled={!status.serviceActive || item.applied === false || actionBusy !== null}
                                  loading={actionBusy === `initiate:${item.id}`}
                                  aria-label={`Initiate ${item.name || item.id}`}
                                  onClick={() => void runControl(item.id, item.name || item.id, 'initiate')}
                                >
                                  Initiate
                                </Button>
                              )}
                              <Button
                                size="xs"
                                variant="default"
                                disabled={!status.serviceActive || item.applied === false || actionBusy !== null}
                                loading={actionBusy === `terminate:${item.id}`}
                                aria-label={`Terminate ${item.name || item.id}`}
                                onClick={() => void runControl(item.id, item.name || item.id, 'terminate')}
                              >
                                Terminate
                              </Button>
                            </Group>
                          )}
                        </Group>
                        <IPsecSATable ikeSAs={ikeSAs} />
                      </Stack>
                    </Card>
                  );
                })}
              </Stack>
            );
          }}
        </Loaded>
      ) : (
        <Alert color="orange" role="alert" title="Access restricted">
          Your account does not have permission to view IPsec configuration or status.
        </Alert>
      )}

      {editor !== undefined && (
        <IPsecModal
          api={api}
          existing={editor === 'new' ? undefined : editor}
          allConnections={loadedConnections}
          zones={loadedZones}
          segments={loadedSegments}
          cas={loadedCAs}
          certificates={loadedCerts}
          onClose={() => setEditor(undefined)}
          onSaved={(saved) => {
            setEditor(undefined);
            handleSaved(`Saved IPsec connection "${saved.value.name}".`);
          }}
          onConflict={() => void configResource.reload()}
          onSessionLost={onSessionLost}
        />
      )}

      <Modal
        opened={deleteTarget !== null}
        onClose={() => { if (!deleting) { setDeleteTarget(null); setDeleteStale(false); } }}
        title="Delete IPsec connection"
        centered
        lockScroll={false}
        closeButtonProps={{ 'aria-label': 'Close', disabled: deleting }}
      >
        <Stack>
          {deleteError && <Alert color="red" role="alert" title="Could not delete IPsec connection">{deleteError}</Alert>}
          <Text size="sm">
            Delete IPsec connection <strong>{deleteTarget?.value.name}</strong>?
            {deleteTarget?.value.ifname ? ` Its XFRM interface (${deleteTarget.value.ifname}) will be removed on apply.` : ''}
          </Text>
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={deleting} onClick={() => { setDeleteTarget(null); setDeleteStale(false); }}>
              {deleteStale ? 'Close' : 'Cancel'}
            </Button>
            <Button color="red" loading={deleting} disabled={deleteStale} onClick={() => void confirmDelete()}>
              Delete
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}

interface EapUserDraft {
  key: string;
  username: string;
  password: string;
  existing?: IPsecEapUser;
}

interface IPsecModalProps {
  api: ApiClient;
  existing?: SavedObject<IPsecConnection>;
  allConnections: Array<SavedObject<IPsecConnection>>;
  zones: Zone[];
  segments: Segment[];
  cas: PKICA[];
  certificates: PKICertificate[];
  onClose(): void;
  onSaved(saved: SavedObject<IPsecConnection>): void;
  onConflict(): void;
  onSessionLost(): void;
}

function IPsecModal({
  api,
  existing,
  allConnections,
  zones,
  segments,
  cas,
  certificates,
  onClose,
  onSaved,
  onConflict,
  onSessionLost,
}: IPsecModalProps) {
  const initial = existing?.value;
  const sortedZones = [...zones].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
  const sortedSegments = [...segments].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
  const sortedCAs = [...cas].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
  const sortedCerts = [...certificates].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));

  const [name, setName] = useState(initial?.name ?? '');
  const [description, setDescription] = useState(initial?.description ?? '');
  const [mode, setMode] = useState<IPsecMode>(initial?.mode ?? 'route-based');
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [ifname, setIfname] = useState(initial?.ifname ?? nextIfname(allConnections));
  const [xfrmIfId, setXfrmIfId] = useState<number | string>(initial?.xfrmIfId ?? nextXfrmIfId(allConnections));
  const [zone, setZone] = useState(initial?.zone ?? sortedZones[0]?.id ?? '');
  const [tunnelAddresses, setTunnelAddresses] = useState((initial?.tunnelAddresses ?? []).join('\n'));
  const [localSegment, setLocalSegment] = useState(initial?.localSegment ?? '');
  const [localAddress, setLocalAddress] = useState(initial?.localAddress ?? '');
  const [remoteAddress, setRemoteAddress] = useState(initial?.remoteAddress ?? '');
  const [localSubnets, setLocalSubnets] = useState((initial?.localSubnets ?? []).join('\n'));
  const [remoteSubnets, setRemoteSubnets] = useState((initial?.remoteSubnets ?? []).join('\n'));
  const [virtualPool, setVirtualPool] = useState((initial?.virtualPool ?? []).join('\n'));

  const [localType, setLocalType] = useState<IPsecLocalAuthType>(initial?.auth.localType ?? 'psk');
  const [remoteType, setRemoteType] = useState<IPsecRemoteAuthType>(initial?.auth.remoteType ?? 'psk');
  const [localId, setLocalId] = useState(initial?.auth.localId ?? '');
  const [remoteId, setRemoteId] = useState(initial?.auth.remoteId ?? '');
  const [psk, setPsk] = useState('');
  const [localCertId, setLocalCertId] = useState(initial?.auth.localCertId ?? sortedCerts[0]?.id ?? '');
  const [caId, setCaId] = useState(initial?.auth.caId ?? sortedCAs[0]?.id ?? '');
  const [eapUsers, setEapUsers] = useState<EapUserDraft[]>(() =>
    (initial?.auth.eapUsers ?? []).map((u, idx) => ({
      key: `existing-${idx}`,
      username: u.username,
      password: '',
      existing: u,
    })),
  );

  const [proposals, setProposals] = useState((initial?.proposals ?? []).join('\n'));
  const [espProposals, setEspProposals] = useState((initial?.espProposals ?? []).join('\n'));
  const [dpdDelaySeconds, setDpdDelaySeconds] = useState<number | string>(initial?.dpdDelaySeconds ?? 30);
  const [startAction, setStartAction] = useState<IPsecStartAction>(initial?.startAction ?? 'trap');
  const [natTraversal, setNatTraversal] = useState(initial?.natTraversal ?? true);

  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const [stale, setStale] = useState(false);
  const submitting = useRef(false);
  const nextUserKey = useRef(1);

  const usesXfrm = mode === 'route-based' || mode === 'road-warrior';
  const usesPsk = localType === 'psk' || remoteType === 'psk';

  const zoneData = sortedZones.map((z) => ({ value: z.id, label: z.name }));
  if (zone && !zoneData.some((z) => z.value === zone)) {
    zoneData.unshift({ value: zone, label: `${zone} (missing zone)` });
  }
  const segmentData = [
    { value: '', label: 'None (use local address / any)' },
    ...sortedSegments.map((s) => ({ value: s.id, label: s.name })),
  ];
  if (localSegment && !segmentData.some((s) => s.value === localSegment)) {
    segmentData.push({ value: localSegment, label: `${localSegment} (missing segment)` });
  }
  const certData = sortedCerts.map((c) => ({ value: c.id, label: `${c.name} (${c.subject})` }));
  if (localCertId && !certData.some((c) => c.value === localCertId)) {
    certData.unshift({ value: localCertId, label: `${localCertId} (missing certificate)` });
  }
  const caData = sortedCAs.map((c) => ({ value: c.id, label: `${c.name} (${c.subject})` }));
  if (caId && !caData.some((c) => c.value === caId)) {
    caData.unshift({ value: caId, label: `${caId} (missing CA)` });
  }

  function handleModeChange(nextMode: IPsecMode) {
    setMode(nextMode);
    if (nextMode === 'road-warrior') {
      if (!remoteAddress.trim()) setRemoteAddress('%any');
      setStartAction('none');
    } else {
      if (remoteAddress.trim() === '%any') setRemoteAddress('');
      if (startAction === 'none') setStartAction('trap');
      if (remoteType === 'eap-mschapv2') setRemoteType('psk');
    }
    if (nextMode !== 'policy-based') {
      if (!ifname.trim()) setIfname(nextIfname(allConnections));
      if (xfrmIfId === '') setXfrmIfId(nextXfrmIfId(allConnections));
    }
  }

  function addEapUser() {
    nextUserKey.current += 1;
    setEapUsers((prev) => [...prev, { key: `new-${nextUserKey.current}`, username: '', password: '' }]);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current || stale) return;

    const nextErrors = new Map<string, string>();
    const trimmedName = name.trim();
    const trimmedIfname = ifname.trim();
    const trimmedLocalId = localId.trim();
    const parsedXfrmId = xfrmIfId === '' ? NaN : Number(xfrmIfId);
    const parsedDpd = dpdDelaySeconds === '' ? 30 : Number(dpdDelaySeconds);

    if (!trimmedName) nextErrors.set('name', 'Enter a connection name.');
    if (enabled && !zone) nextErrors.set('zone', 'Select a firewall zone for an enabled connection.');
    if (usesXfrm) {
      if (!ipsecIfnamePattern.test(trimmedIfname)) {
        nextErrors.set('ifname', 'Interface name must be ipsec0 through ipsec99.');
      }
      if (!Number.isInteger(parsedXfrmId) || parsedXfrmId < 1 || parsedXfrmId > 65535) {
        nextErrors.set('xfrmIfId', 'XFRM interface ID must be an integer between 1 and 65535.');
      }
    }
    if (mode !== 'road-warrior' && !remoteAddress.trim()) {
      nextErrors.set('remoteAddress', 'Enter the remote peer IP address or hostname.');
    }
    if (mode === 'policy-based') {
      if (parseList(localSubnets).length === 0) {
        nextErrors.set('localSubnets', 'Enter at least one local subnet CIDR.');
      }
      if (parseList(remoteSubnets).length === 0) {
        nextErrors.set('remoteSubnets', 'Enter at least one remote subnet CIDR.');
      }
    }
    if (mode === 'road-warrior' && parseList(virtualPool).length === 0) {
      nextErrors.set('virtualPool', 'Enter at least one virtual IP pool CIDR for road-warrior clients.');
    }
    if (!trimmedLocalId) nextErrors.set('auth/localId', 'Enter a local IKE identity.');
    const canKeepExistingPsk = Boolean(initial?.auth.pskRef?.isSet || (!enabled && initial?.auth.pskRef?.unresolved));
    if (usesPsk && psk === '' && !canKeepExistingPsk) {
      nextErrors.set('auth/pskRef', 'Enter a pre-shared key.');
    }
    if (localType === 'cert' && !localCertId) {
      nextErrors.set('auth/localCertId', 'Select a local certificate.');
    }
    if (remoteType === 'cert' && !caId) {
      nextErrors.set('auth/caId', 'Select a CA to verify remote peer certificates.');
    }
    if (remoteType === 'eap-mschapv2') {
      if (eapUsers.length === 0) {
        nextErrors.set('auth/eapUsers', 'Add at least one EAP-MSCHAPv2 user.');
      } else {
        const seenUsers = new Set<string>();
        for (const u of eapUsers) {
          const uname = u.username.trim();
          if (!uname) {
            nextErrors.set('auth/eapUsers', 'Every EAP user must have a username.');
            break;
          }
          if (seenUsers.has(uname)) {
            nextErrors.set('auth/eapUsers', `Duplicate EAP username "${uname}".`);
            break;
          }
          seenUsers.add(uname);
          const canKeepExisting =
            u.existing?.username === uname
            && Boolean(u.existing.passwordRef?.isSet || (!enabled && u.existing.passwordRef?.unresolved));
          if (u.password === '' && !canKeepExisting) {
            nextErrors.set('auth/eapUsers', `Enter a password for EAP user "${uname}".`);
            break;
          }
        }
      }
    }
    if (!Number.isInteger(parsedDpd) || parsedDpd < 0 || parsedDpd > 3600) {
      nextErrors.set('dpdDelaySeconds', 'DPD delay must be between 0 and 3600 seconds.');
    }

    if (nextErrors.size > 0) {
      setFailure('');
      setErrors(nextErrors);
      return;
    }

    const parsedTunnelAddrs = parseList(tunnelAddresses);
    const parsedLocalSubnets = parseList(localSubnets);
    const parsedRemoteSubnets = parseList(remoteSubnets);
    const parsedVirtualPool = parseList(virtualPool);
    const parsedProposals = parseList(proposals);
    const parsedEspProposals = parseList(espProposals);

    const eapPayload: IPsecEapUserWrite[] = eapUsers.map((u) => {
      const uname = u.username.trim();
      const keptRef = u.existing?.username === uname ? u.existing.passwordRef : undefined;
      return {
        username: uname,
        passwordRef: buildSecretWrite(u.password, keptRef, 'ipsec_eap'),
      };
    });

    const payload: IPsecConnectionWrite = {
      name: trimmedName,
      ...(description.trim() ? { description: description.trim() } : {}),
      enabled,
      mode,
      ...(usesXfrm ? { ifname: trimmedIfname, xfrmIfId: parsedXfrmId } : {}),
      ...(zone ? { zone } : {}),
      ...(usesXfrm && parsedTunnelAddrs.length > 0 ? { tunnelAddresses: parsedTunnelAddrs } : {}),
      ...(localSegment ? { localSegment } : {}),
      ...(localAddress.trim() ? { localAddress: localAddress.trim() } : {}),
      remoteAddress: mode === 'road-warrior' ? (remoteAddress.trim() || '%any') : remoteAddress.trim(),
      ikeVersion: 2,
      ...(parsedProposals.length > 0 ? { proposals: parsedProposals } : {}),
      ...(parsedEspProposals.length > 0 ? { espProposals: parsedEspProposals } : {}),
      auth: {
        localType,
        remoteType,
        localId: trimmedLocalId,
        ...(remoteId.trim() ? { remoteId: remoteId.trim() } : {}),
        ...(usesPsk ? { pskRef: buildSecretWrite(psk, initial?.auth.pskRef, 'ipsec_psk') } : {}),
        ...(localType === 'cert' && localCertId ? { localCertId } : {}),
        ...(remoteType === 'cert' && caId ? { caId } : {}),
        ...(remoteType === 'eap-mschapv2' ? { eapUsers: eapPayload } : {}),
      },
      ...(mode === 'policy-based' || parsedLocalSubnets.length > 0 ? { localSubnets: parsedLocalSubnets } : {}),
      ...(mode === 'policy-based' || (mode === 'route-based' && parsedRemoteSubnets.length > 0)
        ? { remoteSubnets: parsedRemoteSubnets }
        : {}),
      ...(mode === 'road-warrior' ? { virtualPool: parsedVirtualPool } : {}),
      dpdDelaySeconds: parsedDpd,
      startAction: mode === 'road-warrior' ? 'none' : startAction,
      natTraversal,
    };

    submitting.current = true;
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      const saved = await api.saveIpsecConnection(payload, existing);
      onSaved(saved);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (error instanceof ApiError && (error.status === 404 || error.status === 412)) {
        setStale(true);
        onConflict();
      }
      const fields = fieldErrors(error, connFieldPattern);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'IPsec connection'));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  const remoteAuthOptions = [
    { value: 'psk', label: 'Pre-shared key (PSK)' },
    { value: 'cert', label: 'Certificate (CA verified)' },
    ...(mode === 'road-warrior' || remoteType === 'eap-mschapv2'
      ? [{ value: 'eap-mschapv2', label: 'EAP-MSCHAPv2 (username & password)' }]
      : []),
  ];

  return (
    <Modal
      opened
      onClose={() => { if (!busy) onClose(); }}
      title={existing ? 'Edit IPsec connection' : 'Add IPsec connection'}
      size="lg"
      centered
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
    >
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert color="red" role="alert" title="IPsec connection was not saved">{failure}</Alert>}
          <Fieldset variant="unstyled" disabled={busy || stale}>
            <Stack>
              <TextInput
                label="Name"
                placeholder="HQ Site-to-Site"
                required
                value={name}
                error={errors.get('name')}
                onChange={(e) => setName(e.currentTarget.value)}
              />
              <Textarea
                label="Description"
                placeholder="Optional note"
                autosize
                minRows={1}
                maxRows={3}
                value={description}
                error={errors.get('description')}
                onChange={(e) => setDescription(e.currentTarget.value)}
              />

              <Group grow align="flex-start">
                <Select
                  label="Connection mode"
                  required
                  allowDeselect={false}
                  data={[
                    { value: 'route-based', label: 'Route-based (XFRM)' },
                    { value: 'policy-based', label: 'Policy-based tunnel' },
                    { value: 'road-warrior', label: 'Road-warrior server' },
                  ]}
                  value={mode}
                  error={errors.get('mode')}
                  onChange={(val) => {
                    if (val === 'route-based' || val === 'policy-based' || val === 'road-warrior') {
                      handleModeChange(val);
                    }
                  }}
                />
                <Select
                  label="Firewall zone"
                  required={enabled}
                  searchable
                  clearable={!enabled}
                  data={zoneData}
                  value={zone || null}
                  error={errors.get('zone')}
                  onChange={(val) => setZone(val ?? '')}
                />
              </Group>

              {usesXfrm && (
                <Group grow align="flex-start">
                  <TextInput
                    label="XFRM interface name"
                    description="ipsec0 through ipsec99."
                    placeholder="ipsec0"
                    classNames={{ input: 'mono-input' }}
                    required
                    value={ifname}
                    error={errors.get('ifname')}
                    onChange={(e) => setIfname(e.currentTarget.value)}
                  />
                  <NumberInput
                    label="XFRM interface ID"
                    description="Unique integer (1 to 65535)."
                    allowDecimal={false}
                    min={1}
                    max={65535}
                    required
                    value={xfrmIfId}
                    error={errors.get('xfrmIfId')}
                    onChange={setXfrmIfId}
                  />
                </Group>
              )}

              <Group grow align="flex-start">
                <Select
                  label="Local segment (optional)"
                  searchable
                  allowDeselect={false}
                  data={segmentData}
                  value={localSegment}
                  error={errors.get('localSegment')}
                  onChange={(val) => setLocalSegment(val ?? '')}
                />
                <TextInput
                  label="Local address (optional)"
                  placeholder="203.0.113.1"
                  classNames={{ input: 'mono-input' }}
                  value={localAddress}
                  error={errors.get('localAddress')}
                  onChange={(e) => setLocalAddress(e.currentTarget.value)}
                />
              </Group>

              <TextInput
                label={mode === 'road-warrior' ? 'Remote address (responder)' : 'Remote peer address'}
                description={mode === 'road-warrior' ? 'Use %any to accept connections from any client IP.' : 'Remote gateway IP address or FQDN.'}
                placeholder={mode === 'road-warrior' ? '%any' : '198.51.100.1 or vpn.example.com'}
                classNames={{ input: 'mono-input' }}
                required={mode !== 'road-warrior'}
                value={remoteAddress}
                error={errors.get('remoteAddress')}
                onChange={(e) => setRemoteAddress(e.currentTarget.value)}
              />

              {usesXfrm && (
                <Textarea
                  label="Tunnel addresses (optional)"
                  description="IPv4 or IPv6 interface CIDRs assigned to the XFRM interface (one per line or comma-separated)."
                  placeholder={'169.254.100.1/30\nfd00:100::1/126'}
                  classNames={{ input: 'mono-input' }}
                  autosize
                  minRows={2}
                  maxRows={4}
                  value={tunnelAddresses}
                  error={errors.get('tunnelAddresses')}
                  onChange={(e) => setTunnelAddresses(e.currentTarget.value)}
                />
              )}

              {(mode === 'policy-based' || mode === 'route-based') && (
                <Group grow align="flex-start">
                  <Textarea
                    label={mode === 'policy-based' ? 'Local subnets' : 'Local traffic selectors (optional)'}
                    description={mode === 'policy-based' ? 'Local CIDRs exposed through the tunnel.' : 'Leave empty for 0.0.0.0/0, ::/0.'}
                    placeholder="10.0.0.0/24"
                    classNames={{ input: 'mono-input' }}
                    required={mode === 'policy-based'}
                    autosize
                    minRows={2}
                    maxRows={4}
                    value={localSubnets}
                    error={errors.get('localSubnets')}
                    onChange={(e) => setLocalSubnets(e.currentTarget.value)}
                  />
                  <Textarea
                    label={mode === 'policy-based' ? 'Remote subnets' : 'Remote traffic selectors (optional)'}
                    description={mode === 'policy-based' ? 'Remote CIDRs reachable through the tunnel.' : 'Leave empty for 0.0.0.0/0, ::/0.'}
                    placeholder="10.20.0.0/24"
                    classNames={{ input: 'mono-input' }}
                    required={mode === 'policy-based'}
                    autosize
                    minRows={2}
                    maxRows={4}
                    value={remoteSubnets}
                    error={errors.get('remoteSubnets')}
                    onChange={(e) => setRemoteSubnets(e.currentTarget.value)}
                  />
                </Group>
              )}

              {mode === 'road-warrior' && (
                <Group grow align="flex-start">
                  <Textarea
                    label="Virtual IP pool"
                    description="Client address pool CIDRs (IPv4 and/or IPv6)."
                    placeholder="10.99.0.0/24"
                    classNames={{ input: 'mono-input' }}
                    required
                    autosize
                    minRows={2}
                    maxRows={4}
                    value={virtualPool}
                    error={errors.get('virtualPool')}
                    onChange={(e) => setVirtualPool(e.currentTarget.value)}
                  />
                  <Textarea
                    label="Local traffic selectors (optional)"
                    description="Subnets advertised to remote clients (defaults to 0.0.0.0/0, ::/0)."
                    placeholder="10.0.0.0/24"
                    classNames={{ input: 'mono-input' }}
                    autosize
                    minRows={2}
                    maxRows={4}
                    value={localSubnets}
                    error={errors.get('localSubnets')}
                    onChange={(e) => setLocalSubnets(e.currentTarget.value)}
                  />
                </Group>
              )}

              <Group grow align="flex-start">
                <Select
                  label="Local authentication"
                  required
                  allowDeselect={false}
                  data={[
                    { value: 'psk', label: 'Pre-shared key (PSK)' },
                    { value: 'cert', label: 'Certificate' },
                  ]}
                  value={localType}
                  error={errors.get('auth/localType')}
                  onChange={(val) => {
                    if (val === 'psk' || val === 'cert') setLocalType(val);
                  }}
                />
                <Select
                  label="Remote authentication"
                  required
                  allowDeselect={false}
                  data={remoteAuthOptions}
                  value={remoteType}
                  error={errors.get('auth/remoteType')}
                  onChange={(val) => {
                    if (val === 'psk' || val === 'cert' || val === 'eap-mschapv2') setRemoteType(val);
                  }}
                />
              </Group>

              <Group grow align="flex-start">
                <TextInput
                  label="Local IKE identity"
                  placeholder="gw.example.com"
                  classNames={{ input: 'mono-input' }}
                  required
                  value={localId}
                  error={errors.get('auth/localId')}
                  onChange={(e) => setLocalId(e.currentTarget.value)}
                />
                <TextInput
                  label="Remote IKE identity (optional)"
                  placeholder="peer.example.com"
                  classNames={{ input: 'mono-input' }}
                  value={remoteId}
                  error={errors.get('auth/remoteId')}
                  onChange={(e) => setRemoteId(e.currentTarget.value)}
                />
              </Group>

              {usesPsk && (
                <PasswordInput
                  label="Pre-shared key"
                  description={
                    initial?.auth.pskRef?.unresolved
                      ? 'Secret was excluded during restore — enter a new key to resolve.'
                      : initial?.auth.pskRef?.isSet
                        ? 'A pre-shared key is stored. Leave blank to keep the existing key.'
                        : 'Shared IKEv2 authentication secret.'
                  }
                  placeholder={initial?.auth.pskRef?.isSet ? 'Leave blank to keep existing' : 'Enter pre-shared key'}
                  required={!initial?.auth.pskRef?.isSet}
                  value={psk}
                  error={errors.get('auth/pskRef')}
                  onChange={(e) => setPsk(e.currentTarget.value)}
                />
              )}

              {localType === 'cert' && (
                <Select
                  label="Local certificate"
                  description="Managed PKI certificate presented by this appliance."
                  required
                  searchable
                  allowDeselect={false}
                  data={certData}
                  value={localCertId || null}
                  error={errors.get('auth/localCertId')}
                  onChange={(val) => setLocalCertId(val ?? '')}
                />
              )}

              {remoteType === 'cert' && (
                <Select
                  label="Trusted CA"
                  description="CA certificate used to verify the remote peer."
                  required
                  searchable
                  allowDeselect={false}
                  data={caData}
                  value={caId || null}
                  error={errors.get('auth/caId')}
                  onChange={(val) => setCaId(val ?? '')}
                />
              )}

              {remoteType === 'eap-mschapv2' && (
                <Stack gap="xs">
                  <Group justify="space-between" align="center">
                    <Text size="sm" fw={500}>EAP-MSCHAPv2 users</Text>
                    <Button size="xs" variant="default" onClick={addEapUser}>
                      Add EAP user
                    </Button>
                  </Group>
                  {errors.get('auth/eapUsers') && (
                    <Text size="xs" c="red">{errors.get('auth/eapUsers')}</Text>
                  )}
                  {eapUsers.map((user, idx) => (
                    <Group key={user.key} align="flex-end" wrap="nowrap">
                      <TextInput
                        label={idx === 0 ? 'Username' : undefined}
                        aria-label={`EAP username ${idx + 1}`}
                        placeholder="alice"
                        style={{ flex: 1 }}
                        value={user.username}
                        onChange={(e) => {
                          const value = e.currentTarget.value;
                          setEapUsers((prev) => prev.map((row) => (row.key === user.key ? { ...row, username: value } : row)));
                        }}
                      />
                      <PasswordInput
                        label={idx === 0 ? 'Password' : undefined}
                        aria-label={`EAP password ${idx + 1}`}
                        placeholder={
                          user.existing?.username === user.username.trim() && user.existing?.passwordRef?.isSet
                            ? 'Leave blank to keep'
                            : 'Password'
                        }
                        style={{ flex: 1 }}
                        value={user.password}
                        onChange={(e) => {
                          const value = e.currentTarget.value;
                          setEapUsers((prev) => prev.map((row) => (row.key === user.key ? { ...row, password: value } : row)));
                        }}
                      />
                      <Button
                        size="xs"
                        variant="light"
                        color="red"
                        aria-label={`Remove EAP user ${idx + 1}`}
                        onClick={() => setEapUsers((prev) => prev.filter((row) => row.key !== user.key))}
                      >
                        Remove
                      </Button>
                    </Group>
                  ))}
                </Stack>
              )}

              <Group grow align="flex-start">
                <Textarea
                  label="IKE proposals (optional)"
                  description="Leave empty for strongSwan modern IKEv2 defaults."
                  placeholder="aes256gcm16-prfsha384-ecp384"
                  classNames={{ input: 'mono-input' }}
                  autosize
                  minRows={1}
                  maxRows={3}
                  value={proposals}
                  error={errors.get('proposals')}
                  onChange={(e) => setProposals(e.currentTarget.value)}
                />
                <Textarea
                  label="ESP proposals (optional)"
                  description="Leave empty for strongSwan modern ESP defaults."
                  placeholder="aes256gcm16-ecp384"
                  classNames={{ input: 'mono-input' }}
                  autosize
                  minRows={1}
                  maxRows={3}
                  value={espProposals}
                  error={errors.get('espProposals')}
                  onChange={(e) => setEspProposals(e.currentTarget.value)}
                />
              </Group>

              <Group grow align="flex-start">
                <NumberInput
                  label="DPD delay (seconds)"
                  description="Dead Peer Detection interval (0 disables DPD)."
                  allowDecimal={false}
                  min={0}
                  max={3600}
                  value={dpdDelaySeconds}
                  error={errors.get('dpdDelaySeconds')}
                  onChange={setDpdDelaySeconds}
                />
                <Select
                  label="Start action"
                  allowDeselect={false}
                  disabled={mode === 'road-warrior'}
                  data={(Object.entries(startActionLabels) as Array<[IPsecStartAction, string]>).map(([value, label]) => ({ value, label }))}
                  value={mode === 'road-warrior' ? 'none' : startAction}
                  error={errors.get('startAction')}
                  onChange={(val) => {
                    if (val === 'trap' || val === 'start' || val === 'none') setStartAction(val);
                  }}
                />
              </Group>

              <Group gap="lg">
                <Switch
                  label="Enable NAT traversal (UDP 4500)"
                  checked={natTraversal}
                  onChange={(e) => setNatTraversal(e.currentTarget.checked)}
                />
                <Switch
                  label="Enable connection"
                  checked={enabled}
                  onChange={(e) => setEnabled(e.currentTarget.checked)}
                />
              </Group>
            </Stack>
          </Fieldset>
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={busy} onClick={onClose}>
              {stale ? 'Close' : 'Cancel'}
            </Button>
            <Button type="submit" loading={busy} disabled={stale}>
              {existing ? 'Save connection' : 'Add connection'}
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
