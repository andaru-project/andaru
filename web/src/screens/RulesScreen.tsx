import { DndContext, KeyboardSensor, PointerSensor, closestCenter, useSensor, useSensors, type Announcements, type DragEndEvent, type UniqueIdentifier } from '@dnd-kit/core';
import { SortableContext, arrayMove, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { ActionIcon, Alert, Anchor, Badge, Button, Group, Modal, Paper, ScrollArea, Stack, Switch, Table, Text, TextInput, Title } from '@mantine/core';
import { useCallback, useState } from 'react';
import { Link } from 'react-router';
import { ApiError, reportError, type ApiClient } from '../api';
import { formatAddressAlias, formatPortAlias } from '../format';
import { firewallZone, type Rule, type RuleSide, type SavedObject, type Session, type Snapshot } from '../types';
import { zoneColorMap } from '../zones';
import { useResource } from '../useResource';
import { Loaded } from './Loaded';
import { EditMark, Grip } from './Marks';
import { RuleEditor } from './RuleEditor';
import { ZoneBadge } from './ZoneBadge';

interface RulesScreenProps { api: ApiClient; session: Session; onSaved(): void; onSessionLost(): void }

// Names and summaries for what a rule refers to, from the saved configuration the rules were read with.
interface Names {
  zones: Map<string, string>;
  zoneColors: Map<string, string>;
  addresses: Map<string, string>;
  addressSummary: Map<string, string>;
  ports: Map<string, string>;
  portSummary: Map<string, string>;
}

function names(snapshot: Snapshot): Names {
  const byID = (items: Array<{ id: string; name: string }> = []) => new Map(items.map((item) => [item.id, item.name]));
  const addressAliases = snapshot.value.aliases?.addresses ?? [];
  const portAliases = snapshot.value.aliases?.ports ?? [];
  const addresses = byID(addressAliases);
  const ports = byID(portAliases);
  const addressSummary = new Map<string, string>();
  for (const alias of addressAliases) {
    const summary = formatAddressAlias(alias, addresses);
    if (summary) addressSummary.set(alias.id, summary);
  }
  const portSummary = new Map<string, string>();
  for (const alias of portAliases) {
    const summary = formatPortAlias(alias, ports);
    if (summary) portSummary.set(alias.id, summary);
  }
  return { zones: byID(snapshot.value.zones), zoneColors: zoneColorMap(snapshot.value.zones), addresses, addressSummary, ports, portSummary };
}

// A mark beside each action's word: the word says it, the mark and the colour help the eye down the column.
// Drawn, not typed: a font may lack the character. A tick, a cross, and a barred circle for "refused, and told so".
const actions: Record<Rule['action'], { mark: string; color: string }> = {
  accept: { mark: 'M3 8.5l3.5 3.5L13 4.5', color: 'green' },
  drop: { mark: 'M4 4l8 8M12 4l-8 8', color: 'red' },
  reject: { mark: 'M8 2.5a5.5 5.5 0 1 0 0 11a5.5 5.5 0 1 0 0-11M4.2 4.2l7.6 7.6', color: 'orange' },
};
function ActionMark({ action }: { action: Rule['action'] }) {
  return <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={actions[action].mark} /></svg>;
}

// One side of a rule as it reads: the zones it arrives in or leaves by, then who within them.
function Side({ side, names: known }: { side: RuleSide; names: Names }) {
  const toFirewall = side.zones.includes(firewallZone);
  return (
    <Group gap={6}>
      {side.zones.length === 0 && side.addresses.length === 0 && <Text size="sm" c="dimmed">any network</Text>}
      {toFirewall && <Badge variant="filled" color="dark" tt="none">This firewall</Badge>}
      {side.zones.filter((id) => id !== firewallZone).map((id) => <ZoneBadge key={id} name={known.zones.get(id) ?? 'unknown zone'} color={known.zoneColors.get(id)} />)}
      {side.not && <Text size="sm" fw={600}>not</Text>}
      {side.addresses.map((id) => <Badge key={id} className="alias" variant="default" tt="none" title={known.addressSummary.get(id)}>{known.addresses.get(id) ?? 'unknown alias'}</Badge>)}
    </Group>
  );
}

// A rule is moved by its grip alone, by touch, pointer or keyboard; anywhere else on the row a finger scrolls.
function Row({ item, position, names: known, canWrite, unlocked, busy, onToggle, onEdit, onDelete }: {
  item: SavedObject<Rule>; position: number; names: Names; canWrite: boolean; unlocked: boolean; busy: boolean;
  onToggle(): void; onEdit(): void; onDelete(): void;
}) {
  const rule = item.value;
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({ id: rule.id, disabled: !unlocked || busy });
  // Names may repeat, so a control says the rule's place as well.
  const title = `rule ${position}, ${rule.name}`;
  return (
    <Table.Tr ref={setNodeRef} className="rule" data-disabled={!rule.enabled || undefined} data-dragging={isDragging || undefined} style={{ transform: CSS.Translate.toString(transform), transition }}>
      <Table.Td data-label="Position">
        <Group gap="xs" wrap="nowrap">
          {/* While a save is out the grip stays, so the keyboard keeps its place, and looks as idle as it is. */}
          {unlocked && <ActionIcon className="widget-grip" size="lg" variant="subtle" color="gray" id={`grip-${rule.id}`} data-disabled={busy || undefined} ref={setActivatorNodeRef} {...attributes} {...listeners} aria-label={`Move ${title}`}><Grip /></ActionIcon>}
          <Text size="sm" className="tabular">{position}</Text>
        </Group>
      </Table.Td>
      <Table.Td data-label="Enabled">
        {/* While the order is unlocked only the grips act, so a finger near one changes nothing else: the state is
            then said in words, as a disabled switch reads as off whatever it holds. */}
        {canWrite && !unlocked ? <Switch checked={rule.enabled} disabled={busy} onChange={onToggle} aria-label={`Enable ${title}`} /> : <Text size="sm">{rule.enabled ? 'on' : 'off'}</Text>}
      </Table.Td>
      <Table.Td data-label="Action">
        <Group gap={6} wrap="nowrap">
          <Badge color={actions[rule.action].color} variant="light" leftSection={<ActionMark action={rule.action} />}>{rule.action}</Badge>
          {rule.log && <Badge variant="outline" color="gray">logs</Badge>}
        </Group>
      </Table.Td>
      <Table.Td data-label="From"><Side side={rule.from} names={known} /></Table.Td>
      <Table.Td data-label="To"><Side side={rule.to} names={known} /></Table.Td>
      <Table.Td data-label="Ports">
        {rule.ports.length === 0 ? <Text size="sm" c="dimmed">any</Text> : <Group gap={6}>{rule.ports.map((id) => <Badge key={id} className="alias" variant="default" tt="none" title={known.portSummary.get(id)}>{known.ports.get(id) ?? 'unknown alias'}</Badge>)}</Group>}
      </Table.Td>
      <Table.Td data-label="Name"><Text size="sm" fw={600}>{rule.name}</Text>{rule.description && <Text size="sm" c="dimmed">{rule.description}</Text>}</Table.Td>
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

export function RulesScreen({ api, session, onSaved, onSessionLost }: RulesScreenProps) {
  const canWrite = session.capabilities.includes('configuration.write');
  // The rules with their versions, and the configuration they name things from. They are two reads: when a save
  // landed between them they are read again, twice at most, so a rule seldom names an alias the page has not got.
  const resource = useResource(useCallback(async () => {
    for (let tries = 0; ; tries++) {
      const [list, snapshot] = await Promise.all([api.rules(), api.config()]);
      if (list.savedVersion === snapshot.savedVersion || tries === 2) return { list, snapshot };
    }
  }, [api]), onSessionLost);
  const [filter, setFilter] = useState('');
  const [unlocked, setUnlocked] = useState(false);
  const [editing, setEditing] = useState<SavedObject<Rule> | 'new'>();
  const [deleting, setDeleting] = useState<{ item: SavedObject<Rule>; position: number }>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [moved, setMoved] = useState<string[]>();
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 4 } }), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }));

  // A save that fails because the saved rules changed underneath shows what is saved now and says so: the page
  // never guesses what the other change was.
  async function change(send: () => Promise<unknown>) {
    setBusy(true);
    setError('');
    try {
      await send();
      onSaved();
    } catch (requestError) {
      if (requestError instanceof ApiError && requestError.status === 412) setError('The rules were changed by someone else. The list now shows what is saved; nothing of this change was.');
      else reportError(requestError, onSessionLost, setError);
      if (requestError instanceof ApiError && requestError.status === 401) return;
    }
    await resource.reload();
    setMoved(undefined);
    setBusy(false);
  }
  // The filter field shows only with rules to filter, so only then does it lock anything.
  const filtered = filter.trim() !== '' && (resource.data?.list.items.length ?? 0) > 0;

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div><Title order={2}>Rules</Title><Text c="dimmed">What may pass between networks and reach this firewall, tried from the top: the first rule that matches decides.</Text></div>
        {canWrite && resource.data && (
          <Group gap="xs">
            {/* A move among hidden rows would be a guess, so the order stays locked while the filter hides any. */}
            <Button size="xs" variant="default" disabled={!unlocked && (filtered || resource.data.list.items.length < 2)} leftSection={<EditMark editing={unlocked} />} onClick={() => setUnlocked(!unlocked)}>{unlocked ? 'Done' : 'Edit order'}</Button>
            <Button size="xs" disabled={unlocked} onClick={() => setEditing('new')}>Add rule</Button>
          </Group>
        )}
      </Group>
      {error && <Alert color="red" role="alert" title="The rules were not changed" withCloseButton onClose={() => setError('')}>{error}</Alert>}
      <Loaded resource={resource} what="rules">
        {({ list, snapshot }) => {
          const known = names(snapshot);
          // While a move is being saved the list shows it at once; a rule saved meanwhile comes after.
          const saved = list.items.map((item) => item.value.id);
          const order = moved ? [...moved, ...saved.filter((id) => !moved.includes(id))] : saved;
          const byID = new Map(list.items.map((item) => [item.value.id, item]));
          const items = order.flatMap((id) => byID.get(id) ?? []);
          // Every word of the filter must be somewhere in what the row shows.
          const words = filter.toLowerCase().split(/\s+/).filter(Boolean);
          const matches = (rule: Rule) => {
            const row = [rule.name, rule.description ?? '', rule.action, rule.log ? 'logs' : '', rule.from.not || rule.to.not ? 'not' : '',
              ...[...rule.from.zones, ...rule.to.zones].map((id) => (id === firewallZone ? 'this firewall' : known.zones.get(id) ?? 'unknown zone')),
              ...[...rule.from.addresses, ...rule.to.addresses].map((id) => known.addresses.get(id) ?? 'unknown alias'),
              ...rule.ports.map((id) => known.ports.get(id) ?? 'unknown alias')].join(' ').toLowerCase();
            return words.every((word) => row.includes(word));
          };
          const shown = items.map((item, index) => ({ item, position: index + 1 })).filter(({ item }) => matches(item.value));
          const management = snapshot.value.segments.find((segment) => segment.id === snapshot.value.management?.segment);
          // The forwards the filter lets in by itself: enabled ones that translate and keep their own rule.
          const forwards = (snapshot.value.nat?.portForwards ?? []).filter((forward) => forward.enabled && !forward.noRedirect && forward.filterRule === 'auto').length;

          const title = (id: UniqueIdentifier) => byID.get(String(id))?.value.name ?? 'the rule';
          const place = (id: UniqueIdentifier) => `position ${order.indexOf(String(id)) + 1} of ${order.length}`;
          const announcements: Announcements = {
            onDragStart: ({ active }) => `Picked up ${title(active.id)}, at ${place(active.id)}.`,
            onDragOver: ({ active, over }) => (over ? `${title(active.id)} is at ${place(over.id)}.` : undefined),
            onDragEnd: ({ active, over }) => (over ? `Dropped ${title(active.id)} at ${place(over.id)}.` : `${title(active.id)} was put back.`),
            onDragCancel: ({ active }) => `${title(active.id)} was put back at ${place(active.id)}.`,
          };
          const instructions = { draggable: 'To move a rule, press Space or Enter on its grip, move it with the arrow keys, then press Space or Enter to drop it, or Escape to put it back.' };
          // A drop is a saved edit: the whole order, guarded by the list's version.
          function dropped({ active, over }: DragEndEvent) {
            if (busy || !over || active.id === over.id) return;
            const next = arrayMove(order, order.indexOf(String(active.id)), order.indexOf(String(over.id)));
            setMoved(next);
            // The row is put elsewhere in the page, which can take the focus off its grip: it goes back there.
            void change(() => api.orderRules(next, list.version)).then(() => { if (document.activeElement === document.body) document.getElementById(`grip-${active.id}`)?.focus(); });
          }

          return (
            <Stack>
              {!management && (
                <Alert color="orange" title="No management segment is set">
                  Without one the packet filter is off, and a configuration that holds rules or NAT is refused when it is applied. The management segment is set with the <Anchor component={Link} to="/network/interfaces">interfaces</Anchor>.
                </Alert>
              )}
              {management && (
                <Paper component="section" withBorder p="md" aria-labelledby="fixed-rules">
                  <Title order={3} size="h5" mb="xs" id="fixed-rules">Before any rule</Title>
                  <ul className="fixed-rules">
                    <li>Traffic of a connection already allowed, and what relates to it, is accepted. Invalid packets are dropped.</li>
                    <li>Loopback and IPv6 neighbour discovery are accepted, and DHCP requests on the segments this firewall serves.</li>
                    <li><strong>Anti-lockout:</strong> SSH to this firewall from the management segment, {management.name}, is always accepted{snapshot.value.management?.allowedSources?.length ? ', from its allowed sources' : ''}.</li>
                    {forwards > 0 && <li>Port forwards that let their own traffic in: {forwards}.</li>}
                  </ul>
                </Paper>
              )}
              {list.items.length === 0 ? (
                <Alert role="status" title="No rules">{management ? 'Traffic between networks is dropped, and so is traffic to this firewall, except what is listed above.' : 'No rule is saved.'}</Alert>
              ) : (
                <>
                  <TextInput label="Filter rules" placeholder="A name, zone, alias or action" value={filter} onChange={(event) => { setFilter(event.currentTarget.value); setUnlocked(false); }} />
                  {canWrite && filtered && list.items.length > 1 && <Text c="dimmed" size="sm">The order is locked while the filter is in use: a move among hidden rules would be a guess.</Text>}
                  {unlocked && <Text c="dimmed" size="sm">Drag a rule by its grip to move it. Choose Done to edit rules again.</Text>}
                  <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={dropped} accessibility={{ announcements, screenReaderInstructions: instructions }}>
                    <SortableContext items={order} strategy={verticalListSortingStrategy}>
                      <ScrollArea>
                        <Table className="data-table rules" verticalSpacing="xs">
                          <Table.Thead><Table.Tr><Table.Th>#</Table.Th><Table.Th>On</Table.Th><Table.Th>Action</Table.Th><Table.Th>From</Table.Th><Table.Th>To</Table.Th><Table.Th>Ports</Table.Th><Table.Th>Name</Table.Th>{canWrite && <Table.Th>Actions</Table.Th>}</Table.Tr></Table.Thead>
                          <Table.Tbody>
                            {shown.map(({ item, position }) => (
                              <Row key={item.value.id} item={item} position={position} names={known} canWrite={canWrite} unlocked={unlocked} busy={busy}
                                onToggle={() => void change(() => api.saveRule({ ...item.value, enabled: !item.value.enabled }, item))}
                                onEdit={() => setEditing(item)} onDelete={() => setDeleting({ item, position })} />
                            ))}
                          </Table.Tbody>
                        </Table>
                      </ScrollArea>
                    </SortableContext>
                  </DndContext>
                  {shown.length === 0 && <Text c="dimmed" size="sm">No rule matches the filter.</Text>}
                  {management && <Text c="dimmed" size="sm">Everything else is dropped.</Text>}
                </>
              )}
              {editing && (
                <RuleEditor key={editing === 'new' ? 'new' : editing.value.id} api={api} rule={editing === 'new' ? undefined : editing.value} snapshot={snapshot} onClose={() => setEditing(undefined)} onSessionLost={onSessionLost} onAbandoned={setError}
                  onSave={async (rule) => {
                    try {
                      await api.saveRule(rule, editing === 'new' ? undefined : editing);
                    } catch (error) {
                      // The rule may have changed or gone underneath, or been saved with the answer lost: the
                      // page reads what is saved, so the panel, once closed, opens on that.
                      if (!(error instanceof ApiError && error.status === 401)) await resource.reload();
                      throw error;
                    }
                    onSaved();
                    // A rule just added is not hidden by a filter typed before it.
                    if (editing === 'new') setFilter('');
                    await resource.reload();
                  }} />
              )}
            </Stack>
          );
        }}
      </Loaded>
      <Modal opened={Boolean(deleting)} onClose={() => setDeleting(undefined)} title="Delete rule" centered lockScroll={false} closeButtonProps={{ 'aria-label': 'Close' }}>
        <Stack>
          <Text>Delete rule {deleting?.position}, <strong>{deleting?.item.value.name}</strong>? What it allows or stops changes when the saved configuration is applied.</Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setDeleting(undefined)}>Cancel</Button>
            <Button color="red" loading={busy} onClick={() => { const { item } = deleting!; void change(() => api.deleteRule(item.value.id, item.version)).then(() => setDeleting(undefined)); }}>Delete rule</Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
