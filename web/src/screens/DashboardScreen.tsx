import { DndContext, KeyboardSensor, PointerSensor, closestCenter, useSensor, useSensors, type Announcements, type DragEndEvent, type UniqueIdentifier } from '@dnd-kit/core';
import { SortableContext, rectSortingStrategy, sortableKeyboardCoordinates, useSortable } from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { ActionIcon, Alert, Anchor, Badge, Button, Group, Loader, Paper, Stack, Text, Title } from '@mantine/core';
import { useCallback, useRef, useState, type ReactNode } from 'react';
import { useElementSize } from '@mantine/hooks';
import { Link } from 'react-router';
import { reportError, type ApiClient } from '../api';
import { arrange, moved } from '../dashboardLayout';
import { zonesByInterface } from '../zones';
import { stateColor, stateLabel, type DashboardLayout, type Preferences, type Session, type SystemInfo } from '../types';
import { usePolledResource, useResource, useVisiblePoll, type Resource } from '../useResource';
import { Address } from './Address';
import { Loaded } from './Loaded';
import { ResourcesWidget } from './ResourcesWidget';
import { SystemWidget } from './SystemWidget';
import { TrafficWidget, type TrafficChoice } from './TrafficWidget';
import { ZoneBadge } from './ZoneBadge';

interface DashboardScreenProps {
  api: ApiClient;
  session: Session;
  system: Resource<{ info: SystemInfo; loadedAt: number } | undefined>;
  onSessionLost(): void;
}

// zones says which zone each interface is in, by interface name; empty for an account that may not read the
// saved configuration, or before it has been read.
interface WidgetProps { api: ApiClient; zones: Map<string, string>; onSessionLost(): void }

const pollMilliseconds = 10_000;

// A pencil to start arranging, a tick to finish: the button's words say the same.
function EditMark({ editing }: { editing: boolean }) {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={editing ? 'M3 8.5l3.5 3.5L13 4.5' : 'M2.5 13.5l.7-3L10.5 3.2a1.4 1.4 0 0 1 2 0l.3.3a1.4 1.4 0 0 1 0 2L5.5 12.8z'} />
    </svg>
  );
}

// Dots in two columns: the sign for "drag here".
function Grip() {
  return (
    <svg width="12" height="18" viewBox="0 0 12 18" fill="currentColor" aria-hidden="true">
      {[3, 9, 15].flatMap((y) => [3, 9].map((x) => <circle key={`${x} ${y}`} cx={x} cy={y} r="1.6" />))}
    </svg>
  );
}

// A widget is dragged by its grip alone: anywhere else on it, a finger scrolls the page. The grip is a button, so
// the keyboard moves a widget through the same control (Space or Enter picks up and drops, the arrows move,
// Escape puts back). Translate, not Transform: widgets differ in height, and a scaled one is distorted.
function Widget({ id, title, to, more, unlocked, onHide, children }: { id: string; title: string; to?: string; more?: string; unlocked: boolean; onHide(): void; children: ReactNode }) {
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({ id, disabled: !unlocked });
  return (
    <Paper ref={setNodeRef} className="widget" data-dragging={isDragging || undefined} style={{ transform: CSS.Translate.toString(transform), transition }} component="section" withBorder p="md" aria-labelledby={`widget-${id}`}>
      <Group justify="space-between" align="flex-start" wrap="nowrap" mb="sm">
        {/* Every title looks the same. Where a page has the full view, a link at the other edge names it. */}
        <Title order={3} size="h5" id={`widget-${id}`}>{title}</Title>
        <Group gap="xs" wrap="nowrap">
          {to && more && <Anchor component={Link} to={to} size="sm" aria-label={`${title}: ${more}`}>{more} <span aria-hidden="true">→</span></Anchor>}
          {unlocked && <Button size="compact-xs" variant="default" aria-label={`Hide ${title}`} onClick={onHide}>Hide</Button>}
          {unlocked && <ActionIcon className="widget-grip" size="xl" variant="subtle" color="gray" ref={setActivatorNodeRef} {...attributes} {...listeners} aria-label={`Move ${title}`}><Grip /></ActionIcon>}
        </Group>
      </Group>
      {children}
    </Paper>
  );
}

