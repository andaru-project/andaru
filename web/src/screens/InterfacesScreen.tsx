import { Alert, Badge, Group, Loader, ScrollArea, Stack, Table, Text, Title } from '@mantine/core';
import { useCallback } from 'react';
import { ApiClient, reportError } from '../api';
import { stateColor, type LinkStatus, type Preview, type SavedLink, type Segment, type Session, type Snapshot } from '../types';
import { useResource } from '../useResource';
import { bindingFindings, kernelName, zonesByInterface } from '../zones';
import { ZoneBadge } from './ZoneBadge';

interface InterfacesScreenProps { api: ApiClient; session: Session; onSessionLost(): void }

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

export function InterfacesScreen({ api, session, onSessionLost }: InterfacesScreenProps) {
  const canReadConfig = session.capabilities.includes('configuration.read');
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

  return (
    <Stack>
      <div><Title order={2}>Interfaces</Title><Text c="dimmed">Network links the box reports now, beside what the saved configuration puts on them.</Text></div>
      {resource.error && <Alert color="red" role="alert" title="Interfaces request failed">{resource.error}</Alert>}
      {savedError && <Alert color="red" role="alert" title="The saved configuration could not be read">{savedError} Only live links are shown.</Alert>}
      {!canReadConfig && <Alert color="gray" role="status">Your account cannot read the saved configuration, so only live links are shown.</Alert>}
      {rows.length === 0 ? <Alert role="status">The box reports no network links.</Alert> : (
        <ScrollArea><Table verticalSpacing="sm" withTableBorder className="data-table"><Table.Thead><Table.Tr><Table.Th>Interface</Table.Th><Table.Th>State</Table.Th><Table.Th>Addresses</Table.Th>{showSaved && <><Table.Th>Saved configuration</Table.Th><Table.Th>Addressing</Table.Th></>}</Table.Tr></Table.Thead><Table.Tbody>
          {rows.map(({ key, live, saved: link, state, finding, conflict }) => {
            const segment = link && segmentOf(link);
            return (
              <Table.Tr key={key}>
                <Table.Td data-label="Interface">
                  {live ? <Group gap="xs"><Text ff="monospace" fw={600}>{live.name}</Text><ZoneBadge name={zones?.get(live.name)} /></Group> : <Text c="dimmed">No device</Text>}
                  <Text c="dimmed" size="sm"><Text span ff="monospace" inherit>{live?.linkAddress || link?.permanentMac || 'no MAC'}</Text> · {live?.linkType ?? link?.kind} · MTU {live?.mtu ?? link?.mtu}</Text>
                </Table.Td>
                <Table.Td data-label="State">{live ? <Badge variant="dot" color={stateColor[live.operState] ?? 'gray'}>{live.operState.toLowerCase()}</Badge> : <Badge color={state === 'Disabled' ? 'gray' : 'red'}>{state}</Badge>}</Table.Td>
                <Table.Td data-label="Addresses">{live?.addresses.length ? live.addresses.map((address) => <Text key={`${address.address}/${address.prefixLength}`} ff="monospace" size="sm">{address.address}/{address.prefixLength}</Text>) : <Text c="dimmed">—</Text>}</Table.Td>
                {showSaved && (
                  <>
                    <Table.Td data-label="Saved configuration">
                      {link ? (
                        <>
                          <Group gap="xs"><Text fw={600}>{link.name}</Text>{segment && saved?.snapshot.value.management?.segment === segment.id && <Badge variant="light">Management</Badge>}{live && !link.enabled && <Badge color="gray" variant="light">Disabled</Badge>}</Group>
                          <Text c="dimmed" size="sm">{link.kind} link{segment ? <> · segment {segment.name} · zone {zoneName(segment.zone)}</> : ' · no segment'}</Text>
                        </>
                      ) : <Text c="dimmed">Not in the configuration</Text>}
                      {conflict && <Badge color="red" variant="light">Conflict: several saved links</Badge>}
                      {finding && <Text size="sm" className="error-text">{finding}</Text>}
                    </Table.Td>
                    <Table.Td data-label="Addressing">{segment ? <><Text size="sm">IPv4 {segment.addressing.ipv4.mode}</Text><Text size="sm">IPv6 {segment.addressing.ipv6.mode}</Text></> : <Text c="dimmed">—</Text>}</Table.Td>
                  </>
                )}
              </Table.Tr>
            );
          })}
        </Table.Tbody></Table></ScrollArea>
      )}
    </Stack>
  );
}
