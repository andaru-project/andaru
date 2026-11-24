// SPDX-License-Identifier: AGPL-3.0-or-later
import {
  Alert,
  Anchor,
  Badge,
  Card,
  Group,
  Loader,
  Progress,
  Select,
  Stack,
  Table,
  Text,
  Title,
} from '@mantine/core';
import { useEffect, useMemo, useState } from 'react';
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router';
import {
  api,
  type Device,
  type DeviceDetail,
  formatBps,
  formatBytes,
  formatSpeed,
  formatUptime,
  type NetworkState,
  type TopologyGraph,
  type VLANFlow,
  type VLANFlowVLAN,
} from '../api';
import { DriverBadge, VlanChip } from '../components/Badges';
import { PortFaceplate } from '../components/PortFaceplate';
import { CardHeader, Stat } from '../components/ui';
import { deviceIdFromPath, devicePath, netplanePath, vlanFlowPath } from '../routing';
import {
  type ColorPair,
  getClientVlanIssue,
  isStpBlocked,
} from '../vlanUtils';

interface Props {
  devices: Device[];
  state: NetworkState | null;
  topology: TopologyGraph | null;
  flow: VLANFlow | null;
  devicesById: Map<string, Device>;
  vlansById: Map<number, VLANFlowVLAN>;
  vlanColors: Map<number, ColorPair>;
  refreshNonce: number;
}

