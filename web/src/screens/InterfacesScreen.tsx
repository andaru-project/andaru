import { Alert, Badge, Button, Group, Loader, ScrollArea, Stack, Table, Text, Title, VisuallyHidden } from '@mantine/core';
import { useCallback, useState } from 'react';
import { ApiClient, reportError } from '../api';
import { ipv4Modes, ipv6Modes, isPort, modeLabel } from '../interfaceEdit';
import { stateColor, stateLabel, type ConfigBatchOperation, type LinkStatus, type Preview, type SavedLink, type SavedObject, type Segment, type Session, type Snapshot } from '../types';
import { useResource, useVisiblePoll } from '../useResource';
import { bindingFindings, kernelName, zonesByInterface } from '../zones';
import { Address } from './Address';
import { InterfaceEditor } from './InterfaceEditor';

import { ZoneBadge } from './ZoneBadge';

interface InterfacesScreenProps { api: ApiClient; session: Session; onSaved(): void; onSessionLost(): void }

// What the panel opens on: the saved objects as read just now, with their versions, and the port on the box.
// It keeps the snapshot it opened with, so a poll that fails to read the configuration does not close it.
interface Editing { link?: SavedObject<SavedLink>; segment?: SavedObject<Segment>; live?: LinkStatus; snapshot: Snapshot }

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
  const all = saved ? join(links, saved.snapshot, saved.preview) : links.map((live): Row => ({ key: live.name, live }));
  const rows = all.filter((row) => row.saved || row.live?.linkType !== 'loopback');
  const showSaved = Boolean(saved);
  const segmentOf = (link: SavedLink): Segment | undefined => saved?.snapshot.value.segments.find((segment) => segment.link === link.id);
  const zoneName = (id: string) => saved?.snapshot.value.zones.find((zone) => zone.id === id)?.name ?? id;
  // The pill by an interface's name is the dashboard's: one rule for which zone an interface is in.
  const zones = saved ? zonesByInterface(saved.snapshot, saved.preview) : undefined;
  // The panel reads the objects again, so a save is guarded by the versions they have now.
  const edit = async (key: string, live: LinkStatus | undefined, link: SavedLink | undefined, segment: Segment | undefined) => {
    if (opening) return;
    setOpening(key);
    setError('');
    try {
      const [savedLink, savedSegment] = await Promise.all([
        link ? api.configObject<SavedLink>('links', link.id) : Promise.resolve(undefined),
        segment ? api.configObject<Segment>('segments', segment.id) : Promise.resolve(undefined),
      ]);
      // The two reads are apart: a segment moved to another link meanwhile would be written back onto this one.
      if (savedSegment && savedSegment.value.link !== savedLink?.value.id) setError('This interface was changed by someone else while it was opening. Refresh, then try again.');
      else if (saved) setEditing({ link: savedLink, segment: savedSegment, live, snapshot: saved.snapshot });
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

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div><Title order={2}>Interfaces</Title><Text c="dimmed">Network links the box reports now, beside what the saved configuration puts on them.</Text></div>
        <Button size="xs" variant="default" loading={resource.loading} onClick={() => void resource.reload()}>Refresh</Button>
      </Group>
      {error && <Alert color="red" role="alert" title="The interface could not be opened" withCloseButton onClose={() => setError('')}>{error}</Alert>}
      {saveError && <Alert color="red" role="alert" title="The interface was not saved" withCloseButton onClose={() => setSaveError('')}>{saveError}</Alert>}
      {resource.error && <Alert color="red" role="alert" title="Interfaces request failed">{resource.error}</Alert>}
      {savedError && <Alert color="red" role="alert" title="The saved configuration could not be read">{savedError} Only live links are shown.</Alert>}
      {!canReadConfig && <Alert color="gray" role="status">Your account cannot read the saved configuration, so only live links are shown.</Alert>}
      {rows.length === 0 ? <Alert role="status">The box reports no network links.</Alert> : (
        <ScrollArea><Table verticalSpacing="sm" withTableBorder className="data-table"><Table.Thead><Table.Tr><Table.Th>Interface</Table.Th><Table.Th>State</Table.Th><Table.Th>Addresses</Table.Th>{showSaved && <><Table.Th>Saved configuration</Table.Th><Table.Th>Addressing</Table.Th></>}{editable && <Table.Th><VisuallyHidden>Actions</VisuallyHidden></Table.Th>}</Table.Tr></Table.Thead><Table.Tbody>
          {rows.map(({ key, live, saved: link, state, finding, conflict }) => {
            const segment = link && segmentOf(link);
            return (
              <Table.Tr key={key}>
                <Table.Td data-label="Interface">
                  {live ? <Group gap="xs"><Text ff="monospace" fw={600}>{live.name}</Text><ZoneBadge name={zones?.get(live.name)} /></Group> : <Text c="dimmed">No device</Text>}
                  <Text c="dimmed" size="sm"><Text span ff="monospace" inherit>{live?.linkAddress || link?.permanentMac || 'no MAC'}</Text> · {live?.linkType ?? link?.kind} · MTU {live?.mtu ?? link?.mtu}</Text>
                </Table.Td>
                <Table.Td data-label="State">{live ? <Badge variant="dot" color={stateColor[live.operState] ?? 'gray'}>{stateLabel(live.operState)}</Badge> : <Badge variant="dot" color={state === 'Disabled' ? 'gray' : 'red'}>{state}</Badge>}</Table.Td>
                <Table.Td data-label="Addresses">{live?.addresses.length ? live.addresses.map((address) => <Address key={`${address.address}/${address.prefixLength}`} value={`${address.address}/${address.prefixLength}`} />) : <Text c="dimmed">—</Text>}</Table.Td>
                {showSaved && (
                  <>
                    <Table.Td data-label="Saved configuration">
                      {link ? (
                        <>
                          <Group gap="xs"><Text fw={600}>{link.name}</Text>{segment && saved?.snapshot.value.management?.segment === segment.id && <Badge variant="light">Management</Badge>}{live && !link.enabled && <Badge color="gray" variant="light">Disabled</Badge>}</Group>
                          {/* The zone is a badge, as everywhere; here only when the name column does not already show it. */}
                          <Group gap="xs">
                            <Text c="dimmed" size="sm">{link.kind} link{segment ? <> · segment {segment.name}</> : ' · no segment'}</Text>
                            {segment && (!live || zones?.get(live.name) !== zoneName(segment.zone)) && <ZoneBadge name={zoneName(segment.zone)} />}
                          </Group>
                        </>
                      ) : <Text c="dimmed">Not in the configuration</Text>}
                      {conflict && <Badge color="red" variant="light">Conflict: several saved links</Badge>}
                      {finding && <Text size="sm" className="error-text">{finding}</Text>}
                    </Table.Td>
                    {/* What is saved, which may not be applied yet: the live addresses are in their own column. */}
                    <Table.Td data-label="Addressing">{segment ? ([['IPv4', ipv4Modes, segment.addressing.ipv4], ['IPv6', ipv6Modes, segment.addressing.ipv6]] as const).map(([family, modes, value]) => (
                      <Text key={family} size="sm" c={value.mode === 'none' ? 'dimmed' : undefined}>
                        {family} {modeLabel(modes, value.mode)}
                        {(value as { addresses?: string[] }).addresses?.map((address) => <Address key={address} value={address} />)}
                      </Text>
                    )) : <Text c="dimmed">—</Text>}</Table.Td>
                  </>
                )}
                {editable && (
                  <Table.Td>
                    {/* Each of two saved links on one port is its own object: either can be edited or removed. */}
                    {link
                      ? <Button size="xs" variant="default" loading={opening === key} disabled={Boolean(opening) && opening !== key} aria-label={`Edit ${link.name}`} onClick={() => void edit(key, live, link, segment)}>Edit</Button>
                      : isPort(live) && <Button size="xs" variant="light" loading={opening === key} disabled={Boolean(opening) && opening !== key} aria-label={`Add ${live?.name} to the configuration`} onClick={() => void edit(key, live, undefined, undefined)}>Add</Button>}
                  </Table.Td>
                )}
              </Table.Tr>
            );
          })}
        </Table.Tbody></Table></ScrollArea>
      )}
      {editing && (
        <InterfaceEditor key={editing.link?.value.id ?? editing.live?.name} {...editing} onClose={() => setEditing(undefined)}
          onSessionLost={onSessionLost} onAbandoned={setSaveError} onSave={save} />
      )}
    </Stack>
  );
}
