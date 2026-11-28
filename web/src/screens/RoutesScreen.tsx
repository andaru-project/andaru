import {
  Alert,
  Badge,
  Button,
  Fieldset,
  Group,
  Modal,
  NumberInput,
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
import { useCallback, useRef, useState, type FormEvent } from 'react';
import { ApiClient, ApiError, fieldErrors, saveFailure } from '../api';
import type { RouteTargetKind, SavedObject, Segment, Session, StaticRoute, WireGuardInterface } from '../types';
import { useResource } from '../useResource';
import { Loaded } from './Loaded';

interface RoutesScreenProps {
  api: ApiClient;
  session: Session;
  onSaved(): void;
  onSessionLost(): void;
}

interface LoadedRoutes {
  routes: Array<SavedObject<StaticRoute>>;
  segments: Segment[];
  wireguard: WireGuardInterface[];
}

const MAX_METRIC = 4_294_967_295;
const routeFieldPattern = /^\/?(name|description|destination|target\/id|target|gateway|metric)(?:\/|$)/;

const targetKindLabels: Record<RouteTargetKind, string> = {
  segment: 'Segment',
  wireguard: 'WireGuard',
  ipsec: 'IPsec',
  openvpn: 'OpenVPN',
};

export function RoutesScreen({ api, session, onSaved, onSessionLost }: RoutesScreenProps) {
  const canWrite = session.capabilities.includes('configuration.write');
  const [notice, setNotice] = useState('');
  const [editor, setEditor] = useState<SavedObject<StaticRoute> | 'new' | undefined>(undefined);
  const [deleteTarget, setDeleteTarget] = useState<SavedObject<StaticRoute> | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState('');

  const load = useCallback(async (): Promise<LoadedRoutes> => {
    const [routesRes, segmentsRes, wgRes] = await Promise.all([
      api.routes(),
      api.segments(),
      api.wireguardInterfaces(),
    ]);
    return {
      routes: routesRes.items,
      segments: segmentsRes.items.map((item) => item.value),
      wireguard: wgRes.items.map((item) => item.value),
    };
  }, [api]);

  const resource = useResource(load, onSessionLost);
  const segments = resource.data?.segments ?? [];
  const wireguard = resource.data?.wireguard ?? [];
  const hasTargets = segments.length > 0 || wireguard.length > 0;

  function handleSaved(message: string) {
    setNotice(message);
    onSaved();
    void resource.reload();
  }

  async function confirmDelete() {
    if (!deleteTarget || deleting) return;
    setDeleting(true);
    setDeleteError('');
    try {
      await api.deleteRoute(deleteTarget.value.id, deleteTarget.version);
      const deletedName = deleteTarget.value.name;
      setDeleteTarget(null);
      handleSaved(`Deleted route "${deletedName}".`);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (error instanceof ApiError && (error.status === 404 || error.status === 412)) {
        void resource.reload();
      }
      setDeleteError(saveFailure(error, new Map(), 'route'));
    } finally {
      setDeleting(false);
    }
  }

  return (
    <Stack gap="lg">
      <Group justify="space-between" align="flex-end" wrap="wrap">
        <div>
          <Title order={2}>Static Routes</Title>
          <Text size="sm" c="dimmed">
            Route IPv4 and IPv6 prefixes through configured network segments or VPN tunnel interfaces.
          </Text>
        </div>
        <Group gap="xs">
          <Button
            size="sm"
            variant="default"
            loading={resource.loading && resource.data !== undefined}
            onClick={() => void resource.reload()}
          >
            Refresh
          </Button>
          {canWrite && (
            <Button size="sm" disabled={!hasTargets} onClick={() => setEditor('new')}>
              Add route
            </Button>
          )}
        </Group>
      </Group>

      {notice && (
        <Alert color="green" role="status" withCloseButton onClose={() => setNotice('')}>
          {notice}
        </Alert>
      )}

      <Loaded resource={resource} what="static routes">
        {({ routes, segments: loadedSegments, wireguard: loadedWireguard }) => {
          const segmentsById = new Map(loadedSegments.map((s) => [s.id, s.name]));
          const wireguardById = new Map(loadedWireguard.map((w) => [w.id, `${w.name} (${w.ifname})`]));
          const sortedRoutes = [...routes].sort((a, b) =>
            a.value.destination.localeCompare(b.value.destination, undefined, { numeric: true })
            || a.value.name.localeCompare(b.value.name, undefined, { sensitivity: 'base' }));

          if (sortedRoutes.length === 0) {
            return (
              <Alert role="status" title="No static routes configured">
                {!canWrite
                  ? 'No static routes are currently configured.'
                  : loadedSegments.length === 0 && loadedWireguard.length === 0
                    ? 'Configure at least one network segment or VPN interface before adding static routes.'
                    : 'Add a static route to direct traffic for a specific IPv4 or IPv6 prefix to a segment or VPN interface.'}
              </Alert>
            );
          }

          return (
            <ScrollArea>
              <Table className="data-table" striped highlightOnHover>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Name</Table.Th>
                    <Table.Th>Destination</Table.Th>
                    <Table.Th>Target</Table.Th>
                    <Table.Th>Gateway</Table.Th>
                    <Table.Th>Metric</Table.Th>
                    <Table.Th>Status</Table.Th>
                    {canWrite && <Table.Th>Actions</Table.Th>}
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {sortedRoutes.map((saved) => {
                    const r = saved.value;
                    return (
                      <Table.Tr key={r.id}>
                        <Table.Td data-label="Name">
                          <Stack gap={2}>
                            <Text fw={600} size="sm">{r.name}</Text>
                            {r.description && <Text size="xs" c="dimmed">{r.description}</Text>}
                          </Stack>
                        </Table.Td>
                        <Table.Td data-label="Destination">
                          <Text size="sm" ff="monospace">{r.destination}</Text>
                        </Table.Td>
                        <Table.Td data-label="Target">
                          <Group gap="xs" wrap="nowrap">
                            <Badge size="xs" variant="light">{targetKindLabels[r.target.kind]}</Badge>
                            <Text size="sm">
                              {r.target.kind === 'wireguard'
                                ? (wireguardById.get(r.target.id) ?? r.target.id)
                                : (segmentsById.get(r.target.id) ?? r.target.id)}
                            </Text>
                          </Group>
                        </Table.Td>
                        <Table.Td data-label="Gateway">
                          {r.gateway ? (
                            <Text size="sm" ff="monospace">{r.gateway}</Text>
                          ) : (
                            <Text size="xs" c="dimmed">Direct / interface</Text>
                          )}
                        </Table.Td>
                        <Table.Td data-label="Metric">
                          {r.metric !== undefined ? (
                            <Text size="sm" className="tabular">{r.metric}</Text>
                          ) : (
                            <Text size="xs" c="dimmed">Default</Text>
                          )}
                        </Table.Td>
                        <Table.Td data-label="Status">
                          <Badge size="sm" color={r.enabled ? 'green' : 'gray'} variant="light">
                            {r.enabled ? 'Enabled' : 'Disabled'}
                          </Badge>
                        </Table.Td>
                        {canWrite && (
                          <Table.Td data-label="Actions">
                            <Group gap="xs" wrap="nowrap">
                              <Button
                                size="xs"
                                variant="default"
                                aria-label={`Edit route ${r.name}`}
                                onClick={() => setEditor(saved)}
                              >
                                Edit
                              </Button>
                              <Button
                                size="xs"
                                variant="light"
                                color="red"
                                aria-label={`Delete route ${r.name}`}
                                onClick={() => {
                                  setDeleteError('');
                                  setDeleteTarget(saved);
                                }}
                              >
                                Delete
                              </Button>
                            </Group>
                          </Table.Td>
                        )}
                      </Table.Tr>
                    );
                  })}
                </Table.Tbody>
              </Table>
            </ScrollArea>
          );
        }}
      </Loaded>

      {editor !== undefined && (
        <RouteModal
          api={api}
          existing={editor === 'new' ? undefined : editor}
          segments={segments}
          wireguard={wireguard}
          onClose={() => setEditor(undefined)}
          onSaved={(saved) => {
            setEditor(undefined);
            handleSaved(`Saved route "${saved.value.name}".`);
          }}
          onConflict={() => void resource.reload()}
          onSessionLost={onSessionLost}
        />
      )}

      <Modal
        opened={deleteTarget !== null}
        onClose={() => { if (!deleting) setDeleteTarget(null); }}
        title="Delete static route"
        centered
        lockScroll={false}
        closeButtonProps={{ 'aria-label': 'Close', disabled: deleting }}
      >
        <Stack>
          {deleteError && <Alert color="red" role="alert" title="Could not delete route">{deleteError}</Alert>}
          <Text size="sm">
            Delete static route <strong>{deleteTarget?.value.name}</strong> ({deleteTarget?.value.destination})?
            Changes take effect after applying configuration.
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

interface RouteModalProps {
  api: ApiClient;
  existing?: SavedObject<StaticRoute>;
  segments: Segment[];
  wireguard: WireGuardInterface[];
  onClose(): void;
  onSaved(saved: SavedObject<StaticRoute>): void;
  onConflict(): void;
  onSessionLost(): void;
}

function RouteModal({ api, existing, segments, wireguard, onClose, onSaved, onConflict, onSessionLost }: RouteModalProps) {
  const sortedSegments = [...segments].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
  const sortedWireguard = [...wireguard].sort((a, b) => a.ifname.localeCompare(b.ifname, undefined, { numeric: true }));
  const defaultKind: RouteTargetKind = existing?.value.target.kind
    ?? (sortedSegments.length > 0 ? 'segment' : 'wireguard');
  const [name, setName] = useState(existing?.value.name ?? '');
  const [description, setDescription] = useState(existing?.value.description ?? '');
  const [destination, setDestination] = useState(existing?.value.destination ?? '');
  const [targetKind, setTargetKind] = useState<RouteTargetKind>(defaultKind);
  const [targetId, setTargetId] = useState(
    existing?.value.target.id
      ?? (defaultKind === 'wireguard' ? sortedWireguard[0]?.id : sortedSegments[0]?.id)
      ?? '',
  );
  const [gateway, setGateway] = useState(existing?.value.gateway ?? '');
  const [metric, setMetric] = useState<number | string>(existing?.value.metric ?? '');
  const [enabled, setEnabled] = useState(existing?.value.enabled ?? true);
  const [errors, setErrors] = useState(new Map<string, string>());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);

  const isPointToPoint = targetKind === 'wireguard' || targetKind === 'ipsec';
  const kindOptions = [
    ...(sortedSegments.length > 0 || targetKind === 'segment' ? [{ value: 'segment', label: 'Segment' }] : []),
    ...(sortedWireguard.length > 0 || targetKind === 'wireguard' ? [{ value: 'wireguard', label: 'WireGuard' }] : []),
  ];

  const targetData = targetKind === 'wireguard'
    ? sortedWireguard.map((w) => ({ value: w.id, label: `${w.name} (${w.ifname})` }))
    : sortedSegments.map((s) => ({ value: s.id, label: s.name }));
  if (targetId && !targetData.some((i) => i.value === targetId)) {
    targetData.unshift({ value: targetId, label: `${targetId} (missing ${targetKind})` });
  }

  function handleTargetKindChange(nextKind: RouteTargetKind) {
    setTargetKind(nextKind);
    if (nextKind === 'wireguard') {
      setTargetId(sortedWireguard[0]?.id ?? '');
      setGateway('');
    } else {
      setTargetId(sortedSegments[0]?.id ?? '');
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;

    const nextErrors = new Map<string, string>();
    if (!name.trim()) nextErrors.set('name', 'Enter a route name.');
    if (!destination.trim() || !destination.includes('/')) {
      nextErrors.set('destination', 'Enter an IPv4 or IPv6 destination in CIDR notation.');
    }
    if (!targetId.trim()) {
      nextErrors.set('target/id', targetKind === 'wireguard' ? 'Select a WireGuard interface.' : 'Select a target segment.');
    }

    let parsedMetric: number | undefined;
    if (metric !== '') {
      const m = Number(metric);
      if (!Number.isInteger(m) || m < 1 || m > MAX_METRIC) {
        nextErrors.set('metric', `Metric must be an integer between 1 and ${MAX_METRIC}.`);
      } else {
        parsedMetric = m;
      }
    }

    if (nextErrors.size > 0) {
      setFailure('');
      setErrors(nextErrors);
      return;
    }

    const payload: Omit<StaticRoute, 'id'> = {
      name: name.trim(),
      ...(description.trim() ? { description: description.trim() } : {}),
      destination: destination.trim(),
      target: { kind: targetKind, id: targetId.trim() },
      ...(!isPointToPoint && gateway.trim() ? { gateway: gateway.trim() } : {}),
      ...(parsedMetric !== undefined ? { metric: parsedMetric } : {}),
      enabled,
    };

    submitting.current = true;
    setBusy(true);
    setErrors(new Map());
    setFailure('');
    try {
      const saved = await api.saveRoute(payload, existing);
      onSaved(saved);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (error instanceof ApiError && (error.status === 404 || error.status === 412)) {
        onConflict();
      }
      const fields = fieldErrors(error, routeFieldPattern);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'route'));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <Modal
      opened
      onClose={() => { if (!busy) onClose(); }}
      title={existing ? 'Edit static route' : 'Add static route'}
      centered
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
    >
      <form onSubmit={submit} noValidate>
        <Stack>
          {failure && <Alert color="red" role="alert" title="Static route was not saved">{failure}</Alert>}
          <Fieldset variant="unstyled" disabled={busy}>
            <Stack>
              <TextInput
                label="Name"
                placeholder="Branch office LAN"
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
              <TextInput
                label="Destination CIDR"
                description="IPv4 or IPv6 prefix (canonicalized to network address on save)."
                placeholder="10.20.0.0/24 or 2001:db8:2::/48"
                classNames={{ input: 'mono-input' }}
                required
                value={destination}
                error={errors.get('destination')}
                onChange={(e) => setDestination(e.currentTarget.value)}
              />
              {kindOptions.length > 1 && (
                <Select
                  label="Target kind"
                  required
                  allowDeselect={false}
                  data={kindOptions}
                  value={targetKind}
                  onChange={(val) => {
                    if (val === 'segment' || val === 'wireguard') handleTargetKindChange(val);
                  }}
                />
              )}
              <Select
                label={targetKind === 'wireguard' ? 'Target WireGuard interface' : 'Target segment'}
                required
                searchable
                allowDeselect={false}
                data={targetData}
                value={targetId || null}
                error={errors.get('target/id') ?? errors.get('target')}
                onChange={(val) => setTargetId(val ?? '')}
              />
              {!isPointToPoint && (
                <TextInput
                  label="Gateway (optional)"
                  description="Optional next-hop IPv4 or IPv6 address matching the destination family."
                  placeholder="192.0.2.1 or fe80::1"
                  classNames={{ input: 'mono-input' }}
                  value={gateway}
                  error={errors.get('gateway')}
                  onChange={(e) => setGateway(e.currentTarget.value)}
                />
              )}
              <NumberInput
                label="Metric (optional)"
                description="Route priority (1 to 4294967295). Lower metrics are preferred."
                placeholder="Default"
                allowDecimal={false}
                min={1}
                max={MAX_METRIC}
                value={metric}
                error={errors.get('metric')}
                onChange={setMetric}
              />
              <Switch
                label="Enable route"
                checked={enabled}
                onChange={(e) => setEnabled(e.currentTarget.checked)}
              />
            </Stack>
          </Fieldset>
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={busy} onClick={onClose}>Cancel</Button>
            <Button type="submit" loading={busy}>{existing ? 'Save route' : 'Add route'}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
