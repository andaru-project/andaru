// SPDX-License-Identifier: AGPL-3.0-or-later
import {
  Anchor,
  Badge,
  Button,
  Card,
  Group,
  Progress,
  SimpleGrid,
  Stack,
  Table,
  Text,
  Title,
} from '@mantine/core';
import { Link, useNavigate } from 'react-router';
import {
  type Device,
  type NetplaneEvent,
  type NetworkState,
  type TopologyGraph,
  type VLANFlow,
} from '../api';
import { DriverBadge, FindingBadge } from '../components/Badges';
import { AlertRow, CardHeader, FindingCounts } from '../components/ui';
import { devicePath, netplanePath, vlanFlowPath } from '../routing';
import {
  describeFinding,
  findingKey,
  findingsForDevice,
  getSfpAlerts,
  portLabel,
} from '../vlanUtils';

interface Props {
  state: NetworkState | null;
  topology: TopologyGraph | null;
  flow: VLANFlow | null;
  events: NetplaneEvent[];
  sseConnected: boolean;
  devicesById: Map<string, Device>;
}

export function OverviewScreen({
  state,
  topology,
  flow,
  events,
  sseConnected,
  devicesById,
}: Props) {
  const navigate = useNavigate();

  const devices = state?.devices ?? [];
  const links = state?.links ?? [];
  const clients = state?.clients ?? [];
  const gateways = state?.gateways ?? [];
  const firewalls = state?.firewalls ?? [];
  const findings = flow?.findings ?? [];
  // No report yet (first load, or it has never loaded): VLAN health is unknown, not clean.
  const flowLoaded = flow != null;

  const onlineDevices = devices.filter((d) => d.online).length;
  const offlineDevices = devices.filter((d) => !d.online);
  const drivers = Array.from(new Set(devices.map((d) => d.driver)));

  const onlineClients = clients.filter((c) => c.online);
  const wiredClients = onlineClients.filter((c) => c.connection_type === 'wired').length;
  const wifiClients = onlineClients.filter((c) => c.connection_type === 'wireless').length;

  const leakCount = findings.filter((f) => f.kind === 'leak').length;

  const sfpAlerts = getSfpAlerts(links);
  const totalAttention = offlineDevices.length + findings.length + sfpAlerts.length;

  // WAN & PoE KPI: only render fields the API actually returned.
  const primaryGw = gateways.find((g) => g.is_default) ?? gateways[0] ?? null;
  const poeDevices = devices.filter((d) => d.poe_budget != null);
  const poeTotalBudget = poeDevices.reduce((s, d) => s + (d.poe_budget?.total_watts ?? 0), 0);
  const poeUsedBudget = poeDevices.reduce((s, d) => s + (d.poe_budget?.used_watts ?? 0), 0);
  const poeLinks = links.filter((l) => l.poe?.supported && l.poe.enabled && l.poe.watts != null);
  const poeLinksWatts = poeLinks.reduce((s, l) => s + (l.poe?.watts ?? 0), 0);
  const hasPoeData = poeDevices.length > 0 || poeLinks.length > 0;
  // The KPI shows budget usage when any device reports a budget, else the port draw sum.
  const poeCoverage = poeTotalBudget > 0 ? poeDevices.length : new Set(poeLinks.map((l) => l.device_id)).size;
  const poeCoverageText = `across ${poeCoverage} PoE device${poeCoverage === 1 ? '' : 's'}`;
  const hasWanOrPoe = primaryGw != null || hasPoeData;

  const generatedAt = flow?.generated_at || topology?.generated_at;

  return (
    <Stack gap="md">
      <Group justify="space-between" align="flex-end" wrap="wrap" gap="sm">
        <div>
          <Title order={2} size="h3">Overview</Title>
          <Text size="sm" c="dimmed">
            Cross-vendor status across {devices.length} device{devices.length === 1 ? '' : 's'}
            {drivers.length > 0 ? ` and ${drivers.length} driver${drivers.length === 1 ? '' : 's'}` : ''}
            {generatedAt ? ` · updated ${new Date(generatedAt).toLocaleTimeString()}` : ''}
          </Text>
        </div>
        <Group gap="xs">
          <Button size="xs" variant="default" component={Link} to={netplanePath('/topology')}>
            Open Topology
          </Button>
          <Button size="xs" component={Link} to={vlanFlowPath()}>
            Inspect VLAN Flow{findings.length > 0 ? ` (${findings.length})` : ''}
          </Button>
        </Group>
      </Group>

      {/* Compact 4-KPI Strip */}
      <SimpleGrid cols={{ base: 2, md: 4 }} spacing="sm">
        <button type="button" className="np-kpi-btn" onClick={() => void navigate(netplanePath('/devices'))}>
          <Text size="xs" fw={600} c="dimmed" tt="uppercase">Devices Online</Text>
          <Text size="xl" fw={700} ff="monospace">{onlineDevices} / {devices.length}</Text>
          <Group gap={6} wrap="wrap">
            <Badge size="xs" variant="dot" color={offlineDevices.length === 0 ? 'green' : 'red'}>
              {offlineDevices.length === 0 ? 'All online' : `${offlineDevices.length} offline`}
            </Badge>
            {drivers.length > 0 && (
              <Text size="xs" c="dimmed">{drivers.length} driver{drivers.length === 1 ? '' : 's'}</Text>
            )}
          </Group>
        </button>

        <button type="button" className="np-kpi-btn" onClick={() => void navigate(vlanFlowPath())}>
          <Text size="xs" fw={600} c="dimmed" tt="uppercase">VLAN Health</Text>
          <Text
            size="xl"
            fw={700}
            c={!flowLoaded ? 'dimmed' : leakCount > 0 ? 'red' : findings.length > 0 ? 'orange' : 'green'}
          >
            {!flowLoaded
              ? 'Unavailable'
              : findings.length === 0
                ? 'Clean'
                : `${findings.length} Issue${findings.length === 1 ? '' : 's'}`}
          </Text>
          <FindingCounts
            findings={findings}
            gap={4}
            empty={
              <Text size="xs" c="dimmed">
                {flowLoaded ? `${flow.vlans.length} VLANs traced` : 'No VLAN flow report yet'}
              </Text>
            }
          />
        </button>

        <button type="button" className="np-kpi-btn" onClick={() => void navigate(netplanePath('/clients'))}>
          <Text size="xs" fw={600} c="dimmed" tt="uppercase">Active Clients</Text>
          <Text size="xl" fw={700} ff="monospace">{onlineClients.length}</Text>
          <Text size="xs" c="dimmed">
            {wiredClients} wired · {wifiClients} wireless
          </Text>
        </button>

        {hasWanOrPoe ? (
          <button
            type="button"
            className="np-kpi-btn"
            onClick={() =>
              void navigate(
                primaryGw
                  ? devicePath(primaryGw.device_id)
                  : poeDevices[0]
                    ? devicePath(poeDevices[0].id)
                    : netplanePath('/devices'),
              )
            }
          >
            <Text size="xs" fw={600} c="dimmed" tt="uppercase">
              {primaryGw && hasPoeData ? 'WAN & PoE' : primaryGw ? 'WAN Gateway' : 'PoE Draw'}
            </Text>
            <Text size="xl" fw={700} ff="monospace">
              {primaryGw
                ? primaryGw.rtt_ms != null
                  ? `${primaryGw.rtt_ms.toFixed(1)} ms`
                  : primaryGw.online
                    ? 'Online'
                    : 'Offline'
                : poeTotalBudget > 0
                  ? `${poeUsedBudget.toFixed(1)} / ${poeTotalBudget.toFixed(0)}W`
                  : `${poeLinksWatts.toFixed(1)} W`}
            </Text>
            <Group gap={6} wrap="wrap">
              {!primaryGw && <Text size="xs" c="dimmed">{poeCoverageText}</Text>}
              {primaryGw && (
                <Badge size="xs" variant="light" color={primaryGw.online ? 'green' : 'red'}>
                  {primaryGw.loss_pct != null ? `${primaryGw.loss_pct.toFixed(1)}% loss` : primaryGw.name}
                </Badge>
              )}
              {primaryGw && hasPoeData && (
                <Text size="xs" c="dimmed" ff="monospace">
                  PoE {poeTotalBudget > 0 ? `${poeUsedBudget.toFixed(1)}/${poeTotalBudget.toFixed(0)}W` : `${poeLinksWatts.toFixed(1)}W`}{' '}
                  {poeCoverageText}
                </Text>
              )}
            </Group>
          </button>
        ) : (
          <button type="button" className="np-kpi-btn" onClick={() => void navigate(netplanePath('/topology'))}>
            <Text size="xs" fw={600} c="dimmed" tt="uppercase">Topology Links</Text>
            <Text size="xl" fw={700} ff="monospace">{topology?.edges.length ?? 0}</Text>
            <Text size="xs" c="dimmed">
              {topology?.nodes.filter((n) => n.kind === 'device').length ?? 0} nodes mapped
            </Text>
          </button>
        )}
      </SimpleGrid>

      {/* Attention Required */}
      <Card withBorder radius="md" p={0}>
        <CardHeader>
          <Group gap="xs">
            <Title order={4} size="h5">Attention Required</Title>
            <Badge
              size="sm"
              variant="light"
              color={
                totalAttention > 0
                  ? leakCount > 0 || offlineDevices.length > 0 ? 'red' : 'yellow'
                  : flowLoaded ? 'green' : 'gray'
              }
            >
              {totalAttention > 0
                ? `${totalAttention} Active Finding${totalAttention === 1 ? '' : 's'}`
                : flowLoaded ? 'All Clear' : 'VLAN report pending'}
            </Badge>
          </Group>
          <Text size="xs" c="dimmed">Reported by VLAN flow analysis, device polling, and SFP DOM</Text>
        </CardHeader>

        <div>
          {offlineDevices.map((d) => (
            <AlertRow
              key={`offline-${d.id}`}
              severity="crit"
              badge={<Badge size="xs" variant="light" color="red">CRIT · OFFLINE</Badge>}
              title={<Text size="sm" fw={600}>{d.name} is unreachable</Text>}
              subtitle={`${d.driver}${d.mgmt_ip ? ` · ${d.mgmt_ip}` : ''} · last seen ${new Date(d.last_seen).toLocaleString()}`}
              subtitleMono
              action={
                <Button size="compact-xs" variant="default" component={Link} to={devicePath(d.id)}>
                  Inspect Device →
                </Button>
              }
            />
          ))}

          {findings.map((f) => {
            const desc = flow ? describeFinding(f, flow, state, devicesById) : null;
            return (
              <AlertRow
                key={findingKey(f)}
                severity={f.severity}
                badge={<FindingBadge finding={f} />}
                title={<Text size="sm" fw={600}>{desc?.title ?? f.message}</Text>}
                subtitle={f.message}
                action={
                  <Button
                    size="compact-xs"
                    variant="default"
                    component={Link}
                    to={vlanFlowPath({ mode: 'single', vlanId: f.vlans[0], findingKey: findingKey(f) })}
                  >
                    Inspect in VLAN Flow →
                  </Button>
                }
              />
            );
          })}

          {sfpAlerts.map((l) => {
            const sev = l.sfp?.alert_state === 'crit' ? 'crit' : 'warn';
            return (
              <AlertRow
                key={`sfp-${l.device_id}-${l.if_name}`}
                severity={sev}
                badge={
                  <Badge size="xs" variant="light" color={sev === 'crit' ? 'red' : 'yellow'}>
                    {sev.toUpperCase()} · SFP DOM
                  </Badge>
                }
                title={
                  <Text size="sm" fw={600}>
                    Optical DOM {l.sfp?.alert_state} on {portLabel(devicesById, l.device_id, l.if_name)}
                  </Text>
                }
                subtitle={[
                  l.sfp?.rx_power_dbm != null ? `RX ${l.sfp.rx_power_dbm.toFixed(2)} dBm` : '',
                  l.sfp?.vendor ? `${l.sfp.vendor} ${l.sfp.part_number ?? ''}`.trim() : '',
                  l.counters ? `${l.counters.rx_errors} RX err` : '',
                ].filter(Boolean).join(' · ')}
                subtitleMono
                action={
                  <Button
                    size="compact-xs"
                    variant="default"
                    component={Link}
                    to={devicePath(l.device_id, l.if_name)}
                  >
                    Inspect Port →
                  </Button>
                }
              />
            );
          })}

          {totalAttention === 0 && (
            <AlertRow>
              <Text size="sm" c="dimmed">
                {flowLoaded
                  ? 'No VLAN leaks, dead-ends, offline devices, or optical alarms detected.'
                  : 'No offline devices or optical alarms detected. VLAN flow analysis is not available yet.'}
              </Text>
            </AlertRow>
          )}
        </div>
      </Card>

      {/* Two-Column Operational Summary */}
      <div className="np-grid-2-asym">
        <Card withBorder radius="md" p={0}>
          <CardHeader>
            <Title order={4} size="h5">Infrastructure Devices</Title>
            <Button size="compact-xs" variant="subtle" component={Link} to={netplanePath('/devices')}>
              View all →
            </Button>
          </CardHeader>
          <Table.ScrollContainer minWidth={0} type="native">
            <Table highlightOnHover className="data-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Device</Table.Th>
                  <Table.Th>Driver</Table.Th>
                  <Table.Th>Role</Table.Th>
                  <Table.Th>Mgmt IP</Table.Th>
                  <Table.Th>Ports</Table.Th>
                  <Table.Th>Health</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {devices.map((dev) => {
                  const devLinks = links.filter((l) => l.device_id === dev.id);
                  const up = devLinks.filter((l) => l.link_up).length;
                  const devFindings = findingsForDevice(findings, dev.id);
                  const hasLeak = devFindings.some((f) => f.kind === 'leak');
                  const hasWarn = devFindings.some((f) => f.kind !== 'leak') || getSfpAlerts(devLinks).length > 0;

                  return (
                    <Table.Tr
                      key={dev.id}
                      style={{ cursor: 'pointer' }}
                      onClick={() => void navigate(devicePath(dev.id))}
                    >
                      <Table.Td fw={600}>
                        <Anchor component={Link} to={devicePath(dev.id)} inherit c="inherit" onClick={(e) => e.stopPropagation()}>
                          {dev.name}
                        </Anchor>
                      </Table.Td>
                      <Table.Td><DriverBadge driver={dev.driver} /></Table.Td>
                      <Table.Td><Text size="xs" c="dimmed">{dev.role}</Text></Table.Td>
                      <Table.Td ff="monospace" fz="xs">{dev.mgmt_ip || '—'}</Table.Td>
                      <Table.Td ff="monospace" fz="xs">
                        {devLinks.length > 0 ? `${up}/${devLinks.length}` : '—'}
                      </Table.Td>
                      <Table.Td>
                        {!dev.online ? (
                          <Badge size="xs" color="red">Offline</Badge>
                        ) : hasLeak ? (
                          <Badge size="xs" variant="light" color="red">⚠ Leak</Badge>
                        ) : hasWarn ? (
                          <Badge size="xs" variant="light" color="yellow">Warn</Badge>
                        ) : (
                          <Badge size="xs" variant="light" color="green">Healthy</Badge>
                        )}
                      </Table.Td>
                    </Table.Tr>
                  );
                })}
                {devices.length === 0 && (
                  <Table.Tr>
                    <Table.Td colSpan={6}>
                      <Text size="sm" c="dimmed">No devices polled yet.</Text>
                    </Table.Td>
                  </Table.Tr>
                )}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Card>

        <Stack gap="md">
          {(gateways.length > 0 || firewalls.length > 0) && (
            <Card withBorder radius="md" p={0}>
              <CardHeader>
                <Title order={4} size="h5">Gateways & Firewall</Title>
                {firewalls[0] && (
                  <Badge size="xs" variant="light" color="teal">
                    {firewalls[0].engine}{firewalls[0].saved_version ? ` · v${firewalls[0].saved_version}` : ''}
                  </Badge>
                )}
              </CardHeader>
              <Stack gap="sm" p="md">
                {gateways.map((g) => (
                  <Group key={`${g.device_id}-${g.name}`} justify="space-between" wrap="wrap" gap="xs">
                    <div>
                      <Text size="sm" fw={600}>
                        {g.name}
                        {g.family && <Text span size="xs" c="dimmed" ff="monospace"> ({g.family})</Text>}
                      </Text>
                      <Text size="xs" c="dimmed" ff="monospace">
                        {g.gateway_ip}{g.monitor_ip ? ` · monitor ${g.monitor_ip}` : ''}
                      </Text>
                    </div>
                    <Badge size="xs" variant="light" color={g.online ? 'green' : 'red'} ff="monospace">
                      {g.online
                        ? g.rtt_ms != null
                          ? `${g.rtt_ms.toFixed(1)} ms${g.loss_pct != null ? ` · ${g.loss_pct.toFixed(1)}%` : ''}`
                          : 'ONLINE'
                        : 'OFFLINE'}
                    </Badge>
                  </Group>
                ))}

                {firewalls.map((fw) => (
                  <Stack
                    key={fw.device_id}
                    gap="xs"
                    pt={gateways.length > 0 ? 'xs' : 0}
                    style={gateways.length > 0 ? { borderTop: '1px solid var(--np-border-subtle)' } : undefined}
                  >
                    {fw.states_used != null && fw.states_max != null && fw.states_max > 0 && (
                      <div>
                        <Group justify="space-between" mb={4}>
                          <Text size="xs" c="dimmed">Conntrack States</Text>
                          <Text size="xs" ff="monospace">
                            {fw.states_used.toLocaleString()} / {fw.states_max.toLocaleString()}
                          </Text>
                        </Group>
                        <Progress
                          size="sm"
                          value={Math.max(2, Math.min(100, Math.round((fw.states_used / fw.states_max) * 100)))}
                          color="teal"
                        />
                      </div>
                    )}
                    {fw.services.length > 0 && (
                      <Group gap={6} wrap="wrap">
                        {fw.services.map((sv) => (
                          <Badge
                            key={sv.name}
                            size="xs"
                            variant="dot"
                            color={sv.state === 'running' ? 'green' : sv.state === 'degraded' ? 'yellow' : 'red'}
                          >
                            {sv.name}
                          </Badge>
                        ))}
                      </Group>
                    )}
                  </Stack>
                ))}
              </Stack>
            </Card>
          )}

          <Card withBorder radius="md" p={0}>
            <CardHeader>
              <Title order={4} size="h5">Recent Live Events (SSE)</Title>
              <Group gap="xs">
                <Badge size="xs" variant="dot" color={sseConnected ? 'teal' : 'gray'}>
                  {sseConnected ? 'Streaming' : 'Connecting'}
                </Badge>
                <Button size="compact-xs" variant="subtle" component={Link} to={netplanePath('/diagnostics')}>
                  All →
                </Button>
              </Group>
            </CardHeader>
            <div>
              {events.slice(0, 6).map((ev) => (
                <AlertRow
                  key={ev.id}
                  severity={ev.severity === 'crit' ? 'crit' : ev.severity === 'warn' ? 'warn' : undefined}
                  style={{ padding: '9px 14px' }}
                >
                  <div>
                    <Group gap={6} mb={2}>
                      <Badge
                        size="xs"
                        variant="light"
                        ff="monospace"
                        color={ev.severity === 'crit' ? 'red' : ev.severity === 'warn' ? 'yellow' : 'blue'}
                      >
                        {ev.type}
                      </Badge>
                      <Text size="xs" c="dimmed" ff="monospace">
                        {new Date(ev.timestamp).toLocaleTimeString()}
                      </Text>
                    </Group>
                    <Text size="xs">{ev.summary}</Text>
                  </div>
                </AlertRow>
              ))}
              {events.length === 0 && (
                <AlertRow>
                  <Text size="xs" c="dimmed">Waiting for live network transitions…</Text>
                </AlertRow>
              )}
            </div>
          </Card>
        </Stack>
      </div>
    </Stack>
  );
}
