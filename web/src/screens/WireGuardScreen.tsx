import {
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  Code,
  Fieldset,
  Group,
  Modal,
  MultiSelect,
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
import { ApiClient, ApiError, fieldErrors, saveFailure } from '../api';
import { triggerDownload } from '../download';
import { bytes } from '../format';
import { encodeQr, qrSvgPath } from '../qr';
import { formatTimestamp, useTimeZone } from '../timezone';
import type {
  SavedObject,
  SecretView,
  SecretWrite,
  Segment,
  Session,
  WireGuardInterface,
  WireGuardInterfaceWrite,
  WireGuardPeer,
  WireGuardPeerWrite,
  WireGuardStatusInterface,
  WireGuardStatusPeer,
  Zone,
} from '../types';
import { useResource, useVisiblePoll } from '../useResource';
import { Loaded } from './Loaded';

interface WireGuardScreenProps {
  api: ApiClient;
  session: Session;
  onSaved(): void;
  onSessionLost(): void;
}

interface LoadedWireGuardConfig {
  interfaces: Array<SavedObject<WireGuardInterface>>;
  zones: Zone[];
  segments: Segment[];
}

const STATUS_POLL_MS = 15_000;
const wgIfnamePattern = /^wg([0-9]|[1-9][0-9])$/;
const ifaceFieldPattern = /^\/?(name|description|ifname|zone|listenPort|allowListenSegments|mtu|addresses|privateKeyRef)(?:\/|$)/;

function parseList(raw: string): string[] {
  return raw
    .split(/[\s,]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function nextIfname(interfaces: Array<SavedObject<WireGuardInterface>>): string {
  const used = new Set(interfaces.map((item) => item.value.ifname));
  for (let i = 0; i <= 99; i++) {
    const candidate = `wg${i}`;
    if (!used.has(candidate)) return candidate;
  }
  return 'wg0';
}

function newSecretName(prefix: string): string {
  const random = new Uint8Array(6);
  globalThis.crypto.getRandomValues(random);
  const suffix = Array.from(random, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${prefix}_${suffix}`;
}

function buildSecretWrite(typed: string, existing: SecretView | undefined, prefix: string): SecretWrite | Record<string, never> {
  if (typed.trim() !== '') {
    return {
      secret: existing?.secret || newSecretName(prefix),
      set: typed.trim(),
      expectedVersion: existing?.version || '0',
    };
  }
  return {};
}

function peerToWrite(peer: WireGuardPeer): WireGuardPeerWrite {
  return {
    id: peer.id,
    name: peer.name,
    ...(peer.description ? { description: peer.description } : {}),
    enabled: peer.enabled,
    publicKey: peer.publicKey,
    ...(peer.presharedKeyRef?.secret ? { presharedKeyRef: {} } : {}),
    ...(peer.allowedIps && peer.allowedIps.length > 0 ? { allowedIps: peer.allowedIps } : {}),
    ...(peer.endpoint ? { endpoint: peer.endpoint } : {}),
    ...(peer.persistentKeepalive !== undefined && peer.persistentKeepalive > 0
      ? { persistentKeepalive: peer.persistentKeepalive }
      : {}),
    routeAllowedIps: peer.routeAllowedIps ?? true,
  };
}

function interfaceToWrite(
  iface: WireGuardInterface,
  peers: WireGuardPeerWrite[],
): WireGuardInterfaceWrite {
  return {
    name: iface.name,
    ...(iface.description ? { description: iface.description } : {}),
    ifname: iface.ifname,
    enabled: iface.enabled,
    ...(iface.zone ? { zone: iface.zone } : {}),
    listenPort: iface.listenPort,
    allowListenPort: iface.allowListenPort ?? true,
    ...(iface.allowListenSegments && iface.allowListenSegments.length > 0
      ? { allowListenSegments: iface.allowListenSegments }
      : {}),
    ...(iface.mtu !== undefined ? { mtu: iface.mtu } : {}),
    ...(iface.addresses && iface.addresses.length > 0 ? { addresses: iface.addresses } : {}),
    privateKeyRef: {},
    ...(peers.length > 0 ? { peers } : {}),
  };
}

function shortKey(key: string): string {
  if (key.length <= 16) return key;
  return `${key.slice(0, 10)}…${key.slice(-6)}`;
}

export function WireGuardScreen({ api, session, onSaved, onSessionLost }: WireGuardScreenProps) {
  const canReadConfig = session.capabilities.includes('configuration.read');
  const canWrite = session.capabilities.includes('configuration.write');
  const canReadStatus = session.capabilities.includes('vpn.read');
  const canControl = session.capabilities.includes('vpn.control');
  const timeZone = useTimeZone();

  const [notice, setNotice] = useState('');
  const [ifaceEditor, setIfaceEditor] = useState<SavedObject<WireGuardInterface> | 'new' | undefined>(undefined);
  const [peerEditor, setPeerEditor] = useState<{ iface: SavedObject<WireGuardInterface>; peer?: WireGuardPeer } | null>(null);
  const [exportTarget, setExportTarget] = useState<{ iface: WireGuardInterface; peer: WireGuardPeer } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<SavedObject<WireGuardInterface> | null>(null);
  const [deletePeerTarget, setDeletePeerTarget] = useState<{ iface: SavedObject<WireGuardInterface>; peer: WireGuardPeer } | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState('');
  // Client private keys generated in this browser session stay strictly in memory (never sent to the appliance).
  const [clientPrivateKeys, setClientPrivateKeys] = useState<Record<string, string>>({});

  const loadConfig = useCallback(async (): Promise<LoadedWireGuardConfig | undefined> => {
    if (!canReadConfig) return undefined;
    const [wgRes, zonesRes, segmentsRes] = await Promise.all([
      api.wireguardInterfaces(),
      api.zones(),
      api.segments(),
    ]);
    return {
      interfaces: wgRes.items,
      zones: zonesRes.items.map((item) => item.value),
      segments: segmentsRes.items.map((item) => item.value),
    };
  }, [api, canReadConfig]);

  const loadStatus = useCallback(async () => {
    if (!canReadStatus) return undefined;
    return api.wireguardStatus();
  }, [api, canReadStatus]);

  const configResource = useResource(loadConfig, onSessionLost);
  const statusResource = useResource(loadStatus, onSessionLost);
  useVisiblePoll(statusResource.refresh, STATUS_POLL_MS, canReadStatus);

  const statusByIfname = useMemo(() => {
    const map = new Map<string, WireGuardStatusInterface>();
    for (const item of statusResource.data?.items ?? []) {
      map.set(item.ifname, item);
    }
    return map;
  }, [statusResource.data]);

  function reloadAll() {
    if (canReadConfig) void configResource.reload();
    if (canReadStatus) void statusResource.reload();
  }

  function handleSaved(message: string) {
    setNotice(message);
    onSaved();
    reloadAll();
  }

  async function confirmDeleteInterface() {
    if (!deleteTarget || deleting) return;
    setDeleting(true);
    setDeleteError('');
    try {
      await api.deleteWireguardInterface(deleteTarget.value.id, deleteTarget.version);
      const name = deleteTarget.value.name;
      setDeleteTarget(null);
      handleSaved(`Deleted WireGuard interface "${name}".`);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (error instanceof ApiError && (error.status === 404 || error.status === 412)) {
        void configResource.reload();
      }
      setDeleteError(saveFailure(error, new Map(), 'WireGuard interface'));
    } finally {
      setDeleting(false);
    }
  }

  async function confirmDeletePeer() {
    if (!deletePeerTarget || deleting) return;
    setDeleting(true);
    setDeleteError('');
    try {
      const { iface, peer } = deletePeerTarget;
      const remaining = (iface.value.peers ?? [])
        .filter((p) => p.id !== peer.id)
        .map(peerToWrite);
      await api.saveWireguardInterface(interfaceToWrite(iface.value, remaining), iface);
      setDeletePeerTarget(null);
      handleSaved(`Removed peer "${peer.name}" from ${iface.value.ifname}.`);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (error instanceof ApiError && (error.status === 404 || error.status === 412)) {
        void configResource.reload();
      }
      setDeleteError(saveFailure(error, new Map(), 'WireGuard peer'));
    } finally {
      setDeleting(false);
    }
  }

  const loadedInterfaces = configResource.data?.interfaces ?? [];
  const loadedZones = configResource.data?.zones ?? [];
  const loadedSegments = configResource.data?.segments ?? [];

  return (
    <Stack gap="lg">
      <Group justify="space-between" align="flex-end" wrap="wrap">
        <div>
          <Title order={2}>WireGuard</Title>
          <Text size="sm" c="dimmed">
            Configure native WireGuard VPN interfaces, peers, and client profiles.
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
            <Button size="sm" onClick={() => setIfaceEditor('new')}>
              Add interface
            </Button>
          )}
        </Group>
      </Group>

      {notice && (
        <Alert color="green" role="status" withCloseButton onClose={() => setNotice('')}>
          {notice}
        </Alert>
      )}

      {canReadStatus && statusResource.error && (
        <Alert color="orange" role="alert" title="Live WireGuard status unavailable">
          {statusResource.error}
        </Alert>
      )}

      {canReadConfig ? (
        <Loaded resource={configResource} what="WireGuard interfaces">
          {(loaded) => {
            if (!loaded) return null;
            const { interfaces, zones, segments } = loaded;
            const zonesById = new Map(zones.map((z) => [z.id, z.name]));
            const segmentsById = new Map(segments.map((s) => [s.id, s.name]));
            const sorted = [...interfaces].sort((a, b) =>
              a.value.ifname.localeCompare(b.value.ifname, undefined, { numeric: true })
              || a.value.name.localeCompare(b.value.name, undefined, { sensitivity: 'base' }));

            if (sorted.length === 0) {
              return (
                <Alert role="status" title="No WireGuard interfaces configured">
                  {canWrite
                    ? 'Add a WireGuard interface to establish site-to-site or remote-access tunnels.'
                    : 'No WireGuard interfaces are currently configured.'}
                </Alert>
              );
            }

            return (
              <Stack gap="md">
                {sorted.map((saved) => {
                  const wg = saved.value;
                  const peers = wg.peers ?? [];
                  const addresses = wg.addresses ?? [];
                  const allowListen = wg.allowListenPort ?? true;
                  const live = statusByIfname.get(wg.ifname);
                  const livePeersByKey = new Map<string, WireGuardStatusPeer>();
                  for (const lp of live?.peers ?? []) livePeersByKey.set(lp.publicKey, lp);
                  const onlineCount = peers.filter((p) => livePeersByKey.get(p.publicKey)?.online).length;
                  const listenScope = !allowListen
                    ? 'Firewall rule disabled'
                    : wg.allowListenSegments && wg.allowListenSegments.length > 0
                      ? `Allowed on ${wg.allowListenSegments.map((id) => segmentsById.get(id) ?? id).join(', ')}`
                      : 'Allowed on uplinks';

                  return (
                    <Card key={wg.id} withBorder padding="md">
                      <Stack gap="md">
                        <Group justify="space-between" align="flex-start" wrap="wrap">
                          <Stack gap={4}>
                            <Group gap="xs" wrap="wrap">
                              <Title order={3} size="h4">{wg.name}</Title>
                              <Badge variant="outline" ff="monospace">{wg.ifname}</Badge>
                              <Badge color={wg.enabled ? 'green' : 'gray'} variant="light">
                                {wg.enabled ? 'Enabled' : 'Disabled'}
                              </Badge>
                              {wg.privateKeyRef.unresolved && (
                                <Badge color="red" variant="filled">Key unresolved</Badge>
                              )}
                              {canReadStatus && wg.enabled && (
                                <Badge color={onlineCount > 0 ? 'teal' : 'gray'} variant="light">
                                  {onlineCount}/{peers.filter((p) => p.enabled).length} peers online
                                </Badge>
                              )}
                            </Group>
                            {wg.description && <Text size="sm" c="dimmed">{wg.description}</Text>}
                            <Group gap="md" wrap="wrap">
                              <Text size="xs" c="dimmed">
                                Zone: <Text span fw={500} c="var(--mantine-color-text)">{wg.zone ? (zonesById.get(wg.zone) ?? wg.zone) : 'Unassigned'}</Text>
                              </Text>
                              <Text size="xs" c="dimmed">
                                Port: <Text span ff="monospace" c="var(--mantine-color-text)">UDP {wg.listenPort}</Text> ({listenScope})
                              </Text>
                              <Text size="xs" c="dimmed">
                                MTU: <Text span className="tabular" c="var(--mantine-color-text)">{wg.mtu ?? 1420}</Text>
                              </Text>
                              <Text size="xs" c="dimmed">
                                Addresses: <Text span ff="monospace" c="var(--mantine-color-text)">{addresses.join(', ') || 'None'}</Text>
                              </Text>
                            </Group>
                            {wg.publicKey && (
                              <Group gap="xs" align="center">
                                <Text size="xs" c="dimmed">Public key:</Text>
                                <Code fz="xs">{wg.publicKey}</Code>
                              </Group>
                            )}
                          </Stack>

                          {canWrite && (
                            <Group gap="xs" wrap="nowrap">
                              <Button
                                size="xs"
                                variant="light"
                                aria-label={`Add peer to ${wg.name}`}
                                onClick={() => setPeerEditor({ iface: saved })}
                              >
                                Add peer
                              </Button>
                              <Button
                                size="xs"
                                variant="default"
                                aria-label={`Edit interface ${wg.name}`}
                                onClick={() => setIfaceEditor(saved)}
                              >
                                Edit
                              </Button>
                              <Button
                                size="xs"
                                variant="light"
                                color="red"
                                aria-label={`Delete interface ${wg.name}`}
                                onClick={() => {
                                  setDeleteError('');
                                  setDeleteTarget(saved);
                                }}
                              >
                                Delete
                              </Button>
                            </Group>
                          )}
                        </Group>

                        {peers.length === 0 ? (
                          <Text size="sm" c="dimmed">No peers configured on this interface.</Text>
                        ) : (
                          <ScrollArea>
                            <Table className="data-table" striped highlightOnHover>
                              <Table.Thead>
                                <Table.Tr>
                                  <Table.Th>Peer</Table.Th>
                                  <Table.Th>Public key</Table.Th>
                                  <Table.Th>Allowed IPs</Table.Th>
                                  <Table.Th>Endpoint</Table.Th>
                                  <Table.Th>PSK</Table.Th>
                                  {canReadStatus && <Table.Th>Live status</Table.Th>}
                                  {(canWrite || canControl) && <Table.Th>Actions</Table.Th>}
                                </Table.Tr>
                              </Table.Thead>
                              <Table.Tbody>
                                {peers.map((peer) => {
                                  const livePeer = livePeersByKey.get(peer.publicKey);
                                  const endpoint = livePeer?.endpoint || peer.endpoint;
                                  const peerAllowedIps = peer.allowedIps ?? [];
                                  const peerRouteAllowed = peer.routeAllowedIps ?? true;
                                  return (
                                    <Table.Tr key={peer.id}>
                                      <Table.Td data-label="Peer">
                                        <Group gap={6} wrap="nowrap">
                                          <Text size="sm" fw={500}>{peer.name}</Text>
                                          {!peer.enabled && (
                                            <Badge size="xs" color="gray" variant="light">Disabled</Badge>
                                          )}
                                        </Group>
                                        {peer.description && <Text size="xs" c="dimmed">{peer.description}</Text>}
                                      </Table.Td>
                                      <Table.Td data-label="Public key">
                                        <Text size="xs" ff="monospace" title={peer.publicKey}>
                                          {shortKey(peer.publicKey)}
                                        </Text>
                                      </Table.Td>
                                      <Table.Td data-label="Allowed IPs">
                                        <Text size="xs" ff="monospace">{peerAllowedIps.join(', ') || 'None'}</Text>
                                        {!peerRouteAllowed && (
                                          <Text size="xs" c="dimmed">Manual routes</Text>
                                        )}
                                      </Table.Td>
                                      <Table.Td data-label="Endpoint">
                                        {endpoint ? (
                                          <Text size="xs" ff="monospace">{endpoint}</Text>
                                        ) : (
                                          <Text size="xs" c="dimmed">Dynamic</Text>
                                        )}
                                      </Table.Td>
                                      <Table.Td data-label="PSK">
                                        {peer.presharedKeyRef?.unresolved ? (
                                          <Badge size="xs" color="red" variant="filled">Unresolved</Badge>
                                        ) : peer.presharedKeyRef?.isSet ? (
                                          <Badge size="xs" color="blue" variant="light">Set</Badge>
                                        ) : (
                                          <Text size="xs" c="dimmed">None</Text>
                                        )}
                                      </Table.Td>
                                      {canReadStatus && (
                                        <Table.Td data-label="Live status">
                                          {!wg.enabled || !peer.enabled ? (
                                            <Badge size="xs" color="gray" variant="light">Disabled</Badge>
                                          ) : livePeer ? (
                                            <Stack gap={2}>
                                              <Group gap={6} wrap="nowrap">
                                                <Badge size="xs" color={livePeer.online ? 'green' : 'gray'} variant="light">
                                                  {livePeer.online ? 'Online' : 'Idle'}
                                                </Badge>
                                                <Text size="xs" className="tabular" c="dimmed">
                                                  ↓ {bytes(livePeer.rxBytes)} · ↑ {bytes(livePeer.txBytes)}
                                                </Text>
                                              </Group>
                                              <Text size="xs" c="dimmed">
                                                {livePeer.latestHandshakeAt
                                                  ? `Handshake ${formatTimestamp(livePeer.latestHandshakeAt, timeZone)}`
                                                  : 'No handshake yet'}
                                              </Text>
                                            </Stack>
                                          ) : (
                                            <Text size="xs" c="dimmed">Not applied</Text>
                                          )}
                                        </Table.Td>
                                      )}
                                      {(canWrite || canControl) && (
                                        <Table.Td data-label="Actions">
                                          <Group gap="xs" wrap="nowrap">
                                            {canControl && (
                                              <Button
                                                size="xs"
                                                variant="light"
                                                aria-label={`Export config for peer ${peer.name}`}
                                                onClick={() => setExportTarget({ iface: wg, peer })}
                                              >
                                                Config / QR
                                              </Button>
                                            )}
                                            {canWrite && (
                                              <>
                                                <Button
                                                  size="xs"
                                                  variant="default"
                                                  aria-label={`Edit peer ${peer.name}`}
                                                  onClick={() => setPeerEditor({ iface: saved, peer })}
                                                >
                                                  Edit
                                                </Button>
                                                <Button
                                                  size="xs"
                                                  variant="light"
                                                  color="red"
                                                  aria-label={`Delete peer ${peer.name}`}
                                                  onClick={() => {
                                                    setDeleteError('');
                                                    setDeletePeerTarget({ iface: saved, peer });
                                                  }}
                                                >
                                                  Delete
                                                </Button>
                                              </>
                                            )}
                                          </Group>
                                        </Table.Td>
                                      )}
                                    </Table.Tr>
                                  );
                                })}
                              </Table.Tbody>
                            </Table>
                          </ScrollArea>
                        )}
                      </Stack>
                    </Card>
                  );
                })}
              </Stack>
            );
          }}
        </Loaded>
      ) : (
        <Loaded resource={statusResource} what="WireGuard status">
          {(status) => {
            const items = status?.items ?? [];
            if (items.length === 0) {
              return <Alert role="status" title="No active WireGuard interfaces">No WireGuard interfaces are currently active.</Alert>;
            }
            return (
              <Stack gap="md">
                {items.map((wg) => (
                  <Card key={wg.ifname} withBorder padding="md">
                    <Stack gap="xs">
                      <Group gap="xs">
                        <Title order={3} size="h4" ff="monospace">{wg.ifname}</Title>
                        <Text size="xs" c="dimmed">UDP {wg.listenPort}</Text>
                      </Group>
                      <Text size="xs" c="dimmed">{wg.peers.length} peers</Text>
                    </Stack>
                  </Card>
                ))}
              </Stack>
            );
          }}
        </Loaded>
      )}

      {ifaceEditor !== undefined && (
        <InterfaceModal
          api={api}
          existing={ifaceEditor === 'new' ? undefined : ifaceEditor}
          defaultIfname={nextIfname(loadedInterfaces)}
          zones={loadedZones}
          segments={loadedSegments}
          canControl={canControl}
          onClose={() => setIfaceEditor(undefined)}
          onSaved={(saved) => {
            setIfaceEditor(undefined);
            handleSaved(`Saved WireGuard interface "${saved.value.name}" (${saved.value.ifname}).`);
          }}
          onConflict={() => void configResource.reload()}
          onSessionLost={onSessionLost}
        />
      )}

      {peerEditor && (
        <PeerModal
          api={api}
          iface={peerEditor.iface}
          existing={peerEditor.peer}
          canControl={canControl}
          onClose={() => setPeerEditor(null)}
          onSaved={(saved, peerName, generatedClientPrivateKey, peerPublicKey) => {
            if (generatedClientPrivateKey && peerPublicKey) {
              setClientPrivateKeys((prev) => ({ ...prev, [peerPublicKey]: generatedClientPrivateKey }));
            }
            setPeerEditor(null);
            handleSaved(`Saved peer "${peerName}" on ${saved.value.ifname}.`);
          }}
          onConflict={() => void configResource.reload()}
          onSessionLost={onSessionLost}
        />
      )}

      {exportTarget && (
        <PeerExportModal
          api={api}
          iface={exportTarget.iface}
          peer={exportTarget.peer}
          initialClientPrivateKey={clientPrivateKeys[exportTarget.peer.publicKey] ?? ''}
          onClose={() => setExportTarget(null)}
          onSessionLost={onSessionLost}
        />
      )}

      <Modal
        opened={deleteTarget !== null}
        onClose={() => { if (!deleting) setDeleteTarget(null); }}
        title="Delete WireGuard interface"
        centered
        lockScroll={false}
        closeButtonProps={{ 'aria-label': 'Close', disabled: deleting }}
      >
        <Stack>
          {deleteError && <Alert color="red" role="alert" title="Could not delete interface">{deleteError}</Alert>}
          <Text size="sm">
            Delete WireGuard interface <strong>{deleteTarget?.value.name}</strong> ({deleteTarget?.value.ifname}) and all of its peers?
            Changes take effect after applying configuration.
          </Text>
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={deleting} onClick={() => setDeleteTarget(null)}>Cancel</Button>
            <Button color="red" loading={deleting} onClick={() => void confirmDeleteInterface()}>Delete</Button>
          </Group>
        </Stack>
      </Modal>

      <Modal
        opened={deletePeerTarget !== null}
        onClose={() => { if (!deleting) setDeletePeerTarget(null); }}
        title="Delete WireGuard peer"
        centered
        lockScroll={false}
        closeButtonProps={{ 'aria-label': 'Close', disabled: deleting }}
      >
        <Stack>
          {deleteError && <Alert color="red" role="alert" title="Could not delete peer">{deleteError}</Alert>}
          <Text size="sm">
            Remove peer <strong>{deletePeerTarget?.peer.name}</strong> from <strong>{deletePeerTarget?.iface.value.ifname}</strong>?
          </Text>
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={deleting} onClick={() => setDeletePeerTarget(null)}>Cancel</Button>
            <Button color="red" loading={deleting} onClick={() => void confirmDeletePeer()}>Delete</Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}

interface InterfaceModalProps {
  api: ApiClient;
  existing?: SavedObject<WireGuardInterface>;
  defaultIfname: string;
  zones: Zone[];
  segments: Segment[];
  canControl: boolean;
  onClose(): void;
  onSaved(saved: SavedObject<WireGuardInterface>): void;
  onConflict(): void;
  onSessionLost(): void;
}

function InterfaceModal({
  api,
  existing,
  defaultIfname,
  zones,
  segments,
  canControl,
  onClose,
  onSaved,
  onConflict,
  onSessionLost,
}: InterfaceModalProps) {
  const sortedZones = [...zones].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
  const sortedSegments = [...segments].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));

  const [name, setName] = useState(existing?.value.name ?? '');
  const [description, setDescription] = useState(existing?.value.description ?? '');
  const [ifname, setIfname] = useState(existing?.value.ifname ?? defaultIfname);
  const [zone, setZone] = useState<string | null>(existing?.value.zone ?? sortedZones[0]?.id ?? null);
  const [enabled, setEnabled] = useState(existing?.value.enabled ?? true);
  const [listenPort, setListenPort] = useState<number | string>(existing?.value.listenPort ?? 51820);
  const [allowListenPort, setAllowListenPort] = useState(existing?.value.allowListenPort ?? true);
  const [allowListenSegments, setAllowListenSegments] = useState<string[]>(existing?.value.allowListenSegments ?? []);
  const [mtu, setMtu] = useState<number | string>(existing?.value.mtu ?? 1420);
  const [addressesText, setAddressesText] = useState((existing?.value.addresses ?? []).join('\n'));
  const [privateKey, setPrivateKey] = useState('');
  const [derivedPublicKey, setDerivedPublicKey] = useState(existing?.value.publicKey ?? '');
  const [generatingKey, setGeneratingKey] = useState(false);
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);

  const zoneData = sortedZones.map((z) => ({ value: z.id, label: z.name }));
  if (zone && !zoneData.some((item) => item.value === zone)) {
    zoneData.unshift({ value: zone, label: `${zone} (missing zone)` });
  }
  const segmentData = sortedSegments.map((s) => ({ value: s.id, label: s.name }));

  async function generateKey() {
    setGeneratingKey(true);
    setFailure('');
    try {
      const pair = await api.generateWireguardKeypair(false);
      setPrivateKey(pair.privateKey);
      setDerivedPublicKey(pair.publicKey);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      setFailure(saveFailure(error, new Map(), 'keypair'));
    } finally {
      setGeneratingKey(false);
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;

    const trimmedName = name.trim();
    const trimmedIfname = ifname.trim();
    const addresses = parseList(addressesText);
    const portNum = Number(listenPort);
    const mtuNum = mtu === '' ? undefined : Number(mtu);
    const trimmedKey = privateKey.trim();

    const local = new Map<string, string>();
    if (!trimmedName) local.set('name', 'Enter a name for this WireGuard interface.');
    if (!wgIfnamePattern.test(trimmedIfname)) local.set('ifname', 'Interface name must be wg0 through wg99.');
    if (enabled && !zone) local.set('zone', 'Select a firewall zone for an enabled interface.');
    if (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535) {
      local.set('listenPort', 'Listen port must be between 1 and 65535.');
    }
    if (mtuNum !== undefined && (!Number.isInteger(mtuNum) || mtuNum < 1280 || mtuNum > 1500)) {
      local.set('mtu', 'MTU must be between 1280 and 1500.');
    }
    if (enabled && addresses.length === 0) {
      local.set('addresses', 'Enter at least one CIDR address (for example 10.200.0.1/24).');
    } else if (addresses.some((addr) => !addr.includes('/'))) {
      local.set('addresses', 'Each address must be in CIDR notation (for example 10.200.0.1/24).');
    }
    if (!existing && !trimmedKey) {
      local.set('privateKeyRef', 'Generate or enter a WireGuard private key.');
    }

    if (local.size > 0) {
      setErrors(local);
      return;
    }

    const payload: WireGuardInterfaceWrite = {
      name: trimmedName,
      ...(description.trim() ? { description: description.trim() } : {}),
      ifname: trimmedIfname,
      enabled,
      ...(zone ? { zone } : {}),
      listenPort: portNum,
      allowListenPort,
      ...(allowListenPort && allowListenSegments.length > 0 ? { allowListenSegments } : {}),
      ...(mtuNum !== undefined ? { mtu: mtuNum } : {}),
      ...(addresses.length > 0 ? { addresses } : {}),
      privateKeyRef: buildSecretWrite(trimmedKey, existing?.value.privateKeyRef, 'wg_key'),
      ...(existing && (existing.value.peers ?? []).length > 0
        ? { peers: (existing.value.peers ?? []).map(peerToWrite) }
        : {}),
    };

    submitting.current = true;
    setBusy(true);
    setFailure('');
    setErrors(new Map());
    try {
      const saved = await api.saveWireguardInterface(payload, existing);
      onSaved(saved);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (error instanceof ApiError && (error.status === 404 || error.status === 412)) {
        onConflict();
      }
      const fields = fieldErrors(error, ifaceFieldPattern);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'WireGuard interface'));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <Modal
      opened
      onClose={() => { if (!busy) onClose(); }}
      title={existing ? `Edit WireGuard interface (${existing.value.ifname})` : 'Add WireGuard interface'}
      size="lg"
      centered
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
    >
      <form onSubmit={(event) => void submit(event)} noValidate>
        <Fieldset disabled={busy} variant="unstyled">
          <Stack gap="sm">
            {failure && <Alert color="red" role="alert" title="Could not save interface">{failure}</Alert>}
            <Group grow align="flex-start">
              <TextInput
                label="Name"
                placeholder="Remote Access VPN"
                value={name}
                onChange={(event) => setName(event.currentTarget.value)}
                error={errors.get('name')}
                required
              />
              <TextInput
                label="Interface name"
                placeholder="wg0"
                value={ifname}
                onChange={(event) => setIfname(event.currentTarget.value)}
                error={errors.get('ifname')}
                required
              />
            </Group>
            <TextInput
              label="Description"
              placeholder="Optional description"
              value={description}
              onChange={(event) => setDescription(event.currentTarget.value)}
              error={errors.get('description')}
            />
            <Group grow align="flex-start">
              <Select
                label="Firewall zone"
                data={zoneData}
                value={zone}
                onChange={setZone}
                error={errors.get('zone')}
                clearable={!enabled}
                required={enabled}
              />
              <NumberInput
                label="Listen port"
                min={1}
                max={65535}
                allowDecimal={false}
                value={listenPort}
                onChange={setListenPort}
                error={errors.get('listenPort')}
                required
              />
              <NumberInput
                label="MTU"
                min={1280}
                max={1500}
                allowDecimal={false}
                value={mtu}
                onChange={setMtu}
                error={errors.get('mtu')}
              />
            </Group>
            <Textarea
              label="Tunnel addresses (CIDR)"
              description="One IPv4 or IPv6 CIDR per line (for example 10.200.0.1/24)."
              autosize
              minRows={2}
              value={addressesText}
              onChange={(event) => setAddressesText(event.currentTarget.value)}
              error={errors.get('addresses')}
              required={enabled}
            />
            <Stack gap="xs">
              <Group justify="space-between" align="flex-end" wrap="nowrap">
                <PasswordInput
                  style={{ flex: 1 }}
                  label={existing ? 'Private key (leave blank to keep current)' : 'Private key'}
                  placeholder={existing ? 'Stored securely on appliance' : 'Base64 32-byte X25519 key'}
                  value={privateKey}
                  onChange={(event) => setPrivateKey(event.currentTarget.value)}
                  error={errors.get('privateKeyRef')}
                  required={!existing}
                />
                {canControl && (
                  <Button
                    variant="light"
                    loading={generatingKey}
                    onClick={() => void generateKey()}
                  >
                    Generate keypair
                  </Button>
                )}
              </Group>
              {derivedPublicKey && (
                <Text size="xs" c="dimmed">
                  Public key: <Code fz="xs">{derivedPublicKey}</Code>
                </Text>
              )}
            </Stack>
            <Switch
              label="Allow listen port in firewall input chain"
              checked={allowListenPort}
              onChange={(event) => setAllowListenPort(event.currentTarget.checked)}
            />
            {allowListenPort && (
              <MultiSelect
                label="Allow listen port on segments (optional)"
                description="Leave empty to allow incoming WireGuard UDP traffic on all uplinks."
                data={segmentData}
                value={allowListenSegments}
                onChange={setAllowListenSegments}
                error={errors.get('allowListenSegments')}
                clearable
              />
            )}
            <Switch
              label="Enabled"
              checked={enabled}
              onChange={(event) => setEnabled(event.currentTarget.checked)}
            />
            <Group justify="flex-end" gap="xs" mt="xs">
              <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
              <Button type="submit" loading={busy}>{existing ? 'Save interface' : 'Add interface'}</Button>
            </Group>
          </Stack>
        </Fieldset>
      </form>
    </Modal>
  );
}

interface PeerModalProps {
  api: ApiClient;
  iface: SavedObject<WireGuardInterface>;
  existing?: WireGuardPeer;
  canControl: boolean;
  onClose(): void;
  onSaved(
    saved: SavedObject<WireGuardInterface>,
    peerName: string,
    generatedClientPrivateKey?: string,
    peerPublicKey?: string,
  ): void;
  onConflict(): void;
  onSessionLost(): void;
}

function PeerModal({
  api,
  iface,
  existing,
  canControl,
  onClose,
  onSaved,
  onConflict,
  onSessionLost,
}: PeerModalProps) {
  const [name, setName] = useState(existing?.name ?? '');
  const [description, setDescription] = useState(existing?.description ?? '');
  const [publicKey, setPublicKey] = useState(existing?.publicKey ?? '');
  const [generatedPrivateKey, setGeneratedPrivateKey] = useState('');
  const [allowedIpsText, setAllowedIpsText] = useState((existing?.allowedIps ?? []).join('\n'));
  const [endpoint, setEndpoint] = useState(existing?.endpoint ?? '');
  const [keepalive, setKeepalive] = useState<number | string>(existing?.persistentKeepalive ?? '');
  const [routeAllowedIps, setRouteAllowedIps] = useState(existing?.routeAllowedIps ?? true);
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const hasExistingPsk = Boolean(existing?.presharedKeyRef?.secret);
  const [pskMode, setPskMode] = useState<'keep' | 'set' | 'clear'>(hasExistingPsk ? 'keep' : 'clear');
  const [pskValue, setPskValue] = useState('');
  const [generating, setGenerating] = useState(false);
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);

  async function generateClientKeys() {
    setGenerating(true);
    setFailure('');
    try {
      const pair = await api.generateWireguardKeypair(true);
      setPublicKey(pair.publicKey);
      setGeneratedPrivateKey(pair.privateKey);
      if (pair.presharedKey) {
        setPskMode('set');
        setPskValue(pair.presharedKey);
      }
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      setFailure(saveFailure(error, new Map(), 'keypair'));
    } finally {
      setGenerating(false);
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;

    const trimmedName = name.trim();
    const trimmedPub = publicKey.trim();
    const allowedIps = parseList(allowedIpsText);
    const trimmedEndpoint = endpoint.trim();
    const keepaliveNum = keepalive === '' ? undefined : Number(keepalive);
    const trimmedPsk = pskValue.trim();

    const local = new Map<string, string>();
    if (!trimmedName) local.set('name', 'Enter a peer name.');
    if (!trimmedPub) local.set('publicKey', 'Enter or generate a peer public key.');
    if (enabled && allowedIps.length === 0) {
      local.set('allowedIps', 'Enter at least one Allowed IP prefix in CIDR notation.');
    } else if (allowedIps.some((cidr) => !cidr.includes('/'))) {
      local.set('allowedIps', 'Each Allowed IP must be in CIDR notation.');
    }
    if (keepaliveNum !== undefined && (!Number.isInteger(keepaliveNum) || keepaliveNum < 0 || keepaliveNum > 65535)) {
      local.set('persistentKeepalive', 'Persistent keepalive must be between 0 and 65535 seconds.');
    }
    if (pskMode === 'set' && !trimmedPsk) {
      local.set('presharedKeyRef', 'Enter a preshared key or choose None.');
    }

    if (local.size > 0) {
      setErrors(local);
      return;
    }

    const peerWrite: WireGuardPeerWrite = {
      ...(existing ? { id: existing.id } : {}),
      name: trimmedName,
      ...(description.trim() ? { description: description.trim() } : {}),
      enabled,
      publicKey: trimmedPub,
      ...(pskMode === 'keep' && hasExistingPsk
        ? { presharedKeyRef: {} }
        : pskMode === 'set'
          ? { presharedKeyRef: buildSecretWrite(trimmedPsk, existing?.presharedKeyRef, 'wg_psk') }
          : {}),
      ...(allowedIps.length > 0 ? { allowedIps } : {}),
      ...(trimmedEndpoint ? { endpoint: trimmedEndpoint } : {}),
      ...(keepaliveNum !== undefined && keepaliveNum > 0 ? { persistentKeepalive: keepaliveNum } : {}),
      routeAllowedIps,
    };

    const existingPeers = iface.value.peers ?? [];
    const nextPeers = existing
      ? existingPeers.map((p) => (p.id === existing.id ? peerWrite : peerToWrite(p)))
      : [...existingPeers.map(peerToWrite), peerWrite];

    submitting.current = true;
    setBusy(true);
    setFailure('');
    setErrors(new Map());
    try {
      const saved = await api.saveWireguardInterface(interfaceToWrite(iface.value, nextPeers), iface);
      onSaved(saved, trimmedName, generatedPrivateKey || undefined, trimmedPub);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (error instanceof ApiError && (error.status === 404 || error.status === 412)) {
        onConflict();
      }
      const fields = fieldErrors(error, /^\/?(?:peers\/\d+\/)?(name|description|publicKey|presharedKeyRef|allowedIps|endpoint|persistentKeepalive)(?:\/|$)/);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'WireGuard peer'));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <Modal
      opened
      onClose={() => { if (!busy) onClose(); }}
      title={existing ? `Edit peer on ${iface.value.ifname}` : `Add peer to ${iface.value.ifname}`}
      size="lg"
      centered
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
    >
      <form onSubmit={(event) => void submit(event)} noValidate>
        <Fieldset disabled={busy} variant="unstyled">
          <Stack gap="sm">
            {failure && <Alert color="red" role="alert" title="Could not save peer">{failure}</Alert>}
            <Group grow align="flex-start">
              <TextInput
                label="Peer name"
                placeholder="Laptop"
                value={name}
                onChange={(event) => setName(event.currentTarget.value)}
                error={errors.get('name')}
                required
              />
              <TextInput
                label="Description"
                placeholder="Optional note"
                value={description}
                onChange={(event) => setDescription(event.currentTarget.value)}
                error={errors.get('description')}
              />
            </Group>
            <Group justify="space-between" align="flex-end" wrap="nowrap">
              <TextInput
                style={{ flex: 1 }}
                label="Peer public key"
                placeholder="Base64 32-byte X25519 public key"
                value={publicKey}
                onChange={(event) => {
                  setPublicKey(event.currentTarget.value);
                  setGeneratedPrivateKey('');
                }}
                error={errors.get('publicKey')}
                required
              />
              {canControl && (
                <Button variant="light" loading={generating} onClick={() => void generateClientKeys()}>
                  Generate client keys
                </Button>
              )}
            </Group>
            {generatedPrivateKey && (
              <Alert color="blue" role="status" title="Client private key kept in browser memory">
                The generated client private key will not be sent to the appliance. After saving this peer, open Config / QR to download or scan the complete client configuration.
              </Alert>
            )}
            <Textarea
              label="Allowed IPs (CIDR)"
              description="Tunnel address and any remote networks reachable through this peer (one per line)."
              autosize
              minRows={2}
              value={allowedIpsText}
              onChange={(event) => setAllowedIpsText(event.currentTarget.value)}
              error={errors.get('allowedIps')}
              required={enabled}
            />
            <Group grow align="flex-start">
              <TextInput
                label="Endpoint (optional)"
                placeholder="peer.example.com:51820"
                value={endpoint}
                onChange={(event) => setEndpoint(event.currentTarget.value)}
                error={errors.get('endpoint')}
              />
              <NumberInput
                label="Persistent keepalive (seconds)"
                placeholder="25"
                min={0}
                max={65535}
                allowDecimal={false}
                value={keepalive}
                onChange={setKeepalive}
                error={errors.get('persistentKeepalive')}
              />
            </Group>
            <Select
              label="Preshared key (PSK)"
              data={[
                ...(hasExistingPsk
                  ? [{ value: 'keep', label: existing?.presharedKeyRef?.unresolved ? 'Keep existing preshared key (unresolved)' : 'Keep existing preshared key' }]
                  : []),
                { value: 'set', label: hasExistingPsk ? 'Replace preshared key' : 'Set preshared key' },
                { value: 'clear', label: 'None (no preshared key)' },
              ]}
              value={pskMode}
              onChange={(value) => setPskMode((value as 'keep' | 'set' | 'clear') || 'clear')}
            />
            {pskMode === 'set' && (
              <PasswordInput
                label="Preshared key value"
                placeholder="Base64 32-byte preshared key"
                value={pskValue}
                onChange={(event) => setPskValue(event.currentTarget.value)}
                error={errors.get('presharedKeyRef')}
                required
              />
            )}
            <Switch
              label="Automatically add routes for Allowed IPs"
              checked={routeAllowedIps}
              onChange={(event) => setRouteAllowedIps(event.currentTarget.checked)}
            />
            <Switch
              label="Enabled"
              checked={enabled}
              onChange={(event) => setEnabled(event.currentTarget.checked)}
            />
            <Group justify="flex-end" gap="xs" mt="xs">
              <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
              <Button type="submit" loading={busy}>{existing ? 'Save peer' : 'Add peer'}</Button>
            </Group>
          </Stack>
        </Fieldset>
      </form>
    </Modal>
  );
}

interface PeerExportModalProps {
  api: ApiClient;
  iface: WireGuardInterface;
  peer: WireGuardPeer;
  initialClientPrivateKey: string;
  onClose(): void;
  onSessionLost(): void;
}

function PeerExportModal({
  api,
  iface,
  peer,
  initialClientPrivateKey,
  onClose,
  onSessionLost,
}: PeerExportModalProps) {
  const [endpoint, setEndpoint] = useState('');
  const [dnsText, setDnsText] = useState('');
  const [includePsk, setIncludePsk] = useState(Boolean(peer.presharedKeyRef?.isSet && !peer.presharedKeyRef?.unresolved));
  const [clientPrivateKey, setClientPrivateKey] = useState(initialClientPrivateKey);
  const [template, setTemplate] = useState('');
  const [filename, setFilename] = useState(`${iface.ifname}-${peer.name.toLowerCase().replace(/[^a-z0-9_-]+/g, '-')}.conf`);
  const [loading, setLoading] = useState(false);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState('');

  const finalConfig = useMemo(() => {
    if (!template) return '';
    const trimmedKey = clientPrivateKey.trim();
    return trimmedKey ? template.replace('<PRIVATE_KEY>', trimmedKey) : template;
  }, [template, clientPrivateKey]);

  const qrSvg = useMemo(() => {
    if (!finalConfig) return null;
    try {
      return qrSvgPath(encodeQr(finalConfig), 4);
    } catch {
      return null;
    }
  }, [finalConfig]);

  async function generateConfig() {
    setLoading(true);
    setError('');
    setCopied(false);
    try {
      const dns = parseList(dnsText);
      const res = await api.wireguardPeerConfig(iface.id, peer.id, {
        ...(endpoint.trim() ? { endpoint: endpoint.trim() } : {}),
        ...(dns.length > 0 ? { dns } : {}),
        ...(includePsk ? { includePsk: true } : {}),
      });
      setTemplate(res.text);
      if (res.filename) setFilename(res.filename);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        onSessionLost();
        return;
      }
      setError(saveFailure(err, new Map(), 'peer config'));
    } finally {
      setLoading(false);
    }
  }

  function handleDownload() {
    if (!finalConfig) return;
    triggerDownload(new Blob([finalConfig], { type: 'text/plain;charset=utf-8' }), filename);
  }

  async function handleCopy() {
    if (!finalConfig || !navigator.clipboard) return;
    await navigator.clipboard.writeText(finalConfig);
    setCopied(true);
  }

  return (
    <Modal
      opened
      onClose={onClose}
      title={`Client configuration — ${peer.name} (${iface.ifname})`}
      size="lg"
      centered
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close' }}
    >
      <Stack gap="sm">
        {error && <Alert color="red" role="alert" title="Could not generate client config">{error}</Alert>}
        <Group grow align="flex-start">
          <TextInput
            label="Appliance endpoint (optional)"
            placeholder={`vpn.example.com:${iface.listenPort}`}
            value={endpoint}
            onChange={(event) => setEndpoint(event.currentTarget.value)}
          />
          <TextInput
            label="DNS servers (optional)"
            placeholder="10.200.0.1, 1.1.1.1"
            value={dnsText}
            onChange={(event) => setDnsText(event.currentTarget.value)}
          />
        </Group>
        <PasswordInput
          label="Client private key (browser-only, optional)"
          description="Replaces <PRIVATE_KEY> locally in your browser before download or QR rendering. Never sent to the appliance."
          placeholder="Paste or keep freshly generated client private key"
          value={clientPrivateKey}
          onChange={(event) => setClientPrivateKey(event.currentTarget.value)}
        />
        {peer.presharedKeyRef?.isSet && (
          <Checkbox
            label="Include preshared key (recorded in security audit log)"
            disabled={peer.presharedKeyRef.unresolved}
            checked={includePsk}
            onChange={(event) => setIncludePsk(event.currentTarget.checked)}
          />
        )}
        <Group justify="flex-end">
          <Button loading={loading} onClick={() => void generateConfig()}>
            {template ? 'Regenerate config' : 'Generate config'}
          </Button>
        </Group>

        {finalConfig && (
          <Stack gap="sm">
            <Code block fz="xs">{finalConfig}</Code>
            {qrSvg && (
              <Group justify="center">
                <svg
                  role="img"
                  aria-label={`WireGuard QR code for ${peer.name}`}
                  width={220}
                  height={220}
                  viewBox={`0 0 ${qrSvg.viewBoxSize} ${qrSvg.viewBoxSize}`}
                  shapeRendering="crispEdges"
                >
                  <rect width={qrSvg.viewBoxSize} height={qrSvg.viewBoxSize} fill="#ffffff" />
                  <path d={qrSvg.path} fill="#000000" />
                </svg>
              </Group>
            )}
            <Group justify="space-between">
              <Group gap="xs">
                <Button size="sm" onClick={handleDownload}>Download .conf</Button>
                <Button size="sm" variant="default" onClick={() => void handleCopy()}>
                  {copied ? 'Copied' : 'Copy config'}
                </Button>
              </Group>
              <Button size="sm" variant="default" onClick={onClose}>Done</Button>
            </Group>
          </Stack>
        )}
      </Stack>
    </Modal>
  );
}
