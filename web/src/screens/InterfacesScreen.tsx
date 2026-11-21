import { Alert, Badge, Group, Loader, ScrollArea, Stack, Table, Text, Title } from '@mantine/core';
import { useCallback } from 'react';
import { ApiClient, reportError } from '../api';
import type { Finding, LinkStatus, Preview, SavedLink, Segment, Session, Snapshot } from '../types';
import { useResource } from '../useResource';

interface InterfacesScreenProps { api: ApiClient; session: Session; onSessionLost(): void }

const namePrefix: Record<string, string> = { vlan: 'vl', bridge: 'br', bond: 'bo', pppoe: 'pp' };

// kernelName mirrors internal/render: a virtual link's interface name derives from its ID, and an
// Ethernet link is the interface the preview resolved from its permanent MAC.
function kernelName(link: SavedLink, resolved: Map<string, string>) {
  if (link.kind === 'ethernet') return resolved.get(link.id);
  return namePrefix[link.kind] + link.id.slice(link.id.indexOf('_') + 1).replaceAll('-', '').slice(0, 12);
}

// A binding finding means the renderer will not put the link on any device, whatever its name matches.
const bindingCode = /^(HARDWARE_|NAME_COLLISION$)/;
const unboundLabel: Record<string, string> = { HARDWARE_MISSING: 'Missing', HARDWARE_AMBIGUOUS: 'Unresolved', NAME_COLLISION: 'Not realized' };

interface Row { key: string; live?: LinkStatus; saved?: SavedLink; state?: string; finding?: string; conflict?: boolean }

function join(links: LinkStatus[], snapshot: Snapshot, preview: Preview): Row[] {
  const resolved = new Map(preview.resolved.map((item) => [item.link, item.interface]));
  const findings = new Map<number, Finding>();
  for (const finding of preview.findings) {
    const index = /^\/links\/(\d+)(\/|$)/.exec(finding.path)?.[1];
    if (index !== undefined && bindingCode.test(finding.code) && !findings.has(Number(index))) findings.set(Number(index), finding);
  }
  const onDevice = new Map<string, SavedLink[]>();
  const unbound: Row[] = [];
  snapshot.value.links.forEach((link, index) => {
    const finding = findings.get(index);
    const name = finding ? undefined : kernelName(link, resolved);
    if (name && links.some((live) => live.name === name)) onDevice.set(name, [...(onDevice.get(name) ?? []), link]);
    else unbound.push({ key: link.id, saved: link, state: finding ? unboundLabel[finding.code] ?? 'Not realized' : link.kind === 'ethernet' ? 'Unresolved' : 'Missing', finding: finding?.message });
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
  const rows: Row[] = saved ? join(links, saved.snapshot, saved.preview) : links.map((live) => ({ key: live.name, live }));
  const showSaved = Boolean(saved);
  const segmentOf = (link: SavedLink): Segment | undefined => saved?.snapshot.value.segments.find((segment) => segment.link === link.id);
  const zoneName = (id: string) => saved?.snapshot.value.zones.find((zone) => zone.id === id)?.name ?? id;

  return (
    <Stack>
      <div><Title order={2}>Interfaces</Title><Text c="dimmed">Network links the box reports now, beside what the saved configuration puts on them.</Text></div>
      {resource.error && <Alert color="red" role="alert" title="Interfaces request failed">{resource.error}</Alert>}
      {savedError && <Alert color="red" role="alert" title="The saved configuration could not be read">{savedError} Only live links are shown.</Alert>}
      {!canReadConfig && <Alert color="gray" role="status">Your account cannot read the saved configuration, so only live links are shown.</Alert>}
      {rows.length === 0 ? <Alert role="status">The box reports no network links.</Alert> : (
        <ScrollArea><Table striped><Table.Thead><Table.Tr><Table.Th>Interface</Table.Th><Table.Th>MAC address</Table.Th><Table.Th>Kind</Table.Th><Table.Th>State</Table.Th><Table.Th>MTU</Table.Th><Table.Th>Addresses</Table.Th>{showSaved && <><Table.Th>Saved link</Table.Th><Table.Th>Segment</Table.Th><Table.Th>Zone</Table.Th><Table.Th>Addressing</Table.Th></>}</Table.Tr></Table.Thead><Table.Tbody>
          {rows.map(({ key, live, saved: link, state, finding, conflict }) => {
            const segment = link && segmentOf(link);
            return (
              <Table.Tr key={key}>
                <Table.Td><Text ff="monospace">{live?.name ?? '—'}</Text></Table.Td>
                <Table.Td><Text ff="monospace" size="sm">{live?.linkAddress || link?.permanentMac || '—'}</Text></Table.Td>
                <Table.Td>{live?.linkType ?? link?.kind}</Table.Td>
                <Table.Td>{live ? live.operState : <Badge color="red">{state}</Badge>}</Table.Td>
                <Table.Td>{live?.mtu ?? link?.mtu}</Table.Td>
                <Table.Td><Text ff="monospace" size="sm">{live?.addresses.map((address) => `${address.address}/${address.prefixLength}`).join(', ') || '—'}</Text></Table.Td>
                {showSaved && (
                  <>
                    <Table.Td>
                      {link ? <Text>{link.name} <Text span c="dimmed" size="sm">{link.kind}{link.enabled ? '' : ', disabled'}</Text></Text> : <Badge color="gray">Unused</Badge>}
                      {conflict && <Badge color="red" variant="light">Conflict: several saved links</Badge>}
                      {finding && <Text size="sm" c="red">{finding}</Text>}
                    </Table.Td>
                    <Table.Td>{segment ? <Group gap="xs"><Text>{segment.name}</Text>{saved?.snapshot.value.management?.segment === segment.id && <Badge variant="light">Management</Badge>}</Group> : '—'}</Table.Td>
                    <Table.Td>{segment ? zoneName(segment.zone) : '—'}</Table.Td>
                    <Table.Td>{segment ? `IPv4 ${segment.addressing.ipv4.mode}, IPv6 ${segment.addressing.ipv6.mode}` : '—'}</Table.Td>
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