export function DeviceDetailScreen({
  devices,
  state,
  topology,
  flow,
  devicesById,
  vlansById,
  vlanColors,
  refreshNonce,
}: Props) {
  const deviceId = deviceIdFromPath(useLocation().pathname);
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();

  const selectedPortParam = searchParams.get('port');

  const [fetched, setFetched] = useState<{ id: string; detail: DeviceDetail | null; error: string | null }>({
    id: '',
    detail: null,
    error: null,
  });

  useEffect(() => {
    if (!deviceId) return;
    let active = true;
    api
      .getDevice(deviceId)
      .then((res) => {
        if (!active) return;
        setFetched({ id: deviceId, detail: res, error: null });
      })
      .catch((e) => {
        if (!active) return;
        const msg = e instanceof Error ? e.message : String(e);
        setFetched((prev) => ({
          id: deviceId,
          detail: prev.id === deviceId ? prev.detail : null,
          error: msg,
        }));
      });
    return () => {
      active = false;
    };
  }, [deviceId, refreshNonce]);

  const detail = fetched.id === deviceId ? fetched.detail : null;
  const error = fetched.id === deviceId ? fetched.error : null;
  const loading = Boolean(deviceId) && fetched.id !== deviceId;

  const zonesById = useMemo(
    () => new Map((detail?.zones ?? []).map((z) => [z.id, z.name])),
    [detail],
  );

  // Lookup unreachable SSIDs from /api/v1/vlans/flow for this device.
  const unreachableSsids = useMemo(() => {
    const set = new Set<string>();
    if (!flow || !deviceId) return set;
    for (const v of flow.vlans) {
      if (v.gateways.length === 0) continue;
      for (const s of v.ssids) {
        if (s.device_id === deviceId && !s.reachable) {
          set.add(`${s.radio}|${s.name}|${v.id}`);
        }
      }
    }
    return set;
  }, [flow, deviceId]);

  if (loading && !detail) {
    return (
      <Group justify="center" py="xl">
        <Loader size="sm" aria-label="Loading device details" />
      </Group>
    );
  }

  if (error && !detail) {
    return (
      <Stack gap="sm">
        <Anchor component={Link} to={netplanePath('/devices')} size="sm">← Back to Devices</Anchor>
        <Alert color="red" title="Could not load device">{error}</Alert>
      </Stack>
    );
  }

  if (!detail) return null;

  const dev = detail.device;
  const hasTelemetryCpu = dev.system_stats.cpu_pct != null;
  const hasTelemetryMem =
    dev.system_stats.mem_used_bytes != null &&
    dev.system_stats.mem_total_bytes != null &&
    dev.system_stats.mem_total_bytes > 0;
  const hasSensors = dev.sensors.length > 0;
  const hasFwUpdate = Boolean(dev.firmware.available && dev.firmware.available !== dev.firmware.current);

  const telemetryItems: { label: string; content: React.ReactNode }[] = [];
  if (dev.firmware.current || dev.firmware.available) {
    telemetryItems.push({
      label: 'Firmware',
      content: (
        <Group gap={6} wrap="wrap">
          <Text size="sm" ff="monospace" fw={600}>{dev.firmware.current || '—'}</Text>
          {hasFwUpdate && <Badge size="xs" variant="light" color="blue">→ {dev.firmware.available}</Badge>}
        </Group>
      ),
    });
  }
  if (dev.uptime_sec > 0) {
    telemetryItems.push({
      label: 'Uptime',
      content: <Text size="sm" ff="monospace" fw={600}>{formatUptime(dev.uptime_sec)}</Text>,
    });
  }
  if (hasTelemetryCpu || hasTelemetryMem || hasSensors) {
    const parts: string[] = [];
    if (hasTelemetryCpu) parts.push(`CPU ${dev.system_stats.cpu_pct!.toFixed(0)}%`);
    if (hasTelemetryMem) {
      const pct = Math.round((dev.system_stats.mem_used_bytes! / dev.system_stats.mem_total_bytes!) * 100);
      parts.push(`Mem ${pct}%`);
    }
    for (const s of dev.sensors) {
      parts.push(`${s.name}: ${s.value}${s.unit}`);
    }
    telemetryItems.push({
      label: 'System & Sensors',
      content: <Text size="xs" ff="monospace" fw={600}>{parts.join(' · ')}</Text>,
    });
  }
  if (dev.poe_budget != null && dev.poe_budget.total_watts > 0) {
    const pct = Math.min(100, Math.round((dev.poe_budget.used_watts / dev.poe_budget.total_watts) * 100));
    telemetryItems.push({
      label: 'PoE Budget',
      content: (
        <Stack gap={4}>
          <Text size="sm" ff="monospace" fw={600}>
            {dev.poe_budget.used_watts.toFixed(1)}W / {dev.poe_budget.total_watts.toFixed(0)}W ({pct}%)
          </Text>
          <Progress size="xs" value={pct} color={pct > 85 ? 'red' : 'teal'} />
        </Stack>
      ),
    });
  } else if (dev.firmware.serial) {
    telemetryItems.push({
      label: 'Serial',
      content: <Text size="sm" ff="monospace" fw={600}>{dev.firmware.serial}</Text>,
    });
  }

  return (
    <Stack gap="md">
      {error && <Alert color="orange" title="Refresh failed">{error}</Alert>}

      <Group justify="space-between" align="flex-end" wrap="wrap" gap="sm">
        <div>
          <Group gap={6} mb={4}>
            <Anchor component={Link} to={netplanePath('/devices')} size="xs">← Devices</Anchor>
            <Text size="xs" c="dimmed">/</Text>
            <Text size="xs" c="dimmed" ff="monospace">{dev.id}</Text>
          </Group>
          <Group gap="xs" wrap="wrap">
            <Title order={2} size="h3">{dev.name}</Title>
            <DriverBadge driver={dev.driver} />
            <Badge size="xs" variant="outline" color="gray">{dev.role}</Badge>
            <Badge size="xs" variant="dot" color={dev.online ? 'green' : 'red'}>
              {dev.online ? 'Online' : 'Offline'}
            </Badge>
          </Group>
          <Text size="xs" c="dimmed" ff="monospace" mt={4}>
            {[
              [dev.vendor, dev.model].filter(Boolean).join(' '),
              dev.mgmt_ip,
              dev.primary_mac,
            ]
              .filter(Boolean)
              .join(' · ')}
          </Text>
        </div>

        {devices.length > 1 && (
          <Select
            size="xs"
            className="np-filter-input"
            aria-label="Switch device"
            value={dev.id}
            onChange={(val) => {
              if (val && val !== dev.id) void navigate(devicePath(val));
            }}
            data={devices.map((d) => ({ value: d.id, label: `${d.name} (${d.driver})` }))}
          />
        )}
      </Group>

      {/* Hardware Telemetry Strip (only rendered when telemetry fields exist) */}
      {telemetryItems.length > 0 && (
        <Card withBorder radius="md" p="md">
          <div className="np-grid-telemetry">
            {telemetryItems.map((item) => (
              <Stat key={item.label} label={item.label} mb={3}>
                {item.content}
              </Stat>
            ))}
          </div>
        </Card>
      )}

      {/* Hardware Port Faceplate */}
      <PortFaceplate
        device={dev}
        links={detail.links}
        lagGroups={detail.lag_groups}
        flow={flow}
        state={state}
        topology={topology}
        devicesById={devicesById}
        vlansById={vlansById}
        vlanColors={vlanColors}
        selectedPort={selectedPortParam}
        onSelectPort={(ifName) => {
          const next = new URLSearchParams(searchParams);
          next.set('port', ifName);
          setSearchParams(next, { replace: true });
        }}
        onOpenDevice={(targetDevId, portName) => void navigate(devicePath(targetDevId, portName))}
        onOpenVlanFlow={(vlanId, fKey) =>
          void navigate(vlanFlowPath({ mode: 'single', vlanId, findingKey: fKey ?? undefined }))
        }
      />

      {/* L3 Segments & Zones */}
      {detail.segments.length > 0 && (
        <Card withBorder radius="md" p={0}>
          <CardHeader>
            <Title order={4} size="h5">L3 Segments & Zones ({detail.segments.length})</Title>
          </CardHeader>
          <Table.ScrollContainer minWidth={0} type="native">
            <Table highlightOnHover className="data-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Segment</Table.Th>
                  <Table.Th>Kind</Table.Th>
                  <Table.Th>Parent</Table.Th>
                  <Table.Th>VLAN</Table.Th>
                  <Table.Th>Zone</Table.Th>
                  <Table.Th>Addresses</Table.Th>
                  <Table.Th>State</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {detail.segments.map((s) => (
                  <Table.Tr key={s.id}>
                    <Table.Td ff="monospace" fw={600} fz="xs">
                      {s.name}
                      {s.is_management && <Badge size="xs" variant="light" color="blue" ml={6}>Mgmt</Badge>}
                    </Table.Td>
                    <Table.Td fz="xs">{s.kind}</Table.Td>
                    <Table.Td ff="monospace" fz="xs">{s.parent_link || '—'}</Table.Td>
                    <Table.Td>
                      {s.vlan_id != null ? (
                        <VlanChip vid={s.vlan_id} vlansById={vlansById} colorMap={vlanColors} />
                      ) : (
                        <Text size="xs" c="dimmed">—</Text>
                      )}
                    </Table.Td>
                    <Table.Td>
                      {s.zone_id ? (
                        <Badge size="xs" variant="outline" color="gray" ff="monospace">
                          {zonesById.get(s.zone_id) || s.zone_id}
                        </Badge>
                      ) : (
                        <Text size="xs" c="dimmed">—</Text>
                      )}
                    </Table.Td>
                    <Table.Td ff="monospace" fz="xs">
                      {s.addresses.map((a) => `${a.address}/${a.prefix_length}`).join(', ') || '—'}
                    </Table.Td>
                    <Table.Td>
                      <Badge size="xs" variant="light" color={s.oper_state === 'up' ? 'green' : 'gray'}>
                        {s.oper_state}
                      </Badge>
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Card>
      )}

      {/* Wireless Radios & SSIDs */}
      {detail.radios.length > 0 && (
        <Card withBorder radius="md" p={0}>
          <CardHeader>
            <Title order={4} size="h5">Wireless Radios & SSIDs</Title>
          </CardHeader>
          <Table.ScrollContainer minWidth={0} type="native">
            <Table highlightOnHover className="data-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Radio</Table.Th>
                  <Table.Th>Band / Channel</Table.Th>
                  <Table.Th>Utilization</Table.Th>
                  <Table.Th>SSID</Table.Th>
                  <Table.Th>VLAN</Table.Th>
                  <Table.Th>Security</Table.Th>
                  <Table.Th>Clients</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {detail.radios.flatMap((r) => {
                  const radioCells = (
                    <>
                      <Table.Td ff="monospace" fz="xs">{r.name}</Table.Td>
                      <Table.Td ff="monospace" fz="xs">
                        {r.band} · ch {r.channel}{r.width_mhz ? ` (${r.width_mhz} MHz)` : ''}
                      </Table.Td>
                      <Table.Td ff="monospace" fz="xs">
                        {r.utilization_pct != null ? `${r.utilization_pct.toFixed(0)}%` : '—'}
                      </Table.Td>
                    </>
                  );
                  if (r.ssids.length === 0) {
                    return (
                      <Table.Tr key={r.name}>
                        {radioCells}
                        <Table.Td colSpan={4}><Text size="xs" c="dimmed">No active SSIDs</Text></Table.Td>
                      </Table.Tr>
                    );
                  }
                  return r.ssids.map((s) => {
                    const stranded = s.vlan_id != null && unreachableSsids.has(`${r.name}|${s.name}|${s.vlan_id}`);
                    return (
                      <Table.Tr key={`${r.name}-${s.name}`}>
                        {radioCells}
                        <Table.Td fw={600} fz="sm">{s.name}</Table.Td>
                        <Table.Td>
                          <Group gap={6} wrap="wrap">
                            <VlanChip vid={s.vlan_id} vlansById={vlansById} colorMap={vlanColors} />
                            {stranded && <Badge size="xs" variant="light" color="yellow">⊘ Unreachable</Badge>}
                          </Group>
                        </Table.Td>
                        <Table.Td ff="monospace" fz="xs">{s.security || '—'}</Table.Td>
                        <Table.Td ff="monospace" fz="xs">{s.client_count}</Table.Td>
                      </Table.Tr>
                    );
                  });
                })}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Card>
      )}

      {/* Gateways, Firewall & VPNs */}
      {(detail.gateways.length > 0 || detail.firewall || detail.vpns.length > 0) && (
        <div className="np-grid-2-md">
          {detail.gateways.length > 0 && (
            <Card withBorder radius="md" p="md">
              <Title order={4} size="h5" mb="sm">Gateways</Title>
              <Stack gap="xs">
                {detail.gateways.map((g) => (
                  <Group key={g.name} justify="space-between" wrap="wrap" gap="xs">
                    <div>
                      <Text size="sm" fw={600}>
                        {g.name} <Text span ff="monospace" size="xs" c="dimmed">{g.gateway_ip}</Text>
                      </Text>
                      {g.monitor_ip && (
                        <Text size="xs" c="dimmed" ff="monospace">Monitor: {g.monitor_ip}</Text>
                      )}
                    </div>
                    <Badge size="xs" variant="light" color={g.online ? 'green' : 'red'} ff="monospace">
                      {g.online
                        ? g.rtt_ms != null
                          ? `${g.rtt_ms.toFixed(1)} ms${g.loss_pct != null ? ` · ${g.loss_pct.toFixed(1)}% loss` : ''}`
                          : 'ONLINE'
                        : 'OFFLINE'}
                    </Badge>
                  </Group>
                ))}
              </Stack>
            </Card>
          )}

          {detail.firewall && (
            <Card withBorder radius="md" p="md">
              <Group justify="space-between" mb="sm">
                <Title order={4} size="h5">Firewall ({detail.firewall.engine})</Title>
                {detail.firewall.saved_version && (
                  <Badge size="xs" variant="light" color="teal" ff="monospace">
                    v{detail.firewall.saved_version}
                  </Badge>
                )}
              </Group>
              <Stack gap="xs">
                {(detail.firewall.states_used != null || detail.firewall.policy_rule_count != null) && (
                  <Text size="xs" ff="monospace">
                    {detail.firewall.states_used != null
                      ? `States: ${detail.firewall.states_used.toLocaleString()}${detail.firewall.states_max != null ? ` / ${detail.firewall.states_max.toLocaleString()}` : ''}`
                      : ''}
                    {detail.firewall.policy_rule_count != null
                      ? `${detail.firewall.states_used != null ? ' · ' : ''}${detail.firewall.policy_rule_count} policy`
                      : ''}
                    {detail.firewall.nat_rule_count != null ? `, ${detail.firewall.nat_rule_count} NAT` : ''}
                  </Text>
                )}
                {detail.firewall.services.length > 0 && (
                  <Group gap={6} wrap="wrap">
                    {detail.firewall.services.map((sv) => (
                      <Badge
                        key={sv.name}
                        size="xs"
                        variant="dot"
                        color={sv.state === 'running' ? 'green' : sv.state === 'degraded' ? 'yellow' : 'red'}
                      >
                        {sv.name}: {sv.state}
                      </Badge>
                    ))}
                  </Group>
                )}
              </Stack>
            </Card>
          )}

          {detail.vpns.length > 0 && (
            <Card withBorder radius="md" p="md">
              <Title order={4} size="h5" mb="sm">VPN Tunnels ({detail.vpns.length})</Title>
              <Stack gap="xs">
                {detail.vpns.map((v) => (
                  <Group key={v.id} justify="space-between" wrap="wrap" gap="xs">
                    <div>
                      <Text size="sm" fw={600}>{v.name} <Text span size="xs" c="dimmed">({v.kind})</Text></Text>
                      <Text size="xs" c="dimmed" ff="monospace">
                        {v.endpoint || v.peer || '—'} · RX {formatBytes(v.rx_bytes)} / TX {formatBytes(v.tx_bytes)}
                      </Text>
                    </div>
                    <Badge size="xs" variant="dot" color={v.up ? 'green' : 'gray'}>
                      {v.up ? 'Up' : 'Down'}
                    </Badge>
                  </Group>
                ))}
              </Stack>
            </Card>
          )}
        </div>
      )}

      {/* Full Ports Table (complement to faceplate) */}
      {detail.links.length > 0 && (
        <Card withBorder radius="md" p={0}>
          <CardHeader>
            <Title order={4} size="h5">All Ports ({detail.links.length})</Title>
          </CardHeader>
          <Table.ScrollContainer minWidth={0} type="native">
            <Table highlightOnHover className="data-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Port</Table.Th>
                  <Table.Th>State</Table.Th>
                  <Table.Th>Speed</Table.Th>
                  <Table.Th>Media</Table.Th>
                  <Table.Th>PVID</Table.Th>
                  <Table.Th>Tagged VLANs</Table.Th>
                  <Table.Th>STP</Table.Th>
                  <Table.Th>RX / TX</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {[...detail.links]
                  .sort((a, b) => a.index - b.index)
                  .map((l) => (
                    <Table.Tr
                      key={l.if_name}
                      style={{ cursor: 'pointer' }}
                      onClick={() => {
                        const next = new URLSearchParams(searchParams);
                        next.set('port', l.if_name);
                        setSearchParams(next, { replace: true });
                      }}
                    >
                      <Table.Td ff="monospace" fw={600} fz="xs">
                        {l.if_name}
                        {l.label && <Text span c="dimmed" fw={400}> ({l.label})</Text>}
                      </Table.Td>
                      <Table.Td>
                        <Badge size="xs" variant="light" color={l.link_up ? 'green' : 'gray'}>
                          {l.oper_state}
                        </Badge>
                      </Table.Td>
                      <Table.Td ff="monospace" fz="xs">{l.link_up ? formatSpeed(l.speed_mbps) : '—'}</Table.Td>
                      <Table.Td ff="monospace" fz="xs">{l.media}</Table.Td>
                      <Table.Td ff="monospace" fz="xs">{l.pvid ?? '—'}</Table.Td>
                      <Table.Td ff="monospace" fz="xs">
                        {l.tagged_vlans.length > 0 ? l.tagged_vlans.join(', ') : '—'}
                      </Table.Td>
                      <Table.Td>
                        {l.stp_state ? (
                          <Badge size="xs" variant="light" color={isStpBlocked(l.stp_state) ? 'orange' : 'teal'}>
                            {l.stp_state}
                          </Badge>
                        ) : (
                          <Text size="xs" c="dimmed">—</Text>
                        )}
                      </Table.Td>
                      <Table.Td ff="monospace" fz="xs">
                        {l.rates ? `${formatBps(l.rates.rx_bps)} / ${formatBps(l.rates.tx_bps)}` : '—'}
                      </Table.Td>
                    </Table.Tr>
                  ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Card>
      )}

      {/* Attached Clients */}
      {detail.clients.length > 0 && (
        <Card withBorder radius="md" p={0}>
          <CardHeader>
            <Title order={4} size="h5">Attached Clients ({detail.clients.length})</Title>
          </CardHeader>
          <Table.ScrollContainer minWidth={0} type="native">
            <Table highlightOnHover className="data-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Hostname</Table.Th>
                  <Table.Th>MAC</Table.Th>
                  <Table.Th>IPs</Table.Th>
                  <Table.Th>Port / SSID</Table.Th>
                  <Table.Th>VLAN</Table.Th>
                  <Table.Th>Status</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {detail.clients.map((c) => {
                  const issue = getClientVlanIssue(c, flow);
                  return (
                    <Table.Tr key={c.mac}>
                      <Table.Td fw={600}>{c.hostname || '—'}</Table.Td>
                      <Table.Td ff="monospace" fz="xs">{c.mac}</Table.Td>
                      <Table.Td ff="monospace" fz="xs">{c.ips.join(', ') || '—'}</Table.Td>
                      <Table.Td ff="monospace" fz="xs">{c.ssid ? `Wi-Fi: ${c.ssid}` : c.attached_link || '—'}</Table.Td>
                      <Table.Td>
                        <VlanChip vid={c.vlan} vlansById={vlansById} colorMap={vlanColors} />
                      </Table.Td>
                      <Table.Td>
                        {issue ? (
                          <Badge
                            component={Link}
                            to={vlanFlowPath({ mode: 'single', vlanId: issue.vlanId })}
                            size="xs"
                            variant="light"
                            color="yellow"
                            style={{ cursor: 'pointer' }}
                          >
                            ⊘ VLAN Unreachable
                          </Badge>
                        ) : (
                          <Badge size="xs" variant="dot" color={c.online ? 'green' : 'gray'}>
                            {c.online ? 'Online' : 'Offline'}
                          </Badge>
                        )}
                      </Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Card>
      )}
    </Stack>
  );
}
