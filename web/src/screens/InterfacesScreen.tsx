import { Alert, Badge, Button, ComboboxChevron, Group, Loader, Menu, ScrollArea, Stack, Table, Text, Title, VisuallyHidden } from '@mantine/core';
import { useCallback, useState } from 'react';
import { ApiClient, reportError } from '../api';
import { addressLines, isPort } from '../interfaceEdit';
import { stateColor, stateLabel, type ConfigBatchOperation, type LinkStatus, type Preview, type SavedLink, type SavedObject, type Segment, type Session, type Snapshot, type VirtualLinkKind } from '../types';
import { useResource, useVisiblePoll } from '../useResource';
import { bindingFindings, kernelName } from '../zones';
import { Address } from './Address';
import { InterfaceEditor } from './InterfaceEditor';
import { ZoneBadge } from './ZoneBadge';

interface InterfacesScreenProps { api: ApiClient; session: Session; onSaved(): void; onSessionLost(): void }

// What the panel opens on: the saved objects as read just now, with their versions, and the port on the box.
// It keeps the snapshot it opened with, so a poll that fails to read the configuration does not close it.
interface Editing { link?: SavedObject<SavedLink>; segment?: SavedObject<Segment>; live?: LinkStatus; newKind?: VirtualLinkKind; snapshot: Snapshot }

const unboundLabel: Record<string, string> = { HARDWARE_MISSING: 'Missing', HARDWARE_AMBIGUOUS: 'Unresolved' };

interface Row { key: string; live?: LinkStatus; saved?: SavedLink; state?: string; finding?: string; conflict?: boolean }

function join(links: LinkStatus[], snapshot: Snapshot, preview: Preview): Row[] {
  const resolved = new Map(preview.resolved.map((item) => [item.link, item.interface]));
  const findings = bindingFindings(preview);
  const onDevice = new Map<string, SavedLink[]>();
  const unbound: Row[] = [];
  snapshot.value.links.forEach((link, index) => {
    const finding = findings.get(index);
    const name = finding ? undefined : kernelName(link, resolved);
    if (name && links.some((live) => live.name === name)) onDevice.set(name, [...(onDevice.get(name) ?? []), link]);
    else unbound.push({ key: link.id, saved: link, state: !link.enabled ? 'Disabled' : finding ? unboundLabel[finding.code] ?? 'Not realized' : link.kind === 'ethernet' ? 'Unresolved' : 'Missing', finding: finding?.message });
  });
  return [
    ...links.flatMap((live) => {
      const saved = onDevice.get(live.name) ?? [];
      if (saved.length === 0) return [{ key: live.name, live }];
      return saved.map((link) => ({ key: `${live.name} ${link.id}`, live, saved: link, conflict: saved.length > 1 }));
    }),
    ...unbound,
  ];
}