function InterfacesWidget({ api, zones, onSessionLost }: WidgetProps) {
  const resource = usePolledResource(useCallback(() => api.links(), [api]), onSessionLost, pollMilliseconds);
  return (
    <Loaded resource={resource} what="interfaces">
      {({ items }) => {
        const links = items.filter((link) => link.linkType !== 'loopback');
        if (links.length === 0) return <Text c="dimmed">The box reports no network links.</Text>;
        return (
          <ul className="plain-list">
            {links.map((link) => (
              <li key={link.name}>
                <Group gap="xs"><Text ff="monospace" fw={600}>{link.name}</Text><ZoneBadge name={zones.get(link.name)} /><Badge variant="dot" color={stateColor[link.operState] ?? 'gray'}>{stateLabel(link.operState)}</Badge></Group>
                {link.addresses.filter((address) => address.scope !== 'link').map((address) => <Address key={address.address} value={`${address.address}/${address.prefixLength}`} size="xs" />)}
              </li>
            ))}
          </ul>
        );
      }}
    </Loaded>
  );
}

// One line for an interface and its prefix, the same for the uplink that holds the delegation and for every
// place a part of it went: who, in which zone, what.
function PrefixLine({ name, zone, note, prefix }: { name: string; zone: string | undefined; note?: string; prefix: string | null | undefined }) {
  return (
    <div>
      <Group gap="xs"><Text ff="monospace" fw={600}>{name}</Text><ZoneBadge name={zone} />{note && <Text size="sm" c="dimmed">{note}</Text>}</Group>
      {prefix ? <Address value={prefix} size="xs" /> : <Text className="error-text" size="sm">no prefix held</Text>}
    </div>
  );
}

function PrefixesWidget({ api, zones, onSessionLost }: WidgetProps) {
  const resource = usePolledResource(useCallback(() => api.prefixes(), [api]), onSessionLost, pollMilliseconds);
  return (
    <Loaded resource={resource} what="IPv6 prefixes">
      {({ wans }) => {
        if (wans.length === 0) return <Text c="dimmed">No uplink asks for a delegated prefix.</Text>;
        return (
          <ul className="plain-list">
            {wans.map((wan) => (
              <li key={wan.interface}>
                <PrefixLine name={wan.interface} zone={zones.get(wan.interface)} note="delegated" prefix={wan.current?.prefix} />
                {/* A segment's subnet ID and a pool's are separate number spaces, so the prefix is the key. */}
                <ul className="plain-list prefix-parts">
                  {wan.segments.filter((place) => place.prefix).map((place) => <li key={place.prefix}><PrefixLine name={place.interface} zone={zones.get(place.interface)} prefix={place.prefix} /></li>)}
                  {wan.pools.filter((place) => place.prefix).map((place) => <li key={place.prefix}><PrefixLine name={place.interface} zone={zones.get(place.interface)} note="pool" prefix={place.prefix} /></li>)}
                </ul>
                {wan.notFitting.length > 0 && <Text className="error-text" size="sm">No room for: {wan.notFitting.join(', ')}</Text>}
              </li>
            ))}
          </ul>
        );
      }}
    </Loaded>
  );
}

const noZones = new Map<string, string>();

