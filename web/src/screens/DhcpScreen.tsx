import { Alert, Anchor, Badge, Button, Group, Loader, NativeSelect, ScrollArea, SegmentedControl, Stack, Switch, Table, Tabs, Text, TextInput, Title, VisuallyHidden } from '@mantine/core';
import { useCallback, useState } from 'react';
import { Link } from 'react-router';
import { ApiClient, reportError } from '../api';
import {
  formatLeaseTime, isDelegatedIpv6, leaseMatchesStatic4, leaseMatchesStatic6, raModeOf, servedIpv4, servedIpv6Static, summarizeDhcp4Extras, summarizeDhcp6Extras,
} from '../dhcpEdit';
import { formatTimestamp, useTimeZone } from '../timezone';
import type { Dhcp4Entry, Dhcp6Entry, Lease4, Lease6, LeaseState, LeaseView, SavedObject, Segment, Session, Snapshot } from '../types';
import { useResource, useVisiblePoll } from '../useResource';
import { zoneColorMap } from '../zones';
import { Address } from './Address';
import { DhcpEditor, type DhcpEditorTarget } from './DhcpEditor';
import { MakeStaticModal, type MakeStaticTarget } from './MakeStaticModal';
import { ZoneBadge } from './ZoneBadge';

interface DhcpScreenProps {
  api: ApiClient;
  session: Session;
  onSaved(): void;
  onSessionLost(): void;
}

const leaseStateColor: Record<LeaseState, string> = {
  active: 'green',
  declined: 'red',
  expired: 'gray',
  released: 'gray',
  unknown: 'gray',
};

const leaseStateLabel: Record<LeaseState, string> = {
  active: 'Active',
  declined: 'Declined',
  expired: 'Expired',
  released: 'Released',
  unknown: 'Unknown',
};

type UnifiedLease =
  | { key: string; family: 'v4'; lease: Lease4 }
  | { key: string; family: 'v6'; lease: Lease6 };

function DhcpStatusCell({ serving, active, canReadStatus }: { serving: boolean; active: number; canReadStatus: boolean }) {
  return (
    <Table.Td data-label="Status">
      {serving ? (
        <Group gap="xs">
          <Badge variant="dot" color="green">Serving</Badge>
          {canReadStatus && active > 0 && <Text size="xs" c="dimmed" className="tabular">{active} active</Text>}
        </Group>
      ) : (
        <Badge variant="dot" color="gray">Off</Badge>
      )}
    </Table.Td>
  );
}

function DhcpExtrasCell({ serving, extras }: { serving: boolean; extras: string[] }) {
  return (
    <Table.Td data-label="Options & static">
      {serving ? (
        extras.length > 0
          ? <Text size="sm">{extras.join(' · ')}</Text>
          : <Text size="sm" c="dimmed">Defaults</Text>
      ) : <Text c="dimmed">—</Text>}
    </Table.Td>
  );
}

function DhcpActionCell({
  family, segmentName, serving, rowKey, opening, onClick,
}: {
  family: 'DHCPv4' | 'DHCPv6';
  segmentName: string;
  serving: boolean;
  rowKey: string;
  opening: string;
  onClick(): void;
}) {
  return (
    <Table.Td>
      <Button
        size="xs"
        variant={serving ? 'default' : 'light'}
        loading={opening === rowKey}
        disabled={Boolean(opening) && opening !== rowKey}
        aria-label={serving ? `Edit ${family} on ${segmentName}` : `Enable ${family} on ${segmentName}`}
        onClick={onClick}
      >
        {serving ? 'Edit' : 'Enable'}
      </Button>
    </Table.Td>
  );
}

