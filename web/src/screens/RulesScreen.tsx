import { DndContext, KeyboardSensor, PointerSensor, closestCenter, useSensor, useSensors, type Announcements, type DragEndEvent, type UniqueIdentifier } from '@dnd-kit/core';
import { SortableContext, arrayMove, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { ActionIcon, Alert, Anchor, Badge, Button, Group, Modal, Paper, Stack, Switch, Table, Text, TextInput, Title } from '@mantine/core';
import { useCallback, useState } from 'react';
import { Link } from 'react-router';
import { ApiError, apiErrorMessage, type ApiClient } from '../api';
import { firewallZone, type Rule, type RuleSide, type SavedObject, type Session, type Snapshot } from '../types';
import { useResource } from '../useResource';
import { Loaded } from './Loaded';
import { EditMark, Grip } from './Marks';
import { RuleEditor } from './RuleEditor';
import { ZoneBadge } from './ZoneBadge';

interface RulesScreenProps { api: ApiClient; session: Session; onSaved(): void; onSessionLost(): void }

// Names for what a rule refers to, from the saved configuration the rules were read with.
interface Names { zones: Map<string, string>; addresses: Map<string, string>; ports: Map<string, string> }

function names(snapshot: Snapshot): Names {
  const byID = (items: Array<{ id: string; name: string }> = []) => new Map(items.map((item) => [item.id, item.name]));
  return { zones: byID(snapshot.value.zones), addresses: byID(snapshot.value.aliases?.addresses), ports: byID(snapshot.value.aliases?.ports) };
}

// A mark beside each action's word: the word says it, the mark and the colour help the eye down the column.
const actions: Record<Rule['action'], { mark: string; color: string }> = {
  accept: { mark: '✓', color: 'green' },
  drop: { mark: '✕', color: 'red' },
  reject: { mark: '⊘', color: 'orange' },
};

// One side of a rule as it reads: the zones it arrives in or leaves by, then who within them.
function Side({ side, names: known }: { side: RuleSide; names: Names }) {
  const toFirewall = side.zones.includes(firewallZone);
  return (
    <Group gap={6}>
      {side.zones.length === 0 && side.addresses.length === 0 && <Text size="sm" c="dimmed">any</Text>}
      {toFirewall && <Badge className="firewall-zone" variant="filled" color="dark">This firewall</Badge>}
      {side.zones.filter((id) => id !== firewallZone).map((id) => <ZoneBadge key={id} name={known.zones.get(id) ?? 'unknown zone'} />)}
      {side.not && <Text size="sm" fw={600}>not</Text>}
      {side.addresses.map((id) => <Badge key={id} className="alias" variant="default">{known.addresses.get(id) ?? 'unknown alias'}</Badge>)}
    </Group>
  );
}

// A rule is moved by its grip alone, by touch, pointer or keyboard; anywhere else on the row a finger scrolls.
function Row({ item, position, names: known, canWrite, unlocked, busy, onToggle, onEdit, onDelete }: {
  item: SavedObject<Rule>; position: number; names: Names; canWrite: boolean; unlocked: boolean; busy: boolean;
  onToggle(): void; onEdit(): void; onDelete(): void;
}) {
  const rule = item.value;
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({ id: rule.id, disabled: !unlocked });
  const title = rule.name || `rule ${position}`;
  return (
    <Table.Tr ref={setNodeRef} className="rule" data-disabled={!rule.enabled || undefined} data-dragging={isDragging || undefined} style={{ transform: CSS.Translate.toString(transform), transition }}>
      <Table.Td data-label="Position">
        <Group gap="xs" wrap="nowrap">
          {unlocked && <ActionIcon className="widget-grip" size="lg" variant="subtle" color="gray" ref={setActivatorNodeRef} {...attributes} {...listeners} aria-label={`Move ${title}`}><Grip /></ActionIcon>}
          <Text size="sm">{position}</Text>
        </Group>
      </Table.Td>
      <Table.Td data-label="Enabled">
        {canWrite ? <Switch checked={rule.enabled} disabled={busy} onChange={onToggle} aria-label={`${title} enabled`} /> : <Text size="sm">{rule.enabled ? 'on' : 'off'}</Text>}
      </Table.Td>
      <Table.Td data-label="Action">
        <Group gap={6} wrap="nowrap">
          <Badge color={actions[rule.action].color} variant="light" leftSection={<span aria-hidden="true">{actions[rule.action].mark}</span>}>{rule.action}</Badge>
          {rule.log && <Badge variant="outline" color="gray">logs</Badge>}
        </Group>
      </Table.Td>
      <Table.Td data-label="From"><Side side={rule.from} names={known} /></Table.Td>
      <Table.Td data-label="To"><Side side={rule.to} names={known} /></Table.Td>
      <Table.Td data-label="Ports">
        {rule.ports.length === 0 ? <Text size="sm" c="dimmed">any</Text> : <Group gap={6}>{rule.ports.map((id) => <Badge key={id} className="alias" variant="default">{known.ports.get(id) ?? 'unknown alias'}</Badge>)}</Group>}
      </Table.Td>
      <Table.Td data-label="Name"><Text size="sm" fw={600}>{rule.name}</Text>{rule.description && <Text size="sm" c="dimmed">{rule.description}</Text>}</Table.Td>
      {canWrite && (
        <Table.Td>
          <Group gap="xs" wrap="nowrap">
            <Button size="compact-xs" variant="default" aria-label={`Edit ${title}`} onClick={onEdit}>Edit</Button>
            <Button size="compact-xs" variant="default" color="red" aria-label={`Delete ${title}`} onClick={onDelete}>Delete</Button>
          </Group>
        </Table.Td>
      )}
    </Table.Tr>
  );
}

export function RulesScreen({ api, session, onSaved, onSessionLost }: RulesScreenProps) {
  const canWrite = session.capabilities.includes('configuration.write');
  // The rules with their versions, and the configuration they name things from, read together.
  const resource = useResource(useCallback(async () => {
    const [list, snapshot] = await Promise.all([api.rules(), api.config()]);
    return { list, snapshot };
  }, [api]), onSessionLost);
  const [filter, setFilter] = useState('');
  const [unlocked, setUnlocked] = useState(false);
  const [editing, setEditing] = useState<SavedObject<Rule> | 'new'>();
  const [deleting, setDeleting] = useState<SavedObject<Rule>>();
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
      if (requestError instanceof ApiError && requestError.status === 401) onSessionLost();
      else if (requestError instanceof ApiError && requestError.status === 412) setError('The rules were changed by someone else. The list now shows what is saved; nothing of this change was.');
      else setError(apiErrorMessage(requestError));
    } finally {
      await resource.reload();
      setMoved(undefined);
      setBusy(false);
    }
  }

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div><Title order={2}>Rules</Title><Text c="dimmed">What may pass between networks and reach this firewall, tried from the top: the first rule that matches decides.</Text></div>
        {canWrite && resource.data && (
          <Group gap="xs">
            {/* A move among hidden rows would be a guess, so the order stays locked while the filter hides any. */}
            <Button size="xs" variant="default" disabled={filter !== '' || resource.data.list.items.length < 2} leftSection={<EditMark editing={unlocked} />} onClick={() => setUnlocked(!unlocked)}>{unlocked ? 'Done' : 'Edit order'}</Button>
            <Button size="xs" onClick={() => setEditing('new')}>Add rule</Button>
          </Group>
        )}
      </Group>
      {error && <Alert color="red" role="alert" title="The rules were not changed" withCloseButton onClose={() => setError('')}>{error}</Alert>}
      <Loaded resource={resource} what="rules">
        {({ list, snapshot }) => {
          const known = names(snapshot);
          const order = moved ?? list.items.map((item) => item.value.id);
          const items = order.flatMap((id) => list.items.filter((item) => item.value.id === id));
          const text = filter.trim().toLowerCase();
          const matches = (rule: Rule) => [rule.name, rule.description ?? '', rule.action,
            ...[...rule.from.zones, ...rule.to.zones].map((id) => (id === firewallZone ? 'this firewall' : known.zones.get(id) ?? '')),
            ...[...rule.from.addresses, ...rule.to.addresses].map((id) => known.addresses.get(id) ?? ''),
            ...rule.ports.map((id) => known.ports.get(id) ?? '')].some((word) => word.toLowerCase().includes(text));
          const shown = items.map((item, index) => ({ item, position: index + 1 })).filter(({ item }) => text === '' || matches(item.value));
          const management = snapshot.value.segments.find((segment) => segment.id === snapshot.value.management?.segment);
          const forwards = (snapshot.value.nat?.portForwards ?? []).filter((forward) => forward.filterRule !== 'none').length;

          const title = (id: UniqueIdentifier) => items.find((item) => item.value.id === id)?.value.name || 'the rule';
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
            if (!over || active.id === over.id) return;
            const next = arrayMove(order, order.indexOf(String(active.id)), order.indexOf(String(over.id)));
            setMoved(next);
            void change(() => api.orderRules(next, list.version));
          }

          return (
            <Stack>
              {!management && (
                <Alert color="orange" title="The packet filter is off">
                  No management segment is set: applying this configuration leaves the packet filter off, and no rule here takes effect. The management segment is set with the <Anchor component={Link} to="/network/interfaces">interfaces</Anchor>.
                </Alert>
              )}
              <Paper component="section" withBorder p="md" aria-labelledby="fixed-rules">
                <Title order={3} size="h5" mb="xs" id="fixed-rules">Before any rule</Title>
                <ul className="fixed-rules">
                  <li>Traffic of a connection already allowed, and what relates to it, is accepted. Invalid packets are dropped.</li>
                  <li>Loopback and IPv6 neighbour discovery are accepted, and DHCP requests on the segments this firewall serves.</li>
                  {management && <li><strong>Anti-lockout:</strong> SSH to this firewall from the management segment, {management.name}, is always accepted{snapshot.value.management?.allowedSources?.length ? ', from its allowed sources' : ''}.</li>}
                  {forwards > 0 && <li>Port forwards let their own traffic in ({forwards}).</li>}
                </ul>
              </Paper>
              {list.items.length === 0 ? (
                <Alert role="status" title="No rules">Traffic between networks is dropped, and so is traffic to this firewall, except what is listed above.</Alert>
              ) : (
                <>
                  <TextInput label="Filter rules" placeholder="A name, zone, alias or action" value={filter} onChange={(event) => { setFilter(event.currentTarget.value); setUnlocked(false); }} />
                  <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={dropped} accessibility={{ announcements, screenReaderInstructions: instructions }}>
                    <SortableContext items={order} strategy={verticalListSortingStrategy}>
                      <Table className="data-table rules" verticalSpacing="xs">
                        <Table.Thead><Table.Tr><Table.Th>#</Table.Th><Table.Th>On</Table.Th><Table.Th>Action</Table.Th><Table.Th>From</Table.Th><Table.Th>To</Table.Th><Table.Th>Ports</Table.Th><Table.Th>Name</Table.Th>{canWrite && <Table.Th>Actions</Table.Th>}</Table.Tr></Table.Thead>
                        <Table.Tbody>
                          {shown.map(({ item, position }) => (
                            <Row key={item.value.id} item={item} position={position} names={known} canWrite={canWrite} unlocked={unlocked && canWrite} busy={busy}
                              onToggle={() => void change(() => api.saveRule({ ...item.value, enabled: !item.value.enabled }, { id: item.value.id, version: item.version }))}
                              onEdit={() => setEditing(item)} onDelete={() => setDeleting(item)} />
                          ))}
                        </Table.Tbody>
                      </Table>
                    </SortableContext>
                  </DndContext>
                  {shown.length === 0 && <Text c="dimmed" size="sm">No rule matches the filter.</Text>}
                  <Text c="dimmed" size="sm">Everything else is dropped.</Text>
                </>
              )}
              {editing && (
                <RuleEditor key={editing === 'new' ? 'new' : editing.value.id} rule={editing === 'new' ? undefined : editing.value} snapshot={snapshot} onClose={() => setEditing(undefined)} onSessionLost={onSessionLost}
                  onSave={async (rule) => {
                    await api.saveRule(rule, editing === 'new' ? undefined : { id: editing.value.id, version: editing.version });
                    onSaved();
                    await resource.reload();
                  }} />
              )}
            </Stack>
          );
        }}
      </Loaded>
      <Modal opened={Boolean(deleting)} onClose={() => setDeleting(undefined)} title="Delete rule" centered>
        <Stack>
          <Text>Delete the rule <strong>{deleting?.value.name}</strong>? What it allows or stops changes when the saved configuration is applied.</Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setDeleting(undefined)}>Cancel</Button>
            <Button color="red" loading={busy} onClick={() => { const item = deleting!; void change(() => api.deleteRule(item.value.id, item.version)).then(() => setDeleting(undefined)); }}>Delete rule</Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