export function DashboardScreen({ api, session, system, onSessionLost }: DashboardScreenProps) {
  // Which zone each interface is in comes from the saved configuration, for accounts that may read it. It is
  // read when the dashboard opens, and again until it has been read: it changes with a save, which is made on
  // another page. zonesKnown says the answer is in, so a choice that depends on it can be made; an account
  // that may not read the configuration has its answer at once, and it is that there are none to know.
  const canReadConfig = session.capabilities.includes('configuration.read');
  const zoneMap = useResource(useCallback(async () => {
    if (!canReadConfig) return noZones;
    const snapshot = await api.config();
    return zonesByInterface(snapshot, await api.previewNetwork(snapshot.savedVersion));
  }, [api, canReadConfig]), onSessionLost);
  useVisiblePoll(zoneMap.refresh, pollMilliseconds, !zoneMap.data);
  const zones = zoneMap.data ?? noZones;
  const zonesKnown = zoneMap.data !== undefined;
  const trafficChoice = useState<TrafficChoice>({});
  const all: Array<{ id: string; title: string; to?: string; more?: string; capability: string; body: ReactNode }> = [
    { id: 'system', title: 'System', to: '/system/details', more: 'Details', capability: 'status.read', body: <Loaded resource={system} what="the system description">{(loaded) => loaded && <SystemWidget info={loaded.info} loadedAt={loaded.loadedAt} />}</Loaded> },
    { id: 'interfaces', title: 'Interfaces', to: '/network/interfaces', more: 'Manage', capability: 'status.read', body: <InterfacesWidget api={api} zones={zones} onSessionLost={onSessionLost} /> },
    { id: 'resources', title: 'Resources', capability: 'status.read', body: <ResourcesWidget api={api} onSessionLost={onSessionLost} /> },
    { id: 'traffic', title: 'Network activity', capability: 'status.read', body: <TrafficWidget api={api} zones={zones} zonesKnown={zonesKnown} choice={trafficChoice} onSessionLost={onSessionLost} /> },
    { id: 'prefixes', title: 'IPv6 prefixes', capability: 'status.read', body: <PrefixesWidget api={api} zones={zones} onSessionLost={onSessionLost} /> },
  ];
  const allowed = all.filter((widget) => session.capabilities.includes(widget.capability));

  // The layout is kept with the account. It is read before any widget shows, so a hidden widget never loads and
  // nothing jumps; what the user then changes shows at once and is saved behind it. While it cannot be read the
  // default order shows and the layout stays locked: a save would replace preferences this page has not seen.
  const preferences = useResource(useCallback(() => api.preferences(), [api]), onSessionLost);
  useVisiblePoll(preferences.refresh, pollMilliseconds, !preferences.data);
  const [edited, setEdited] = useState<DashboardLayout>();
  const [unlocked, setUnlocked] = useState(false);
  const [saveError, setSaveError] = useState('');
  const editButton = useRef<HTMLButtonElement>(null);
  const layout = arrange(all.map((widget) => widget.id), edited ?? preferences.data?.dashboard);
  const placed = layout.order.flatMap((id) => allowed.filter((widget) => widget.id === id));
  const shown = placed.filter((widget) => !layout.hidden.includes(widget.id));
  const hidden = placed.filter((widget) => layout.hidden.includes(widget.id));
  // As many columns as fit at 20rem each, one before the width is known. The widgets are dealt to them in turn,
  // so they read across in their order and each column is as tall as its own widgets.
  const { ref: gridRef, width: gridWidth } = useElementSize();
  const columnCount = Math.max(1, Math.floor((gridWidth + 16) / (320 + 16)));
  const columns = Array.from({ length: columnCount }, (_, column) => shown.filter((_, index) => index % columnCount === column));
  // A drag starts after a few pixels, so a tap on the grip is not one.
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 4 } }), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }));

  // Saves go one at a time, each on the document the last one stored, and a save that a newer one has overtaken
  // is not sent: every save is the whole layout. Another browser's save since this page loaded is still lost,
  // last writer wins; the engine keeps no version of the document to refuse it by.
  const sending = useRef(Promise.resolve());
  const newest = useRef(0);
  const stored = useRef<Preferences>(undefined);
  function save(next: DashboardLayout) {
    setEdited(next);
    setSaveError('');
    const mine = ++newest.current;
    sending.current = sending.current.then(async () => {
      if (mine !== newest.current) return;
      try {
        stored.current = await api.savePreferences({ ...(stored.current ?? preferences.data), dashboard: next });
      } catch (error) {
        if (mine === newest.current) reportError(error, onSessionLost, setSaveError);
      }
    });
  }
  // The button that hid or showed a widget goes with it; focus goes to the edit button, not to the top of the page.
  function toggle(id: string) {
    save({ ...layout, hidden: layout.hidden.includes(id) ? layout.hidden.filter((item) => item !== id) : [...layout.hidden, id] });
    editButton.current?.focus();
  }

  // What a screen reader hears while a widget moves: its title and its place, not the library's identifiers.
  const name = (id: UniqueIdentifier) => all.find((widget) => widget.id === id)?.title ?? String(id);
  const place = (id: UniqueIdentifier) => `position ${shown.findIndex((widget) => widget.id === id) + 1} of ${shown.length}`;
  const announcements: Announcements = {
    onDragStart: ({ active }) => `Picked up ${name(active.id)}, at ${place(active.id)}.`,
    onDragOver: ({ active, over }) => (over ? `${name(active.id)} is at ${place(over.id)}.` : undefined),
    onDragEnd: ({ active, over }) => (over ? `Dropped ${name(active.id)} at ${place(over.id)}.` : `${name(active.id)} was put back.`),
    onDragCancel: ({ active }) => `${name(active.id)} was put back at ${place(active.id)}.`,
  };
  const instructions = { draggable: 'To move a widget, press Space or Enter on its grip, move it with the arrow keys, then press Space or Enter to drop it, or Escape to put it back.' };

  function dropped({ active, over }: DragEndEvent) {
    if (over && active.id !== over.id) save({ ...layout, order: moved(layout.order, shown.map((widget) => widget.id), String(active.id), String(over.id)) });
  }

  if (allowed.length === 0) return <Stack><Title order={2}>Dashboard</Title><Alert role="status">Your account can see none of the dashboard's widgets.</Alert></Stack>;

  return (
    <Stack>
      <Group justify="space-between">
        <Title order={2}>Dashboard</Title>
        <Group gap="xs">
          {unlocked && <Button size="xs" variant="default" onClick={() => save({})}>Reset layout</Button>}
          {/* The name says what a press does, so it is not also a pressed button. */}
          <Button ref={editButton} size="xs" variant="default" disabled={!preferences.data} leftSection={<EditMark editing={unlocked} />} onClick={() => setUnlocked(!unlocked)}>{unlocked ? 'Done' : 'Edit layout'}</Button>
        </Group>
      </Group>
      {preferences.error && !preferences.data && <Text c="dimmed" size="sm">The layout cannot be changed now: {preferences.error}</Text>}
      {saveError && <Alert color="red" role="alert" title="The layout was not saved">{saveError}</Alert>}
      {!preferences.data && !preferences.error ? <Loader aria-label="Loading the layout" /> : (
        <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={dropped} accessibility={{ announcements, screenReaderInstructions: instructions }}>
          <SortableContext items={shown.map((widget) => widget.id)} strategy={rectSortingStrategy}>
            <div className="widget-columns" ref={gridRef}>
              {columns.map((column, index) => (
                <div className="widget-column" key={index}>
                  {column.map(({ id, title, to, more, body }) => <Widget key={id} id={id} title={title} to={to} more={more} unlocked={unlocked} onHide={() => toggle(id)}>{body}</Widget>)}
                </div>
              ))}
            </div>
          </SortableContext>
        </DndContext>
      )}
      {shown.length === 0 && <Alert role="status">Every widget is hidden.{!unlocked && ' Edit the layout to show them again.'}</Alert>}
      {unlocked && hidden.length > 0 && (
        <Paper component="section" withBorder p="md" aria-labelledby="hidden-widgets">
          <Title order={3} size="h5" mb="sm" id="hidden-widgets">Hidden widgets</Title>
          <ul className="plain-list">
            {hidden.map(({ id, title }) => <li key={id}><Group justify="space-between"><Text>{title}</Text><Button size="compact-xs" variant="default" aria-label={`Show ${title}`} onClick={() => toggle(id)}>Show</Button></Group></li>)}
          </ul>
        </Paper>
      )}
    </Stack>
  );
}
