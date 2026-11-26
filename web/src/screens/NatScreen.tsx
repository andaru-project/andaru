import { DndContext, KeyboardSensor, PointerSensor, closestCenter, useSensor, useSensors, type Announcements, type DragEndEvent, type UniqueIdentifier } from '@dnd-kit/core';
import { SortableContext, arrayMove, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { ActionIcon, Alert, Anchor, Badge, Button, Group, Modal, Paper, ScrollArea, SegmentedControl, Stack, Switch, Table, Tabs, Text, TextInput, Title, VisuallyHidden } from '@mantine/core';
import { useCallback, useEffect, useRef, useState } from 'react';
import { Link } from 'react-router';
import { ApiError, reportError, saveFailure, type ApiClient } from '../api';
import type { OneToOneNat, OutboundNat, OutboundNatMode, PortForward, SavedObject, Session, Snapshot } from '../types';
import { useResource } from '../useResource';
import { Address } from './Address';
import { Loaded } from './Loaded';
import { EditMark, Grip } from './Marks';
import { OneToOneEditor, PortForwardEditor } from './NatEditors';
import { ZoneBadge } from './ZoneBadge';

interface NatScreenProps {
  api: ApiClient;
  session: Session;
  onSaved(): void;
  onSessionLost(): void;
}

const protocolLabel: Record<PortForward['protocol'], string> = {
  tcp: 'TCP',
  udp: 'UDP',
  'tcp-udp': 'TCP/UDP',
};

function orderedEntries<T extends { id: string }>(
  items: Array<SavedObject<T>>,
  pendingOrder: string[] | undefined,
): { order: string[]; byId: Map<string, SavedObject<T>>; ordered: Array<SavedObject<T>> } {
  const savedOrder = items.map((item) => item.value.id);
  const order = pendingOrder
    ? [...pendingOrder, ...savedOrder.filter((id) => !pendingOrder.includes(id))]
    : savedOrder;
  const byId = new Map(items.map((item) => [item.value.id, item]));
  const ordered = order.flatMap((id) => byId.get(id) ?? []);
  return { order, byId, ordered };
}

function NatOrderToolbar({
  description, canWrite, unlocked, filtered, count, addLabel, onToggleUnlock, onAdd,
}: {
  description: string;
  canWrite: boolean;
  unlocked: boolean;
  filtered: boolean;
  count: number;
  addLabel: string;
  onToggleUnlock(): void;
  onAdd(): void;
}) {
  return (
    <Group justify="space-between" align="flex-end" wrap="wrap">
      <Text size="sm" c="dimmed">{description}</Text>
      {canWrite && (
        <Group gap="xs">
          <Button
            size="xs"
            variant="default"
            disabled={!unlocked && (filtered || count < 2)}
            leftSection={<EditMark editing={unlocked} />}
            onClick={onToggleUnlock}
          >
            {unlocked ? 'Done' : 'Edit order'}
          </Button>
          <Button size="xs" disabled={unlocked} onClick={onAdd}>{addLabel}</Button>
        </Group>
      )}
    </Group>
  );
}

function PortForwardRow({
  item, position, zones, addresses, canWrite, unlocked, busy, onToggle, onEdit, onDelete,
}: {
  item: SavedObject<PortForward>;
  position: number;
  zones: Map<string, string>;
  addresses: Map<string, string>;
  canWrite: boolean;
  unlocked: boolean;
  busy: boolean;
  onToggle(): void;
  onEdit(): void;
  onDelete(): void;
}) {
  const pf = item.value;
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({ id: pf.id, disabled: !unlocked || busy });
  const title = `port forward ${position}, ${pf.name}`;

  return (
    <Table.Tr ref={setNodeRef} className="rule" data-disabled={!pf.enabled || undefined} data-dragging={isDragging || undefined} style={{ transform: CSS.Translate.toString(transform), transition }}>
      <Table.Td data-label="Position">
        <Group gap="xs" wrap="nowrap">
          {unlocked && (
            <ActionIcon className="widget-grip" size="lg" variant="subtle" color="gray" id={`grip-${pf.id}`} data-disabled={busy || undefined} ref={setActivatorNodeRef} {...attributes} {...listeners} aria-label={`Move ${title}`}>
              <Grip />
            </ActionIcon>
          )}
          <Text size="sm" className="tabular">{position}</Text>
        </Group>
      </Table.Td>
      <Table.Td data-label="Enabled">
        {canWrite && !unlocked
          ? <Switch checked={pf.enabled} disabled={busy} onChange={onToggle} aria-label={`Enable ${title}`} />
          : <Text size="sm">{pf.enabled ? 'on' : 'off'}</Text>}
      </Table.Td>
      <Table.Td data-label="Ingress">
        <Group gap={6}>
          {pf.ingressZones.map((id) => <ZoneBadge key={id} name={zones.get(id) ?? 'unknown zone'} />)}
        </Group>
      </Table.Td>
      <Table.Td data-label="Match">
        <Stack gap={2}>
          <Group gap={6} wrap="wrap">
            <Badge size="xs" variant="light" color="gray" className="plain-badge">{protocolLabel[pf.protocol]}</Badge>
            <Text size="sm" ff="monospace">{pf.ports}</Text>
            {pf.family === 'ipv6' && <Badge size="xs" variant="light" color="teal" className="plain-badge">IPv6</Badge>}
          </Group>
          <Group gap={4} wrap="wrap">
            {pf.from.length > 0 && (
              <Text size="xs" c="dimmed">
                from {pf.fromNegated ? 'not ' : ''}{pf.from.map((id) => addresses.get(id) ?? 'unknown alias').join(', ')}
              </Text>
            )}
            <Text size="xs" c="dimmed">
              to {pf.toNegated ? 'not ' : ''}{pf.to.self ? 'this firewall' : (addresses.get(pf.to.address ?? '') ?? 'unknown alias')}
            </Text>
          </Group>
        </Stack>
      </Table.Td>
      <Table.Td data-label="Target">
        {pf.noRedirect ? (
          <Badge size="xs" variant="light" color="gray" className="plain-badge">No redirect</Badge>
        ) : (
          <Stack gap={2}>
            <Group gap={4} wrap="wrap">
              {pf.target?.self
                ? <Badge size="xs" variant="filled" color="dark" tt="none">This firewall</Badge>
                : <Address value={pf.target?.address ?? ''} />}
              {pf.targetPort !== undefined && <Text size="xs" ff="monospace">:{pf.targetPort}</Text>}
            </Group>
            <Group gap={4} wrap="wrap">
              {pf.filterRule === 'auto' && <Badge size="xs" variant="light" color="green" className="plain-badge">Auto rule</Badge>}
              {pf.reflection && <Badge size="xs" variant="outline" color="gray">Reflection</Badge>}
              {pf.log && <Badge size="xs" variant="outline" color="gray">Logs</Badge>}
            </Group>
          </Stack>
        )}
      </Table.Td>
      <Table.Td data-label="Name">
        <Text size="sm" fw={600}>{pf.name}</Text>
        {pf.description && <Text size="sm" c="dimmed">{pf.description}</Text>}
      </Table.Td>
      {canWrite && (
        <Table.Td className="rule-actions">
          <Group gap="xs" wrap="nowrap">
            <Button size="compact-xs" variant="default" disabled={busy || unlocked} aria-label={`Edit ${title}`} onClick={onEdit}>Edit</Button>
            <Button size="compact-xs" variant="subtle" color="red" disabled={busy || unlocked} aria-label={`Delete ${title}`} onClick={onDelete}>Delete</Button>
          </Group>
        </Table.Td>
      )}
    </Table.Tr>
  );
}

function OneToOneRow({
  item, position, zones, addresses, canWrite, unlocked, busy, onToggle, onEdit, onDelete,
}: {
  item: SavedObject<OneToOneNat>;
  position: number;
  zones: Map<string, string>;
  addresses: Map<string, string>;
  canWrite: boolean;
  unlocked: boolean;
  busy: boolean;
  onToggle(): void;
  onEdit(): void;
  onDelete(): void;
}) {
  const entry = item.value;
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({ id: entry.id, disabled: !unlocked || busy });
  const title = `1:1 entry ${position}, ${entry.name}`;

  return (
    <Table.Tr ref={setNodeRef} className="rule" data-disabled={!entry.enabled || undefined} data-dragging={isDragging || undefined} style={{ transform: CSS.Translate.toString(transform), transition }}>
      <Table.Td data-label="Position">
        <Group gap="xs" wrap="nowrap">
          {unlocked && (
            <ActionIcon className="widget-grip" size="lg" variant="subtle" color="gray" id={`grip-${entry.id}`} data-disabled={busy || undefined} ref={setActivatorNodeRef} {...attributes} {...listeners} aria-label={`Move ${title}`}>
              <Grip />
            </ActionIcon>
          )}
          <Text size="sm" className="tabular">{position}</Text>
        </Group>
      </Table.Td>
      <Table.Td data-label="Enabled">
        {canWrite && !unlocked
          ? <Switch checked={entry.enabled} disabled={busy} onChange={onToggle} aria-label={`Enable ${title}`} />
          : <Text size="sm">{entry.enabled ? 'on' : 'off'}</Text>}
      </Table.Td>
      <Table.Td data-label="Zone">
        <ZoneBadge name={zones.get(entry.zone) ?? 'unknown zone'} />
      </Table.Td>
      <Table.Td data-label="External">
        {entry.noBinat
          ? <Badge size="xs" variant="light" color="gray" className="plain-badge">No 1:1</Badge>
          : <Address value={entry.external ?? ''} />}
      </Table.Td>
      <Table.Td data-label="Internal">
        <Address value={entry.internal} />
      </Table.Td>
      <Table.Td data-label="Destination">
        <Group gap={4} wrap="wrap">
          {entry.noBinat || entry.destination.length === 0 ? (
            <Text size="sm" c="dimmed">any</Text>
          ) : (
            <>
              {entry.destinationNegated && <Text size="sm" fw={600}>not</Text>}
              {entry.destination.map((id) => <Badge key={id} className="alias" variant="default" tt="none">{addresses.get(id) ?? 'unknown alias'}</Badge>)}
            </>
          )}
          {entry.reflection && <Badge size="xs" variant="outline" color="gray">Reflection</Badge>}
        </Group>
      </Table.Td>
      <Table.Td data-label="Name">
        <Text size="sm" fw={600}>{entry.name}</Text>
        {entry.description && <Text size="sm" c="dimmed">{entry.description}</Text>}
      </Table.Td>
      {canWrite && (
        <Table.Td className="rule-actions">
          <Group gap="xs" wrap="nowrap">
            <Button size="compact-xs" variant="default" disabled={busy || unlocked} aria-label={`Edit ${title}`} onClick={onEdit}>Edit</Button>
            <Button size="compact-xs" variant="subtle" color="red" disabled={busy || unlocked} aria-label={`Delete ${title}`} onClick={onDelete}>Delete</Button>
          </Group>
        </Table.Td>
      )}
    </Table.Tr>
  );
}

export function NatScreen({ api, session, onSaved, onSessionLost }: NatScreenProps) {
  const canWrite = session.capabilities.includes('configuration.write');
  const readSeq = useRef(0);
  const load = useCallback(async () => {
    const seq = ++readSeq.current;
    for (let tries = 0; ; tries++) {
      const [forwards, oneToOne, outbound, snapshot] = await Promise.all([
        api.portForwards(),
        api.oneToOneNats(),
        api.outboundNat(),
        api.config(),
      ]);
      if ((forwards.savedVersion === snapshot.savedVersion && oneToOne.savedVersion === snapshot.savedVersion) || tries === 2) {
        return { forwards, oneToOne, outbound, snapshot, seq };
      }
    }
  }, [api]);

  const resource = useResource(load, onSessionLost);

  const [tab, setTab] = useState<string>('port-forwards');
  const [filter, setFilter] = useState('');
  const [unlocked, setUnlocked] = useState(false);
  const [editingForward, setEditingForward] = useState<SavedObject<PortForward> | 'new'>();
  const [editingOneToOne, setEditingOneToOne] = useState<SavedObject<OneToOneNat> | 'new'>();
  const [deleting, setDeleting] = useState<
    | { kind: 'port-forward'; item: SavedObject<PortForward>; position: number }
    | { kind: 'one-to-one'; item: SavedObject<OneToOneNat>; position: number }
  >();
  const [moved, setMoved] = useState<{ tab: 'port-forwards' | 'one-to-one'; order: string[] }>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  // Outbound NAT singleton state.
  const [outboundOverride, setOutboundOverride] = useState<{ value: SavedObject<OutboundNat> | null; afterSeq: number } | undefined>();
  const [outboundMode, setOutboundMode] = useState<OutboundNatMode>('none');
  const [outboundBaseVersion, setOutboundBaseVersion] = useState('0');
  const [outboundDirty, setOutboundDirty] = useState(false);
  const [seenData, setSeenData] = useState<{ snapshot: Snapshot; outbound: SavedObject<OutboundNat> | null; seq: number } | undefined>();
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);

  if (resource.data && resource.data !== seenData && !busy) {
    setSeenData(resource.data);
    const supersedes = !outboundOverride || resource.data.seq > outboundOverride.afterSeq;
    if (supersedes) {
      if (outboundOverride) setOutboundOverride(undefined);
      if (!outboundDirty) {
        setOutboundMode(resource.data.outbound?.value.mode ?? 'none');
        setOutboundBaseVersion(resource.data.outbound?.version ?? '0');
      }
    }
  }

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  async function change(send: () => Promise<unknown>, conflictNoun: string) {
    if (busy) return;
    setBusy(true);
    setError('');
    try {
      await send();
      onSaved();
    } catch (requestError) {
      if (requestError instanceof ApiError && requestError.status === 412) {
        setError(`The ${conflictNoun} changed elsewhere. The list now shows what is saved; nothing of this change was.`);
      } else {
        reportError(requestError, onSessionLost, setError);
      }
      if (requestError instanceof ApiError && requestError.status === 401) return;
    }
    await resource.reload();
    if (mounted.current) {
      setMoved(undefined);
      setBusy(false);
    }
  }

  async function saveOutbound() {
    if (!canWrite || busy) return;
    setBusy(true);
    setError('');
    try {
      const saved = await api.saveOutboundNat({ mode: outboundMode }, outboundBaseVersion);
      if (!mounted.current) return;
      setOutboundOverride({ value: saved, afterSeq: readSeq.current });
      setOutboundMode(saved.value.mode);
      setOutboundBaseVersion(saved.version);
      setOutboundDirty(false);
      onSaved();
      await resource.reload();
    } catch (requestError) {
      if (requestError instanceof ApiError && requestError.status === 401) return onSessionLost();
      if (mounted.current) setError(saveFailure(requestError, new Map(), 'outbound NAT setting'));
      if (requestError instanceof ApiError && requestError.status === 412) {
        await resource.reload();
      }
    } finally {
      if (mounted.current) setBusy(false);
    }
  }

  function switchTab(next: string | null) {
    if (!next || busy) return;
    setTab(next);
    setFilter('');
    setUnlocked(false);
    setMoved(undefined);
  }

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div>
          <Title order={2}>NAT</Title>
          <Text c="dimmed">Port forwards, 1:1 address mappings, and outbound masquerade.</Text>
        </div>
        <Button size="xs" variant="default" loading={resource.loading} onClick={() => void resource.reload()}>Refresh</Button>
      </Group>

      {error && <Alert color="red" role="alert" title="NAT was not changed" withCloseButton onClose={() => setError('')}>{error}</Alert>}

      <Loaded resource={resource} what="NAT settings">
        {({ forwards, oneToOne, outbound, snapshot }) => {
          const zones = new Map(snapshot.value.zones.map((z) => [z.id, z.name]));
          const addresses = new Map((snapshot.value.aliases?.addresses ?? []).map((a) => [a.id, a.name]));
          const management = snapshot.value.segments.find((s) => s.id === snapshot.value.management?.segment);
          const effectiveOutbound = outboundOverride ? outboundOverride.value : outbound;
          const savedOutboundMode: OutboundNatMode = effectiveOutbound?.value.mode ?? 'none';
          const outboundServerVersion = effectiveOutbound?.version ?? '0';
          const outboundRemoteChanged = outboundDirty && outboundServerVersion !== outboundBaseVersion;

          const words = filter.toLowerCase().split(/\s+/).filter(Boolean);

          const { order: pfOrder, byId: pfById, ordered: pfItems } = orderedEntries(
            forwards.items,
            moved?.tab === 'port-forwards' ? moved.order : undefined,
          );
          const pfFiltered = filter.trim() !== '' && forwards.items.length > 0;
          const pfShown = pfItems
            .map((item, idx) => ({ item, position: idx + 1 }))
            .filter(({ item }) => {
              if (words.length === 0) return true;
              const pf = item.value;
              const text = [
                pf.name, pf.description ?? '', pf.protocol, pf.ports, pf.family ?? 'ipv4',
                pf.target?.address ?? (pf.target?.self ? 'this firewall' : ''),
                pf.targetPort !== undefined ? String(pf.targetPort) : '',
                ...pf.ingressZones.map((id) => zones.get(id) ?? ''),
                ...pf.from.map((id) => addresses.get(id) ?? ''),
                pf.to.address ? (addresses.get(pf.to.address) ?? '') : 'this firewall',
              ].join(' ').toLowerCase();
              return words.every((w) => text.includes(w));
            });

          const { order: o2oOrder, byId: o2oById, ordered: o2oItems } = orderedEntries(
            oneToOne.items,
            moved?.tab === 'one-to-one' ? moved.order : undefined,
          );
          const o2oFiltered = filter.trim() !== '' && oneToOne.items.length > 0;
          const o2oShown = o2oItems
            .map((item, idx) => ({ item, position: idx + 1 }))
            .filter(({ item }) => {
              if (words.length === 0) return true;
              const entry = item.value;
              const text = [
                entry.name, entry.description ?? '', entry.external ?? '', entry.internal,
                zones.get(entry.zone) ?? '',
                ...entry.destination.map((id) => addresses.get(id) ?? ''),
              ].join(' ').toLowerCase();
              return words.every((w) => text.includes(w));
            });

          const makeAnnouncements = (order: string[], titleOf: (id: UniqueIdentifier) => string): Announcements => {
            const place = (id: UniqueIdentifier) => `position ${order.indexOf(String(id)) + 1} of ${order.length}`;
            return {
              onDragStart: ({ active }) => `Picked up ${titleOf(active.id)}, at ${place(active.id)}.`,
              onDragOver: ({ active, over }) => (over ? `${titleOf(active.id)} is at ${place(over.id)}.` : undefined),
              onDragEnd: ({ active, over }) => (over ? `Dropped ${titleOf(active.id)} at ${place(over.id)}.` : `${titleOf(active.id)} was put back.`),
              onDragCancel: ({ active }) => `${titleOf(active.id)} was put back at ${place(active.id)}.`,
            };
          };
          const instructions = { draggable: 'To move an entry, press Space or Enter on its grip, move with the arrow keys, then press Space or Enter to drop it.' };

          function handleDrop(
            tabKey: 'port-forwards' | 'one-to-one',
            order: string[],
            version: string,
            noun: string,
            saveOrder: (ids: string[], ver: string) => Promise<unknown>,
          ) {
            return ({ active, over }: DragEndEvent) => {
              if (busy || !over || active.id === over.id) return;
              const next = arrayMove(order, order.indexOf(String(active.id)), order.indexOf(String(over.id)));
              setMoved({ tab: tabKey, order: next });
              void change(() => saveOrder(next, version), noun).then(() => {
                if (document.activeElement === document.body) document.getElementById(`grip-${active.id}`)?.focus();
              });
            };
          }

          return (
            <Stack>
              {!management && (
                <Alert color="orange" title="No management segment is set">
                  Without one the packet filter is off, and a configuration that holds rules or NAT is refused when it is applied. The management segment is set with the{' '}
                  <Anchor component={Link} to="/network/interfaces">interfaces</Anchor>.
                </Alert>
              )}

              <Tabs value={tab} onChange={switchTab}>
                <Tabs.List>
                  <Tabs.Tab value="port-forwards">Port forwards ({forwards.items.length})</Tabs.Tab>
                  <Tabs.Tab value="one-to-one">1:1 NAT ({oneToOne.items.length})</Tabs.Tab>
                  <Tabs.Tab value="outbound">Outbound</Tabs.Tab>
                </Tabs.List>

                <Tabs.Panel value="port-forwards" pt="md">
                  <Stack>
                    <NatOrderToolbar
                      description="Tried from the top: the first matching port forward decides."
                      canWrite={canWrite}
                      unlocked={unlocked}
                      filtered={pfFiltered}
                      count={forwards.items.length}
                      addLabel="Add port forward"
                      onToggleUnlock={() => setUnlocked(!unlocked)}
                      onAdd={() => setEditingForward('new')}
                    />

                    {forwards.items.length === 0 ? (
                      <Alert role="status" title="No port forwards">No port forward is saved.</Alert>
                    ) : (
                      <>
                        <TextInput
                          label="Filter port forwards"
                          placeholder="Name, zone, port, target, or alias"
                          value={filter}
                          onChange={(e) => { setFilter(e.currentTarget.value); setUnlocked(false); }}
                        />
                        {canWrite && pfFiltered && forwards.items.length > 1 && (
                          <Text c="dimmed" size="sm">The order is locked while the filter is in use.</Text>
                        )}
                        {unlocked && <Text c="dimmed" size="sm">Drag an entry by its grip to move it. Choose Done to edit entries again.</Text>}
                        <DndContext
                          sensors={sensors}
                          collisionDetection={closestCenter}
                          onDragEnd={handleDrop('port-forwards', pfOrder, forwards.version, 'port forwards', (ids, ver) => api.orderPortForwards(ids, ver))}
                          accessibility={{
                            announcements: makeAnnouncements(pfOrder, (id) => pfById.get(String(id))?.value.name ?? 'the port forward'),
                            screenReaderInstructions: instructions,
                          }}
                        >
                          <SortableContext items={pfOrder} strategy={verticalListSortingStrategy}>
                            <ScrollArea>
                              <Table className="data-table rules" verticalSpacing="xs">
                                <Table.Thead>
                                  <Table.Tr>
                                    <Table.Th>#</Table.Th>
                                    <Table.Th>On</Table.Th>
                                    <Table.Th>Ingress</Table.Th>
                                    <Table.Th>Match</Table.Th>
                                    <Table.Th>Target</Table.Th>
                                    <Table.Th>Name</Table.Th>
                                    {canWrite && <Table.Th><VisuallyHidden>Actions</VisuallyHidden></Table.Th>}
                                  </Table.Tr>
                                </Table.Thead>
                                <Table.Tbody>
                                  {pfShown.map(({ item, position }) => (
                                    <PortForwardRow
                                      key={item.value.id}
                                      item={item}
                                      position={position}
                                      zones={zones}
                                      addresses={addresses}
                                      canWrite={canWrite}
                                      unlocked={unlocked}
                                      busy={busy}
                                      onToggle={() => {
                                        const { id: _id, ...rest } = item.value;
                                        void _id;
                                        void change(() => api.savePortForward({ ...rest, enabled: !item.value.enabled }, item), 'port forwards');
                                      }}
                                      onEdit={() => setEditingForward(item)}
                                      onDelete={() => setDeleting({ kind: 'port-forward', item, position })}
                                    />
                                  ))}
                                </Table.Tbody>
                              </Table>
                            </ScrollArea>
                          </SortableContext>
                        </DndContext>
                        {pfShown.length === 0 && <Text c="dimmed" size="sm">No port forward matches the filter.</Text>}
                      </>
                    )}
                  </Stack>
                </Tabs.Panel>

                <Tabs.Panel value="one-to-one" pt="md">
                  <Stack>
                    <NatOrderToolbar
                      description="Maps an external IPv4 address or prefix to an internal one in both directions, evaluated from the top."
                      canWrite={canWrite}
                      unlocked={unlocked}
                      filtered={o2oFiltered}
                      count={oneToOne.items.length}
                      addLabel="Add 1:1 entry"
                      onToggleUnlock={() => setUnlocked(!unlocked)}
                      onAdd={() => setEditingOneToOne('new')}
                    />

                    {oneToOne.items.length === 0 ? (
                      <Alert role="status" title="No 1:1 NAT entries">No 1:1 NAT mapping is saved.</Alert>
                    ) : (
                      <>
                        <TextInput
                          label="Filter 1:1 NAT"
                          placeholder="Name, zone, external, internal, or alias"
                          value={filter}
                          onChange={(e) => { setFilter(e.currentTarget.value); setUnlocked(false); }}
                        />
                        {canWrite && o2oFiltered && oneToOne.items.length > 1 && (
                          <Text c="dimmed" size="sm">The order is locked while the filter is in use.</Text>
                        )}
                        {unlocked && <Text c="dimmed" size="sm">Drag an entry by its grip to move it. Choose Done to edit entries again.</Text>}
                        <DndContext
                          sensors={sensors}
                          collisionDetection={closestCenter}
                          onDragEnd={handleDrop('one-to-one', o2oOrder, oneToOne.version, '1:1 NAT entries', (ids, ver) => api.orderOneToOneNats(ids, ver))}
                          accessibility={{
                            announcements: makeAnnouncements(o2oOrder, (id) => o2oById.get(String(id))?.value.name ?? 'the 1:1 entry'),
                            screenReaderInstructions: instructions,
                          }}
                        >
                          <SortableContext items={o2oOrder} strategy={verticalListSortingStrategy}>
                            <ScrollArea>
                              <Table className="data-table rules" verticalSpacing="xs">
                                <Table.Thead>
                                  <Table.Tr>
                                    <Table.Th>#</Table.Th>
                                    <Table.Th>On</Table.Th>
                                    <Table.Th>Zone</Table.Th>
                                    <Table.Th>External</Table.Th>
                                    <Table.Th>Internal</Table.Th>
                                    <Table.Th>Destination</Table.Th>
                                    <Table.Th>Name</Table.Th>
                                    {canWrite && <Table.Th><VisuallyHidden>Actions</VisuallyHidden></Table.Th>}
                                  </Table.Tr>
                                </Table.Thead>
                                <Table.Tbody>
                                  {o2oShown.map(({ item, position }) => (
                                    <OneToOneRow
                                      key={item.value.id}
                                      item={item}
                                      position={position}
                                      zones={zones}
                                      addresses={addresses}
                                      canWrite={canWrite}
                                      unlocked={unlocked}
                                      busy={busy}
                                      onToggle={() => {
                                        const { id: _id, ...rest } = item.value;
                                        void _id;
                                        void change(() => api.saveOneToOneNat({ ...rest, enabled: !item.value.enabled }, item), '1:1 NAT entries');
                                      }}
                                      onEdit={() => setEditingOneToOne(item)}
                                      onDelete={() => setDeleting({ kind: 'one-to-one', item, position })}
                                    />
                                  ))}
                                </Table.Tbody>
                              </Table>
                            </ScrollArea>
                          </SortableContext>
                        </DndContext>
                        {o2oShown.length === 0 && <Text c="dimmed" size="sm">No 1:1 NAT entry matches the filter.</Text>}
                      </>
                    )}
                  </Stack>
                </Tabs.Panel>

                <Tabs.Panel value="outbound" pt="md">
                  <Paper withBorder p="md" radius="sm">
                    <Stack gap="sm" align="flex-start">
                      <div>
                        <Text fw={600}>Outbound IPv4 NAT</Text>
                        <Text size="sm" c="dimmed">
                          Automatic masquerade translates forwarded IPv4 traffic leaving uplink interfaces to each uplink&apos;s address. Any matching 1:1 NAT mapping runs first.
                        </Text>
                      </div>
                      {outboundRemoteChanged && (
                        <Alert color="yellow" role="status" title="Outbound NAT changed elsewhere">
                          A newer saved outbound NAT setting was loaded. Click Reset to load the latest version before saving.
                        </Alert>
                      )}
                      <SegmentedControl
                        aria-label="Outbound NAT mode"
                        disabled={!canWrite || busy}
                        value={outboundMode}
                        onChange={(value) => {
                          const next = value as OutboundNatMode;
                          setOutboundMode(next);
                          setOutboundDirty(effectiveOutbound === null || next !== savedOutboundMode);
                        }}
                        data={[
                          { value: 'automatic', label: 'Automatic masquerade' },
                          { value: 'none', label: 'Disabled (none)' },
                        ]}
                      />
                      {canWrite && (
                        <Group gap="xs">
                          <Button
                            loading={busy}
                            disabled={outboundRemoteChanged || (outboundMode === savedOutboundMode && !outboundDirty)}
                            onClick={() => void saveOutbound()}
                          >
                            Save outbound NAT
                          </Button>
                          {outboundDirty && (
                            <Button
                              variant="default"
                              disabled={busy}
                              onClick={() => {
                                setOutboundMode(savedOutboundMode);
                                setOutboundBaseVersion(outboundServerVersion);
                                setOutboundDirty(false);
                              }}
                            >
                              Reset
                            </Button>
                          )}
                        </Group>
                      )}
                    </Stack>
                  </Paper>
                </Tabs.Panel>
              </Tabs>

              {editingForward && (
                <PortForwardEditor
                  key={editingForward === 'new' ? 'new' : `${editingForward.value.id}:${editingForward.version}`}
                  entry={editingForward === 'new' ? undefined : editingForward.value}
                  snapshot={snapshot}
                  onClose={() => setEditingForward(undefined)}
                  onSessionLost={onSessionLost}
                  onAbandoned={setError}
                  onSave={async (value) => {
                    try {
                      await api.savePortForward(value, editingForward === 'new' ? undefined : editingForward);
                    } catch (err) {
                      if (err instanceof ApiError && (err.status === 404 || err.status === 412)) void resource.reload();
                      throw err;
                    }
                    onSaved();
                    void resource.reload();
                  }}
                />
              )}

              {editingOneToOne && (
                <OneToOneEditor
                  key={editingOneToOne === 'new' ? 'new' : `${editingOneToOne.value.id}:${editingOneToOne.version}`}
                  entry={editingOneToOne === 'new' ? undefined : editingOneToOne.value}
                  snapshot={snapshot}
                  onClose={() => setEditingOneToOne(undefined)}
                  onSessionLost={onSessionLost}
                  onAbandoned={setError}
                  onSave={async (value) => {
                    try {
                      await api.saveOneToOneNat(value, editingOneToOne === 'new' ? undefined : editingOneToOne);
                    } catch (err) {
                      if (err instanceof ApiError && (err.status === 404 || err.status === 412)) void resource.reload();
                      throw err;
                    }
                    onSaved();
                    void resource.reload();
                  }}
                />
              )}

              <Modal opened={deleting !== undefined} onClose={() => setDeleting(undefined)} title={deleting?.kind === 'port-forward' ? 'Delete port forward' : 'Delete 1:1 NAT entry'} centered>
                {deleting && (
                  <Stack>
                    <Text size="sm">
                      Delete {deleting.kind === 'port-forward' ? `port forward ${deleting.position}` : `1:1 NAT entry ${deleting.position}`},{' '}
                      <strong>{deleting.item.value.name}</strong>? Nothing changes on the box until applied.
                    </Text>
                    <Group justify="flex-end" gap="xs">
                      <Button variant="default" disabled={busy} onClick={() => setDeleting(undefined)}>Cancel</Button>
                      <Button
                        color="red"
                        loading={busy}
                        onClick={() => {
                          const target = deleting;
                          setDeleting(undefined);
                          if (target.kind === 'port-forward') {
                            void change(() => api.deletePortForward(target.item.value.id, target.item.version), 'port forwards');
                          } else {
                            void change(() => api.deleteOneToOneNat(target.item.value.id, target.item.version), '1:1 NAT entries');
                          }
                        }}
                      >
                        Delete
                      </Button>
                    </Group>
                  </Stack>
                )}
              </Modal>
            </Stack>
          );
        }}
      </Loaded>
    </Stack>
  );
}