export function DhcpScreen({ api, session, onSaved, onSessionLost }: DhcpScreenProps) {
  const canReadConfig = session.capabilities.includes('configuration.read');
  const canWrite = canReadConfig && session.capabilities.includes('configuration.write');
  const canReadStatus = session.capabilities.includes('status.read');
  const timeZone = useTimeZone();

  const [tab, setTab] = useState<string>(canReadConfig ? 'servers' : 'leases');
  const [editing, setEditing] = useState<DhcpEditorTarget>();
  const [opening, setOpening] = useState('');
  const [promoting, setPromoting] = useState<MakeStaticTarget>();
  const [error, setError] = useState('');
  const [saveError, setSaveError] = useState('');

  // Leases tab filters.
  const [search, setSearch] = useState('');
  const [familyFilter, setFamilyFilter] = useState<'all' | 'v4' | 'v6'>('all');
  const [segmentFilter, setSegmentFilter] = useState('');
  const [showInactive, setShowInactive] = useState(false);

  const load = useCallback(async () => {
    let configError = '';
    let leasesError = '';
    const loadConfig = async (): Promise<Snapshot | null> => {
      try {
        return await api.config();
      } catch (err) {
        reportError(err, onSessionLost, (msg) => { configError = msg; });
        return null;
      }
    };
    const loadLeases = async (): Promise<LeaseView | null> => {
      try {
        return await api.leases();
      } catch (err) {
        reportError(err, onSessionLost, (msg) => { leasesError = msg; });
        return null;
      }
    };
    const [snapshot, leases] = await Promise.all([
      canReadConfig ? loadConfig() : Promise.resolve(null),
      canReadStatus ? loadLeases() : Promise.resolve(null),
    ]);
    return { snapshot, leases, configError, leasesError };
  }, [api, canReadConfig, canReadStatus, onSessionLost]);

  const resource = useResource(load, onSessionLost);
  useVisiblePoll(resource.refresh, 10_000, canReadStatus);

  const loadedLeases = resource.data?.leases;
  const [seenLeases, setSeenLeases] = useState<LeaseView | null | undefined>();
  if (loadedLeases !== seenLeases) {
    setSeenLeases(loadedLeases);
    if (segmentFilter && loadedLeases
      && !loadedLeases.v4.some((l) => l.segment === segmentFilter)
      && !loadedLeases.v6.some((l) => l.segment === segmentFilter)) {
      setSegmentFilter('');
    }
  }

  if (!resource.data) {
    return resource.error
      ? <Alert color="red" role="alert" title="DHCP request failed">{resource.error}</Alert>
      : <Loader aria-label="Loading DHCP" />;
  }

  const { snapshot, leases, configError, leasesError } = resource.data;
  const activeTab = (tab === 'servers' && canReadConfig) || (tab === 'leases' && canReadStatus)
    ? tab
    : (canReadConfig ? 'servers' : 'leases');
  const segments = snapshot?.value.segments ?? [];
  const zones = new Map((snapshot?.value.zones ?? []).map((z) => [z.id, z.name]));
  const zoneColors = zoneColorMap(snapshot?.value.zones ?? []);
  const segmentById = new Map(segments.map((s) => [s.id, s]));
  const dhcp4BySegment = new Map((snapshot?.value.dhcp ?? []).map((e) => [e.segment, e]));
  const dhcp6BySegment = new Map((snapshot?.value.dhcp6 ?? []).map((e) => [e.segment, e]));

  const v4Eligible = segments.filter((s) => servedIpv4(s) !== null || dhcp4BySegment.has(s.id));
  const v6Eligible = segments.filter((s) => servedIpv6Static(s) !== null || isDelegatedIpv6(s) || dhcp6BySegment.has(s.id));

  const activeCount4 = (segmentId: string) => (leases?.v4 ?? []).filter((l) => l.segment === segmentId && l.state === 'active').length;
  const activeCount6 = (segmentId: string) => (leases?.v6 ?? []).filter((l) => l.segment === segmentId && l.state === 'active').length;
  const totalActiveLeases = (leases?.v4.filter((l) => l.state === 'active').length ?? 0) + (leases?.v6.filter((l) => l.state === 'active').length ?? 0);

  async function openEditor(family: 'v4' | 'v6', segment: Segment, existingId?: string) {
    if (opening) return;
    const key = `${family}:${segment.id}`;
    setOpening(key);
    setError('');
    try {
      if (family === 'v4') {
        const [freshSnapshot, saved] = await Promise.all([
          api.config(),
          existingId ? api.configObject<Dhcp4Entry>('dhcp', existingId) : Promise.resolve(undefined),
        ]);
        const freshSegment = freshSnapshot.value.segments.find((s) => s.id === segment.id) ?? segment;
        setEditing({ family: 'v4', segment: freshSegment, snapshot: freshSnapshot, saved });
      } else {
        const [freshSnapshot, saved] = await Promise.all([
          api.config(),
          existingId ? api.configObject<Dhcp6Entry>('dhcp6', existingId) : Promise.resolve(undefined),
        ]);
        const freshSegment = freshSnapshot.value.segments.find((s) => s.id === segment.id) ?? segment;
        setEditing({ family: 'v6', segment: freshSegment, snapshot: freshSnapshot, saved });
      }
    } catch (failure) {
      reportError(failure, onSessionLost, setError);
    } finally {
      setOpening('');
    }
  }

  const handleSaveV4 = async (value: Omit<Dhcp4Entry, 'id'>, saved?: SavedObject<Dhcp4Entry>) => {
    await api.saveDhcp4(value, saved);
    onSaved();
    void resource.reload();
  };
  const handleDeleteV4 = async (id: string, version: string) => {
    await api.deleteDhcp4(id, version);
    onSaved();
    void resource.reload();
  };
  const handleSaveV6 = async (value: Omit<Dhcp6Entry, 'id'>, saved?: SavedObject<Dhcp6Entry>) => {
    await api.saveDhcp6(value, saved);
    onSaved();
    void resource.reload();
  };
  const handleDeleteV6 = async (id: string, version: string) => {
    await api.deleteDhcp6(id, version);
    onSaved();
    void resource.reload();
  };

  // Unified leases list for the Leases tab.
  const allLeases: UnifiedLease[] = [
    ...(leases?.v4 ?? []).map((lease): UnifiedLease => ({ key: `v4:${lease.segment}:${lease.address}:${lease.mac}`, family: 'v4', lease })),
    ...(leases?.v6 ?? []).map((lease): UnifiedLease => ({ key: `v6:${lease.segment}:${lease.type}:${lease.address}:${lease.duid}`, family: 'v6', lease })),
  ];
  const leaseSegmentIds = Array.from(new Set(allLeases.map((item) => item.lease.segment).filter(Boolean)));
  const activeSegmentFilter = segmentFilter && leaseSegmentIds.includes(segmentFilter) ? segmentFilter : '';
  const searchTerms = search.toLowerCase().split(/\s+/).filter(Boolean);
  const hiddenInactiveCount = allLeases.filter((item) => item.lease.state === 'expired' || item.lease.state === 'released').length;

  const filteredLeases = allLeases.filter((item) => {
    if (!showInactive && (item.lease.state === 'expired' || item.lease.state === 'released')) return false;
    if (familyFilter !== 'all' && item.family !== familyFilter) return false;
    if (activeSegmentFilter && item.lease.segment !== activeSegmentFilter) return false;
    if (searchTerms.length === 0) return true;
    const segName = segmentById.get(item.lease.segment)?.name ?? item.lease.segment;
    const haystack = [
      item.lease.hostname,
      item.lease.address,
      item.family === 'v4' ? item.lease.mac : `${item.lease.duid} ${item.lease.mac ?? ''}`,
      segName,
      item.lease.state,
    ].join(' ').toLowerCase();
    return searchTerms.every((term) => haystack.includes(term));
  });

  const editable = canWrite && Boolean(snapshot);

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div>
          <Title order={2}>DHCP</Title>
          <Text c="dimmed">DHCPv4 and DHCPv6 servers per segment, static leases, custom options, and active leases.</Text>
        </div>
        <Button size="xs" variant="default" loading={resource.loading} onClick={() => void resource.reload()}>Refresh</Button>
      </Group>

      {error && <Alert color="red" role="alert" title="The DHCP server could not be opened" withCloseButton onClose={() => setError('')}>{error}</Alert>}
      {saveError && <Alert color="red" role="alert" title="The DHCP server was not saved" withCloseButton onClose={() => setSaveError('')}>{saveError}</Alert>}
      {configError && <Alert color="red" role="alert" title="The saved configuration could not be read">{configError}</Alert>}
      {leasesError && <Alert color="red" role="alert" title="Live leases could not be read">{leasesError}</Alert>}

      <Tabs value={activeTab} onChange={(next) => next && setTab(next)}>
        <Tabs.List>
          {canReadConfig && <Tabs.Tab value="servers">Servers</Tabs.Tab>}
          {canReadStatus && (
            <Tabs.Tab value="leases">
              Leases{leases ? ` (${totalActiveLeases})` : ''}
            </Tabs.Tab>
          )}
        </Tabs.List>

        {canReadConfig && (
          <Tabs.Panel value="servers" pt="md">
            {!snapshot ? (
              <Alert role="status">Saved configuration is unavailable.</Alert>
            ) : v4Eligible.length === 0 && v6Eligible.length === 0 ? (
              <Alert role="status" title="No eligible segments">
                DHCP requires a segment with a static IPv4 address, a static IPv6 /64, or a delegated IPv6 prefix. Configure addressing on{' '}
                <Anchor component={Link} to="/network/interfaces">Interfaces</Anchor>.
              </Alert>
            ) : (
              <Stack gap="lg">
                {v4Eligible.length > 0 && (
                  <Stack gap="xs">
                    <div>
                      <Title order={3} size="h4">DHCPv4 servers</Title>
                      <Text size="sm" c="dimmed">Segments with a static IPv4 subnet.</Text>
                    </div>
                    <ScrollArea>
                      <Table verticalSpacing="sm" withTableBorder className="data-table">
                        <Table.Thead>
                          <Table.Tr>
                            <Table.Th>Segment</Table.Th>
                            <Table.Th>Status</Table.Th>
                            <Table.Th>Ranges &amp; lease</Table.Th>
                            <Table.Th>Options &amp; static</Table.Th>
                            {editable && <Table.Th><VisuallyHidden>Actions</VisuallyHidden></Table.Th>}
                          </Table.Tr>
                        </Table.Thead>
                        <Table.Tbody>
                          {v4Eligible.map((segment) => {
                            const entry = dhcp4BySegment.get(segment.id);
                            const info = servedIpv4(segment);
                            const rowKey = `v4:${segment.id}`;
                            return (
                              <Table.Tr key={rowKey}>
                                <Table.Td data-label="Segment">
                                  <Group gap="xs">
                                    <Text fw={600}>{segment.name}</Text>
                                    <ZoneBadge name={zones.get(segment.zone) ?? segment.zone} color={zoneColors.get(segment.zone)} />
                                  </Group>
                                  {info && <Text size="sm" c="dimmed" ff="monospace">{info.subnet}</Text>}
                                </Table.Td>
                                <DhcpStatusCell serving={Boolean(entry)} active={activeCount4(segment.id)} canReadStatus={canReadStatus} />
                                <Table.Td data-label="Ranges & lease">
                                  {entry ? (
                                    <Stack gap={2}>
                                      {entry.ranges.map((r, i) => (
                                        <Group key={i} gap={4} wrap="wrap">
                                          <Address value={r.first} />
                                          <Text size="sm" c="dimmed">–</Text>
                                          <Address value={r.last} />
                                        </Group>
                                      ))}
                                      <Text size="xs" c="dimmed">Lease {formatLeaseTime(entry.leaseTime)}</Text>
                                    </Stack>
                                  ) : <Text c="dimmed">—</Text>}
                                </Table.Td>
                                <DhcpExtrasCell serving={Boolean(entry)} extras={entry ? summarizeDhcp4Extras(entry) : []} />
                                {editable && (
                                  <DhcpActionCell
                                    family="DHCPv4"
                                    segmentName={segment.name}
                                    serving={Boolean(entry)}
                                    rowKey={rowKey}
                                    opening={opening}
                                    onClick={() => void openEditor('v4', segment, entry?.id)}
                                  />
                                )}
                              </Table.Tr>
                            );
                          })}
                        </Table.Tbody>
                      </Table>
                    </ScrollArea>
                  </Stack>
                )}

                {v6Eligible.length > 0 && (
                  <Stack gap="xs">
                    <div>
                      <Title order={3} size="h4">DHCPv6 servers</Title>
                      <Text size="sm" c="dimmed">Segments with a static IPv6 /64 or a delegated prefix.</Text>
                    </div>
                    <ScrollArea>
                      <Table verticalSpacing="sm" withTableBorder className="data-table">
                        <Table.Thead>
                          <Table.Tr>
                            <Table.Th>Segment</Table.Th>
                            <Table.Th>Status</Table.Th>
                            <Table.Th>Ranges &amp; delegation</Table.Th>
                            <Table.Th>Options &amp; static</Table.Th>
                            {editable && <Table.Th><VisuallyHidden>Actions</VisuallyHidden></Table.Th>}
                          </Table.Tr>
                        </Table.Thead>
                        <Table.Tbody>
                          {v6Eligible.map((segment) => {
                            const entry = dhcp6BySegment.get(segment.id);
                            const static6 = servedIpv6Static(segment);
                            const delegated = isDelegatedIpv6(segment);
                            const raMode = raModeOf(segment);
                            const raUnannounced = Boolean(entry?.ranges?.length) && raMode !== 'managed' && raMode !== 'assisted';
                            const rowKey = `v6:${segment.id}`;
                            return (
                              <Table.Tr key={rowKey}>
                                <Table.Td data-label="Segment">
                                  <Group gap="xs">
                                    <Text fw={600}>{segment.name}</Text>
                                    <ZoneBadge name={zones.get(segment.zone) ?? segment.zone} color={zoneColors.get(segment.zone)} />
                                  </Group>
                                  <Text size="sm" c="dimmed" ff="monospace">
                                    {delegated ? `Delegated · subnet ${segment.addressing.ipv6.subnetId ?? '0'}` : static6?.prefix}
                                  </Text>
                                  {raUnannounced && (
                                    <Text size="xs" className="warning-text">RA mode is {raMode}; clients may not request DHCPv6 addresses.</Text>
                                  )}
                                </Table.Td>
                                <DhcpStatusCell serving={Boolean(entry)} active={activeCount6(segment.id)} canReadStatus={canReadStatus} />
                                <Table.Td data-label="Ranges & delegation">
                                  {entry ? (
                                    <Stack gap={2}>
                                      {(entry.ranges ?? []).map((r, i) => (
                                        <Group key={i} gap={4} wrap="wrap">
                                          <Address value={r.first} />
                                          <Text size="sm" c="dimmed">–</Text>
                                          <Address value={r.last} />
                                        </Group>
                                      ))}
                                      {entry.prefixDelegation && (
                                        <Text size="xs" ff="monospace">
                                          PD:{' '}
                                          {entry.prefixDelegation.prefix
                                            ? `${entry.prefixDelegation.prefix} → /${entry.prefixDelegation.delegatedLength}`
                                            : `subnet ${entry.prefixDelegation.subnetId} (/${entry.prefixDelegation.length} → /${entry.prefixDelegation.delegatedLength})`}
                                        </Text>
                                      )}
                                      <Text size="xs" c="dimmed">Lease {formatLeaseTime(entry.leaseTime)}</Text>
                                    </Stack>
                                  ) : <Text c="dimmed">—</Text>}
                                </Table.Td>
                                <DhcpExtrasCell serving={Boolean(entry)} extras={entry ? summarizeDhcp6Extras(entry) : []} />
                                {editable && (
                                  <DhcpActionCell
                                    family="DHCPv6"
                                    segmentName={segment.name}
                                    serving={Boolean(entry)}
                                    rowKey={rowKey}
                                    opening={opening}
                                    onClick={() => void openEditor('v6', segment, entry?.id)}
                                  />
                                )}
                              </Table.Tr>
                            );
                          })}
                        </Table.Tbody>
                      </Table>
                    </ScrollArea>
                  </Stack>
                )}
              </Stack>
            )}
          </Tabs.Panel>
        )}

        {canReadStatus && (
          <Tabs.Panel value="leases" pt="md">
            <Stack>
              <Group align="flex-end" gap="sm" wrap="wrap">
                <TextInput
                  className="grow"
                  label="Filter leases"
                  placeholder="Hostname, address, MAC, DUID, or segment"
                  value={search}
                  onChange={(e) => setSearch(e.currentTarget.value)}
                />
                <SegmentedControl
                  aria-label="Address family"
                  value={familyFilter}
                  onChange={(next) => setFamilyFilter(next as typeof familyFilter)}
                  data={[
                    { value: 'all', label: 'All' },
                    { value: 'v4', label: 'IPv4' },
                    { value: 'v6', label: 'IPv6' },
                  ]}
                />
                {leaseSegmentIds.length > 1 && (
                  <NativeSelect
                    label="Segment"
                    value={activeSegmentFilter}
                    onChange={(e) => setSegmentFilter(e.currentTarget.value)}
                    data={[
                      { value: '', label: 'All segments' },
                      ...leaseSegmentIds.map((id) => ({ value: id, label: segmentById.get(id)?.name ?? id })),
                    ]}
                  />
                )}
                <Switch
                  label={`Show expired & released${hiddenInactiveCount > 0 && !showInactive ? ` (${hiddenInactiveCount})` : ''}`}
                  checked={showInactive}
                  onChange={(e) => setShowInactive(e.currentTarget.checked)}
                  mb={6}
                />
              </Group>

              {filteredLeases.length === 0 ? (
                <Alert role="status" title="No matching leases">
                  {allLeases.length === 0
                    ? 'The DHCP servers have not reported any leases yet.'
                    : !showInactive && hiddenInactiveCount > 0 && searchTerms.length === 0 && familyFilter === 'all' && !activeSegmentFilter
                      ? `All ${hiddenInactiveCount} reported ${hiddenInactiveCount === 1 ? 'lease is' : 'leases are'} expired or released.`
                      : 'No lease matches the current filter.'}
                </Alert>
              ) : (
                <ScrollArea>
                  <Table verticalSpacing="sm" withTableBorder className="data-table">
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Client</Table.Th>
                        <Table.Th>Address</Table.Th>
                        <Table.Th>Segment</Table.Th>
                        <Table.Th>State</Table.Th>
                        <Table.Th>Expires</Table.Th>
                        {editable && <Table.Th><VisuallyHidden>Actions</VisuallyHidden></Table.Th>}
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {filteredLeases.map((item) => {
                        const seg = segmentById.get(item.lease.segment);
                        const isStatic = item.family === 'v4'
                          ? leaseMatchesStatic4(item.lease, snapshot ?? undefined)
                          : leaseMatchesStatic6(item.lease, snapshot ?? undefined);
                        const serverEntry = item.family === 'v4'
                          ? dhcp4BySegment.get(item.lease.segment)
                          : dhcp6BySegment.get(item.lease.segment);
                        const canPromote = editable && !isStatic && Boolean(seg) && Boolean(serverEntry);
                        return (
                          <Table.Tr key={item.key}>
                            <Table.Td data-label="Client">
                              <Group gap="xs">
                                <Text fw={600} c={item.lease.hostname ? undefined : 'dimmed'}>
                                  {item.lease.hostname || 'Unnamed client'}
                                </Text>
                                {isStatic && <Badge size="xs" variant="light" color="blue" className="plain-badge">Static</Badge>}
                              </Group>
                              <Text size="xs" c="dimmed" ff="monospace">
                                {item.family === 'v4' ? item.lease.mac : item.lease.duid}
                              </Text>
                              {item.family === 'v6' && item.lease.mac && (
                                <Text size="xs" c="dimmed" ff="monospace">{item.lease.mac}</Text>
                              )}
                            </Table.Td>
                            <Table.Td data-label="Address">
                              <Group gap="xs">
                                <Address value={item.lease.address} />
                                {item.family === 'v6' && item.lease.type === 'prefix' && (
                                  <Badge size="xs" variant="light" color="teal" className="plain-badge">Prefix</Badge>
                                )}
                              </Group>
                            </Table.Td>
                            <Table.Td data-label="Segment">
                              {seg ? (
                                <Group gap="xs">
                                  <Text size="sm">{seg.name}</Text>
                                  <ZoneBadge name={zones.get(seg.zone) ?? seg.zone} color={zoneColors.get(seg.zone)} />
                                </Group>
                              ) : (
                                <Text size="sm" c="dimmed">{item.lease.segment || '—'}</Text>
                              )}
                            </Table.Td>
                            <Table.Td data-label="State">
                              <Badge variant="dot" color={leaseStateColor[item.lease.state]}>
                                {leaseStateLabel[item.lease.state]}
                              </Badge>
                            </Table.Td>
                            <Table.Td data-label="Expires">
                              <Text size="sm" className="tabular">
                                {item.lease.expiresAt ? formatTimestamp(item.lease.expiresAt, timeZone) : 'Never'}
                              </Text>
                            </Table.Td>
                            {editable && (
                              <Table.Td>
                                {canPromote && seg && serverEntry && (
                                  <Button
                                    size="compact-xs"
                                    variant="light"
                                    aria-label={`Make ${item.lease.address} static`}
                                    onClick={() => setPromoting(
                                      item.family === 'v4'
                                        ? { family: 'v4', lease: item.lease, segment: seg, entryId: serverEntry.id }
                                        : { family: 'v6', lease: item.lease, segment: seg, entryId: serverEntry.id },
                                    )}
                                  >
                                    Make static
                                  </Button>
                                )}
                              </Table.Td>
                            )}
                          </Table.Tr>
                        );
                      })}
                    </Table.Tbody>
                  </Table>
                </ScrollArea>
              )}
            </Stack>
          </Tabs.Panel>
        )}
      </Tabs>

      {editing && (
        <DhcpEditor
          key={`${editing.family}:${editing.segment.id}:${editing.saved?.version ?? 'new'}`}
          target={editing}
          onClose={() => setEditing(undefined)}
          onSessionLost={onSessionLost}
          onAbandoned={setSaveError}
          onSaveV4={handleSaveV4}
          onDeleteV4={handleDeleteV4}
          onSaveV6={handleSaveV6}
          onDeleteV6={handleDeleteV6}
        />
      )}

      {promoting && (
        <MakeStaticModal
          key={`${promoting.family}:${promoting.entryId}:${promoting.lease.address}`}
          api={api}
          target={promoting}
          onClose={() => setPromoting(undefined)}
          onSaved={() => {
            onSaved();
            void resource.reload();
          }}
          onSessionLost={onSessionLost}
          onAbandoned={setSaveError}
        />
      )}
    </Stack>
  );
}
