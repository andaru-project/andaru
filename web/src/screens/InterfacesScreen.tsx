import { Alert, Badge, Group, Loader, ScrollArea, Stack, Table, Text, Title } from '@mantine/core';
import { useCallback } from 'react';
import { ApiClient } from '../api';
import type { LinkStatus, SavedLink, Segment, Session } from '../types';
import { useResource } from '../useResource';

interface InterfacesScreenProps { api: ApiClient; session: Session; onSessionLost(): void }

const namePrefix: Record<string, string> = { vlan: 'vl', bridge: 'br', bond: 'bo', pppoe: 'pp' };

// kernelName mirrors internal/render: a virtual link's interface name derives from its ID, and an
// Ethernet link is the interface the preview resolved from its permanent MAC.
function kernelName(link: SavedLink, resolved: Map<string, string>) {
  if (link.kind === 'ethernet') return resolved.get(link.id);
  return namePrefix[link.kind] + link.id.slice(link.id.indexOf('_') + 1).replaceAll('-', '').slice(0, 12);
}

interface Row { key: string; live?: LinkStatus; saved?: SavedLink }

export function InterfacesScreen({ api, session, onSessionLost }: InterfacesScreenProps) {
  const canReadConfig = session.capabilities.includes('configuration.read');
  const load = useCallback(async () => {
    const [links, snapshot] = await Promise.all([api.links(), canReadConfig ? api.config() : Promise.resolve(null)]);
    const preview = snapshot ? await api.previewNetwork(snapshot.savedVersion) : null;
    return { links: links.items, snapshot, preview };
  }, [api, canReadConfig]);
  const resource = useResource(load, onSessionLost);

  if (!resource.data) return resource.error ? <Alert color="red" role="alert" title="Interfaces request failed">{resource.error}</Alert> : <Loader aria-label="Loading interfaces" />;

  const { links, snapshot, preview } = resource.data;
  const resolved = new Map(preview?.resolved.map((item) => [item.link, item.interface]));
  const savedByName = new Map<string, SavedLink>();
  const missing: SavedLink[] = [];
  for (const link of snapshot?.value.links ?? []) {
    const name = kernelName(link, resolved);
    if (name && links.some((live) => live.name === name)) savedByName.set(name, link);
    else missing.push(link);
  }
  const rows: Row[] = [
    ...links.map((live) => ({ key: live.name, live, saved: savedByName.get(live.name) })),
    ...missing.map((saved) => ({ key: saved.id, saved })),
  ];
  const segmentOf = (link: SavedLink): Segment | undefined => snapshot?.value.segments.find((segment) => segment.link === link.id);
  const zoneName = (id: string) => snapshot?.value.zones.find((zone) => zone.id === id)?.name ?? id;

  return (
    <Stack>
      <div><Title order={2}>Interfaces</Title><Text c="dimmed">Network links the box reports now, beside what the saved configuration puts on them.</Text></div>
      {resource.error && <Alert color="red" role="alert" title="Interfaces request failed">{resource.error}</Alert>}
      {!canReadConfig && <Alert color="gray" role="status">Your account cannot read the saved configuration, so only live links are shown.</Alert>}
      {rows.length === 0 ? <Alert role="status">The box reports no network links.</Alert> : (
        <ScrollArea><Table striped><Table.Thead><Table.Tr><Table.Th>Interface</Table.Th><Table.Th>MAC address</Table.Th><Table.Th>Kind</Table.Th><Table.Th>State</Table.Th><Table.Th>MTU</Table.Th><Table.Th>Addresses</Table.Th>{canReadConfig && <><Table.Th>Saved link</Table.Th><Table.Th>Segment</Table.Th><Table.Th>Zone</Table.Th><Table.Th>Addressing</Table.Th></>}</Table.Tr></Table.Thead><Table.Tbody>
          {rows.map(({ key, live, saved }) => {
            const segment = saved && segmentOf(saved);
            return (
              <Table.Tr key={key}>
                <Table.Td><Text ff="monospace">{live?.name ?? '—'}</Text></Table.Td>
                <Table.Td><Text ff="monospace" size="sm">{live?.linkAddress || saved?.permanentMac || '—'}</Text></Table.Td>
                <Table.Td>{live?.linkType ?? saved?.kind}</Table.Td>
                <Table.Td>{live ? live.operState : <Badge color="red">Missing</Badge>}</Table.Td>
                <Table.Td>{live?.mtu ?? saved?.mtu}</Table.Td>
                <Table.Td><Text ff="monospace" size="sm">{live?.addresses.map((address) => `${address.address}/${address.prefixLength}`).join(', ') || '—'}</Text></Table.Td>
                {canReadConfig && (
                  <>
                    <Table.Td>{saved ? <Text>{saved.name} <Text span c="dimmed" size="sm">{saved.kind}{saved.enabled ? '' : ', disabled'}</Text></Text> : <Badge color="gray">Unused</Badge>}</Table.Td>
                    <Table.Td>{segment ? <Group gap="xs"><Text>{segment.name}</Text>{snapshot?.value.management?.segment === segment.id && <Badge variant="light">Management</Badge>}</Group> : '—'}</Table.Td>
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