export function InterfacesScreen({ api, session, onSaved, onSessionLost }: InterfacesScreenProps) {
  const canReadConfig = session.capabilities.includes('configuration.read');
  const canWrite = canReadConfig && session.capabilities.includes('configuration.write');
  const [editing, setEditing] = useState<Editing>();
  const [opening, setOpening] = useState('');
  const [error, setError] = useState('');
  const [saveError, setSaveError] = useState('');
  const load = useCallback(async () => {
    let savedError = '';
    // The saved side is optional: when it fails, the live links still show.
    const loadSaved = async () => {
      try {
        const snapshot = await api.config();
        return { snapshot, preview: await api.previewNetwork(snapshot.savedVersion) };
      } catch (error) {
        reportError(error, onSessionLost, (message) => { savedError = message; });
        return null;
      }
    };
    const [links, saved] = await Promise.all([api.links(), canReadConfig ? loadSaved() : Promise.resolve(null)]);
    return { links: links.items, saved, savedError };
  }, [api, canReadConfig, onSessionLost]);
  const resource = useResource(load, onSessionLost);
  useVisiblePoll(resource.refresh, 10_000);

  if (!resource.data) return resource.error ? <Alert color="red" role="alert" title="Interfaces request failed">{resource.error}</Alert> : <Loader aria-label="Loading interfaces" />;

  const { links, saved, savedError } = resource.data;
  // The loopback is not a port anyone configures: it shows only if the configuration names it.
  const all = (saved ? join(links, saved.snapshot, saved.preview) : links.map((live): Row => ({ key: live.name, live })))
    .filter((row) => row.saved || row.live?.linkType !== 'loopback');
  // One row per configured interface; what the box has beyond them goes to a table of its own.
  const configured = all.filter((row) => row.saved);
  const others = all.filter((row) => !row.saved);
  const segmentOf = (link: SavedLink): Segment | undefined => saved?.snapshot.value.segments.find((segment) => segment.link === link.id);
  const zoneOf = (id: string) => saved?.snapshot.value.zones.find((zone) => zone.id === id);
  const zoneName = (id: string) => zoneOf(id)?.name ?? id;
  const linkName = (id: string) => saved?.snapshot.value.links.find((link) => link.id === id)?.name ?? 'another link';
  // The panel reads the objects again, so a save is guarded by the versions they have now.
  const edit = async (key: string, live: LinkStatus | undefined, link: SavedLink | undefined, segment: Segment | undefined) => {
    if (opening) return;
    setOpening(key);
    setError('');
    try {
      // The configuration too: which segment is the management one, the zones and the rest the panel reads.
      const [savedLink, savedSegment, snapshot] = await Promise.all([
        link ? api.configObject<SavedLink>('links', link.id) : Promise.resolve(undefined),
        segment ? api.configObject<Segment>('segments', segment.id) : Promise.resolve(undefined),
        api.config(),
      ]);
      // The two reads are apart: a segment moved to another link meanwhile would be written back onto this one.
      if (savedSegment && savedSegment.value.link !== savedLink?.value.id) setError('This interface was changed by someone else while it was opening. Refresh, then try again.');
      else setEditing({ link: savedLink, segment: savedSegment, live, snapshot });
    } catch (failure) {
      reportError(failure, onSessionLost, setError);
    } finally {
      setOpening('');
    }
  };
  const createVirtual = async (kind: VirtualLinkKind) => {
    if (opening) return;
    setOpening('new:');
    setError('');
    try {
      const snapshot = await api.config();
      setEditing({ newKind: kind, snapshot });
    } catch (failure) {
      reportError(failure, onSessionLost, setError);
    } finally {
      setOpening('');
    }
  };
  const save = async (operations: ConfigBatchOperation[]) => {
    await api.applyConfigChanges(operations);
    onSaved();
    void resource.reload();
  };
  // Edits start from the saved configuration: without it, nothing on the page says what is saved.
  const editable = canWrite && Boolean(saved);

  // What a link is, said only when it is not a plain Ethernet port.
  const kindLine = (link: SavedLink) => {
    if (link.kind === 'vlan') return `VLAN ${link.vlanId} on ${link.parent ? linkName(link.parent) : 'its parent'}`;
    if (link.kind === 'pppoe') return `PPPoE on ${link.parent ? linkName(link.parent) : 'its parent'}`;
    if (link.kind === 'bridge') {
      const names = (link.members ?? []).map(linkName);
      return names.length > 0 ? `Bridge (${names.join(', ')})` : 'Bridge (no members)';
    }
    if (link.kind === 'bond') {
      const names = (link.members ?? []).map(linkName);
      const detail = [link.mode, names.join(', ')].filter(Boolean).join(' · ');
      return detail ? `Bond (${detail})` : 'Bond';
    }
    return undefined;
  };
  const addressesCell = (live: LinkStatus | undefined, segment: Segment | undefined) => {
    const lines = addressLines(live, segment);
    if (lines.length === 0) return <Text c="dimmed">—</Text>;
    return lines.map((line) => (
      <div key={line.family} className="address-line">
        <Text size="xs" c="dimmed">{line.method ? `${line.family} · ${line.method}` : line.family}</Text>
        {line.addresses.map((address) => <Address key={address} value={address} />)}
        {line.pending.length > 0 && <Group gap={6} align="baseline"><Badge size="xs" color="yellow" variant="light" style={{ '--badge-color': 'var(--mantine-color-text)' }}>Pending</Badge><div>{line.pending.map((address) => <Address key={address} value={address} />)}</div></Group>}
      </div>
    ));
  };
  const stateBadge = (live: LinkStatus | undefined, state: string | undefined) => live
    ? <Badge variant="dot" color={stateColor[live.operState] ?? 'gray'}>{stateLabel(live.operState)}</Badge>
    : <Badge variant="dot" color={state === 'Disabled' ? 'gray' : 'red'}>{state}</Badge>;
  const actionButton = (key: string, label: string, ariaLabel: string, onClick: () => void, variant: 'default' | 'light') => (
    <Button size="xs" variant={variant} loading={opening === key} disabled={Boolean(opening) && opening !== key} aria-label={ariaLabel} onClick={onClick}>{label}</Button>
  );

  // Links the configuration does not name: hardware ports can be added; without the configuration, every link.
  const othersTable = (
    <ScrollArea><Table verticalSpacing="sm" withTableBorder className="data-table interfaces"><Table.Thead><Table.Tr><Table.Th>Device</Table.Th><Table.Th>State</Table.Th><Table.Th>Addresses</Table.Th>{editable && <Table.Th><VisuallyHidden>Actions</VisuallyHidden></Table.Th>}</Table.Tr></Table.Thead><Table.Tbody>
      {others.map(({ key, live }) => live && (
        <Table.Tr key={key}>
          <Table.Td data-label="Device">
            <Text ff="monospace" fw={600}>{live.name}</Text>
            <Text c="dimmed" size="sm"><Text span ff="monospace" inherit>{live.linkAddress || 'no MAC'}</Text>{live.kind && ` · ${live.kind}`}{live.mtu !== 1500 && ` · MTU ${live.mtu}`}</Text>
          </Table.Td>
          <Table.Td data-label="State">{stateBadge(live, undefined)}</Table.Td>
          <Table.Td data-label="Addresses">{addressesCell(live, undefined)}</Table.Td>
          {editable && <Table.Td>{isPort(live) && actionButton(key, 'Add', `Add ${live.name} to the configuration`, () => void edit(key, live, undefined, undefined), 'light')}</Table.Td>}
        </Table.Tr>
      ))}
    </Table.Tbody></Table></ScrollArea>
  );

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div><Title order={2}>Interfaces</Title><Text c="dimmed">{saved ? 'Configured network interfaces and their live state.' : 'Network links the box reports now.'}</Text></div>
        <Group gap="xs">
          {editable && (
            <Menu position="bottom-end">
              <Menu.Target>
                <Button size="xs" rightSection={<ComboboxChevron size="xs" style={{ color: 'currentColor' }} />} loading={opening === 'new:'} disabled={Boolean(opening) && opening !== 'new:'}>Add virtual interface</Button>
              </Menu.Target>
              <Menu.Dropdown>
                <Menu.Item onClick={() => void createVirtual('vlan')}>VLAN</Menu.Item>
                <Menu.Item onClick={() => void createVirtual('bridge')}>Bridge</Menu.Item>
                <Menu.Item onClick={() => void createVirtual('bond')}>Bond</Menu.Item>
              </Menu.Dropdown>
            </Menu>
          )}
          <Button size="xs" variant="default" loading={resource.loading} onClick={() => void resource.reload()}>Refresh</Button>
        </Group>
      </Group>
      {error && <Alert color="red" role="alert" title="The interface could not be opened" withCloseButton onClose={() => setError('')}>{error}</Alert>}
      {saveError && <Alert color="red" role="alert" title="The interface was not saved" withCloseButton onClose={() => setSaveError('')}>{saveError}</Alert>}
      {resource.error && <Alert color="red" role="alert" title="Interfaces request failed">{resource.error}</Alert>}
      {savedError && <Alert color="red" role="alert" title="The saved configuration could not be read">{savedError} Only live links are shown.</Alert>}
      {!canReadConfig && <Alert color="gray" role="status">Your account cannot read the saved configuration, so only live links are shown.</Alert>}
      {all.length === 0 ? <Alert role="status">The box reports no network links.</Alert> : !saved ? othersTable : (
        <>
          {configured.length === 0 ? <Alert role="status">The configuration names no interfaces yet.</Alert> : (
            <ScrollArea><Table verticalSpacing="sm" withTableBorder className="data-table interfaces"><Table.Thead><Table.Tr><Table.Th>Interface</Table.Th><Table.Th>Device</Table.Th><Table.Th>State</Table.Th><Table.Th>Addresses</Table.Th>{editable && <Table.Th><VisuallyHidden>Actions</VisuallyHidden></Table.Th>}</Table.Tr></Table.Thead><Table.Tbody>
              {configured.map(({ key, live, saved: link, state, finding, conflict }) => {
                if (!link) return null;
                const segment = segmentOf(link);
                const management = segment !== undefined && saved.snapshot.value.management?.segment === segment.id;
                const kind = kindLine(link);
                const mtu = live && live.mtu !== link.mtu ? `MTU ${live.mtu} → ${link.mtu}` : link.mtu !== 1500 ? `MTU ${link.mtu}` : undefined;
                return (
                  <Table.Tr key={key}>
                    {/* The interface as configured: its name, zone and role; its kind and segment only when unusual. */}
                    <Table.Td data-label="Interface">
                      <Group gap="xs">
                        <Text fw={600}>{link.name}</Text>
                        {segment && <ZoneBadge name={zoneName(segment.zone)} color={zoneOf(segment.zone)?.color} />}
                        {conflict && <Badge color="red" variant="light">Conflict: several saved links</Badge>}
                      </Group>
                      {/* Under the name: the interface's role, and its kind and segment when they are not the usual. */}
                      {(management || kind || !segment || segment.name !== link.name) && (
                        <Group gap="xs" mt={4}>
                          {management && <Badge variant="light">Management</Badge>}
                          {(kind || !segment || segment.name !== link.name) && <Text c="dimmed" size="sm">{[kind, !segment ? 'No segment' : segment.name !== link.name ? `Segment ${segment.name}` : undefined].filter(Boolean).join(' · ')}</Text>}
                        </Group>
                      )}
                      {finding && <Text size="sm" className="error-text">{finding}</Text>}
                    </Table.Td>
                    <Table.Td data-label="Device">
                      {live ? <Text ff="monospace">{live.name}</Text> : <Text c="dimmed">No device</Text>}
                      {(live?.linkAddress || link.permanentMac || mtu) && <Text c="dimmed" size="sm"><Text span ff="monospace" inherit>{live?.linkAddress || link.permanentMac}</Text>{mtu && <>{(live?.linkAddress || link.permanentMac) && ' · '}{mtu}</>}</Text>}
                    </Table.Td>
                    <Table.Td data-label="State">
                      {stateBadge(live, state)}
                      {/* Applied, a disabled link is down: only while it is still up is the change waiting. */}
                      {live && !link.enabled && live.flags.some((flag) => flag.toUpperCase() === 'UP') && <Text size="xs" c="dimmed">Pending: off</Text>}
                    </Table.Td>
                    <Table.Td data-label="Addresses">{addressesCell(live, segment)}</Table.Td>
                    {editable && <Table.Td>{actionButton(key, 'Edit', `Edit ${link.name}`, () => void edit(key, live, link, segment), 'default')}</Table.Td>}
                  </Table.Tr>
                );
              })}
            </Table.Tbody></Table></ScrollArea>
          )}
          {others.length > 0 && (
            <>
              <div><Title order={3} size="h4">Not in the configuration</Title><Text c="dimmed" size="sm">Links on the box the configuration does not name. A hardware port can be added.</Text></div>
              {othersTable}
            </>
          )}
        </>
      )}
      {editing && (
        <InterfaceEditor key={editing.link ? `link:${editing.link.value.id}` : editing.live ? `live:${editing.live.name}` : `new:${editing.newKind}`} {...editing} onClose={() => setEditing(undefined)}
          onSessionLost={onSessionLost} onAbandoned={setSaveError} onSave={save} />
      )}
    </Stack>
  );
}
