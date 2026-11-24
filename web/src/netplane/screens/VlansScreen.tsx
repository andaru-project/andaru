// SPDX-License-Identifier: AGPL-3.0-or-later
import {
  Anchor,
  Badge,
  Button,
  Card,
  Group,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  Title,
  useComputedColorScheme,
} from '@mantine/core';
import { useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import {
  type Device,
  type NetworkState,
  type VLANFlow,
} from '../api';
import { VlanHealthBadge } from '../components/Badges';
import { CardHeader } from '../components/ui';
import { devicePath, vlanFlowPath } from '../routing';
import {
  type ColorPair,
  deviceName,
  findingKey,
  findingsForPort,
  getVlanColor,
} from '../vlanUtils';

interface Props {
  flow: VLANFlow | null;
  state: NetworkState | null;
  devicesById: Map<string, Device>;
  vlanColors: Map<number, ColorPair>;
}

export function VlansScreen({ flow, state, devicesById, vlanColors }: Props) {
  const isDark = useComputedColorScheme('light') === 'dark';
  const navigate = useNavigate();
  const [deviceFilter, setDeviceFilter] = useState<string>('all');
  const [anomaliesOnly, setAnomaliesOnly] = useState(false);

  const vlans = useMemo(() => flow?.vlans ?? [], [flow?.vlans]);
  const findings = useMemo(() => flow?.findings ?? [], [flow?.findings]);
  const links = useMemo(() => state?.links ?? [], [state?.links]);
  const devices = state?.devices ?? [];
  // Segments the API lists as a VLAN's gateway appear with that VLAN; every other one is listed here.
  const vlanGateways = new Set(vlans.flatMap((v) => v.gateways.map((g) => `${g.device_id}\u0000${g.segment_id}`)));
  const nonVlanSegments = (state?.segments ?? []).filter(
    (s) => !vlanGateways.has(`${s.device_id}\u0000${s.id}`),
  );

  // Build lookup of unreachable access ports reported by /api/v1/vlans/flow.
  const unreachableAccess = useMemo(() => {
    const map = new Map<string, number>();
    for (const v of vlans) {
      if (v.gateways.length === 0) continue;
      for (const ap of v.access_ports) {
        if (!ap.reachable) {
          map.set(`${ap.device_id}|${ap.link}|${v.id}`, v.id);
        }
      }
    }
    return map;
  }, [vlans]);

  const matrixPorts = useMemo(() => {
    return links
      .filter((l) => {
        if (deviceFilter !== 'all' && l.device_id !== deviceFilter) return false;
        const portFindings = findingsForPort(findings, l.device_id, l.if_name, l.lag_group_id);
        const hasStrandedAccess = l.pvid != null && unreachableAccess.has(`${l.device_id}|${l.if_name}|${l.pvid}`);
        if (anomaliesOnly) {
          return portFindings.length > 0 || hasStrandedAccess;
        }
        return l.pvid != null || l.tagged_vlans.length > 0 || portFindings.length > 0;
      })
      .sort((a, b) => {
        const devCmp = deviceName(devicesById, a.device_id).localeCompare(deviceName(devicesById, b.device_id));
        if (devCmp !== 0) return devCmp;
        return a.index - b.index;
      });
  }, [links, deviceFilter, anomaliesOnly, findings, unreachableAccess, devicesById]);

  return (
    <Stack gap="md">
      <Group justify="space-between" align="flex-end" wrap="wrap" gap="sm">
        <div>
          <Title order={2} size="h3">VLANs & Segments</Title>
          <Text size="sm" c="dimmed">
            Unified L3 firewall segments, security zones, SSID mappings, and cross-switch 802.1Q tagging matrix.
          </Text>
        </div>
        <Button size="xs" component={Link} to={vlanFlowPath()}>
          Open VLAN Flow Diagram →
        </Button>
      </Group>

      {/* Configured VLANs Table */}
      <Card withBorder radius="md" p={0}>
        <CardHeader>
          <Title order={4} size="h5">Configured VLANs ({vlans.length})</Title>
          <Text size="xs" c="dimmed">Click any row to trace its propagation in VLAN Flow</Text>
        </CardHeader>
        <Table.ScrollContainer minWidth={0} type="native">
          <Table highlightOnHover className="data-table">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>VLAN</Table.Th>
                <Table.Th>Zone</Table.Th>
                <Table.Th>Gateway & Subnets</Table.Th>
                <Table.Th>Devices</Table.Th>
                <Table.Th>Ports (U / T)</Table.Th>
                <Table.Th>SSIDs</Table.Th>
                <Table.Th>Clients</Table.Th>
                <Table.Th>Health</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {vlans.map((v) => {
                const uCount = links.filter((l) => l.pvid === v.id).length;
                const tCount = links.filter((l) => l.tagged_vlans.includes(v.id)).length;
                const zones = Array.from(
                  new Set(v.gateways.map((g) => g.zone_name || g.zone_id).filter((x): x is string => Boolean(x))),
                );
                const addrs = v.gateways.flatMap((g) =>
                  g.addresses.map((a) => `${a.address}/${a.prefix_length}`),
                );
                const ssidNames = Array.from(new Set(v.ssids.map((s) => s.name)));
                const isMgmt = v.gateways.some((g) => g.is_management);

                return (
                  <Table.Tr
                    key={v.id}
                    style={{ cursor: 'pointer' }}
                    onClick={() => void navigate(vlanFlowPath({ mode: 'single', vlanId: v.id }))}
                  >
                    <Table.Td>
                      <Group gap={8} wrap="nowrap">
                        <span
                          className="np-vlan-swatch"
                          style={{ width: 11, height: 11, background: getVlanColor(v.id, vlanColors, isDark) }}
                        />
                        <div>
                          <Group gap={6} wrap="nowrap">
                            <Text size="sm" fw={700} ff="monospace">{v.id}</Text>
                            {v.name && <Text size="sm" fw={600}>{v.name}</Text>}
                            {isMgmt && <Badge size="xs" variant="light" color="blue">Mgmt</Badge>}
                          </Group>
                          {v.gateways.length > 0 && (
                            <Text size="xs" c="dimmed" ff="monospace">
                              {v.gateways.map((g) => g.segment_name).join(', ')}
                            </Text>
                          )}
                        </div>
                      </Group>
                    </Table.Td>
                    <Table.Td>
                      {zones.length > 0 ? (
                        <Group gap={4} wrap="wrap">
                          {zones.map((z) => (
                            <Badge key={z} size="xs" variant="outline" color="gray" ff="monospace">{z}</Badge>
                          ))}
                        </Group>
                      ) : (
                        <Text size="xs" c="dimmed">{v.gateways.length === 0 ? 'L2 only' : '—'}</Text>
                      )}
                    </Table.Td>
                    <Table.Td ff="monospace" fz="xs">
                      {addrs.length > 0 ? (
                        <Stack gap={2}>
                          {addrs.slice(0, 3).map((a) => <span key={a}>{a}</span>)}
                        </Stack>
                      ) : (
                        <Text size="xs" c="dimmed">—</Text>
                      )}
                    </Table.Td>
                    <Table.Td ff="monospace" fz="xs">
                      {v.gateways.length > 0 ? `${v.reached.length}/${v.devices.length}` : v.devices.length}
                    </Table.Td>
                    <Table.Td ff="monospace" fz="xs">{uCount} U · {tCount} T</Table.Td>
                    <Table.Td>
                      {ssidNames.length > 0 ? (
                        <Group gap={4} wrap="wrap">
                          {ssidNames.map((s) => (
                            <Badge key={s} size="xs" variant="outline" color="gray">{s}</Badge>
                          ))}
                        </Group>
                      ) : (
                        <Text size="xs" c="dimmed">—</Text>
                      )}
                    </Table.Td>
                    <Table.Td ff="monospace" fz="xs">{v.clients}</Table.Td>
                    <Table.Td><VlanHealthBadge vlan={v} findings={findings} /></Table.Td>
                    <Table.Td>
                      <Button
                        size="compact-xs"
                        variant="default"
                        component={Link}
                        to={vlanFlowPath({ mode: 'single', vlanId: v.id })}
                        onClick={(e) => e.stopPropagation()}
                      >
                        Trace Flow →
                      </Button>
                    </Table.Td>
                  </Table.Tr>
                );
              })}
              {vlans.length === 0 && (
                <Table.Tr>
                  <Table.Td colSpan={9}>
                    <Text size="sm" c="dimmed">No VLANs configured or discovered yet.</Text>
                  </Table.Td>
                </Table.Tr>
              )}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Card>

      {/* Cross-Device Port Tagging Matrix */}
      <Card withBorder radius="md" p={0}>
        <CardHeader gap="sm">
          <div>
            <Title order={4} size="h5">Cross-Device Port Tagging Matrix</Title>
            <Text size="xs" c="dimmed">
              Untagged (<span className="np-mono">U</span>) vs. Tagged (<span className="np-mono">T</span>) per port — flagged cells jump to VLAN Flow
            </Text>
          </div>
          <Group gap="sm" wrap="wrap">
            <Select
              size="xs"
              w={190}
              aria-label="Filter matrix by device"
              value={deviceFilter}
              onChange={(v) => setDeviceFilter(v ?? 'all')}
              data={[
                { value: 'all', label: 'All Devices' },
                ...devices.map((d) => ({ value: d.id, label: d.name })),
              ]}
            />
            <Switch
              size="xs"
              label="Anomalies only"
              checked={anomaliesOnly}
              onChange={(e) => setAnomaliesOnly(e.currentTarget.checked)}
            />
          </Group>
        </CardHeader>

        <Table.ScrollContainer minWidth={0} type="native">
          <Table highlightOnHover className="data-table">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Device & Port</Table.Th>
                <Table.Th>Label / State</Table.Th>
                {vlans.map((v) => (
                  <Table.Th key={v.id} ta="center">
                    <Group gap={4} justify="center" wrap="nowrap">
                      <span
                        className="np-vlan-swatch"
                        style={{ background: getVlanColor(v.id, vlanColors, isDark) }}
                      />
                      <span className="np-mono">{v.id}</span>
                    </Group>
                    {v.name && (
                      <Text size="10px" c="dimmed" fw={500} tt="none" lineClamp={1}>
                        {v.name}
                      </Text>
                    )}
                  </Table.Th>
                ))}
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {matrixPorts.map((l) => {
                const devName = deviceName(devicesById, l.device_id);
                const portAnoms = findingsForPort(findings, l.device_id, l.if_name, l.lag_group_id);

                return (
                  <Table.Tr key={`${l.device_id}|${l.if_name}`}>
                    <Table.Td style={{ whiteSpace: 'nowrap' }}>
                      <Anchor
                        component={Link}
                        to={devicePath(l.device_id, l.if_name)}
                        fw={600}
                        size="sm"
                      >
                        {devName}
                      </Anchor>
                      <Text span ff="monospace" size="sm">:{l.if_name}</Text>
                      {l.lag_group_id && (
                        <Badge size="xs" variant="light" color="blue" ff="monospace" ml={6}>
                          {l.lag_group_id}
                        </Badge>
                      )}
                    </Table.Td>
                    <Table.Td>
                      <Text size="xs" c="dimmed">{l.label || l.oper_state}</Text>
                    </Table.Td>
                    {vlans.map((v) => {
                      const isU = l.pvid === v.id;
                      const isT = l.tagged_vlans.includes(v.id);
                      const leakHere = portAnoms.find((f) => f.kind === 'leak' && f.vlans.includes(v.id));
                      const deadHere = portAnoms.find((f) => f.kind === 'dead_end' && f.vlans.includes(v.id));
                      const misHere = portAnoms.find((f) => f.kind === 'tag_mismatch' && f.vlans.includes(v.id));
                      const isStrandedAccess = isU && unreachableAccess.has(`${l.device_id}|${l.if_name}|${v.id}`);

                      let cell: { cls: string; label: string; title?: string; to?: string } | null = null;
                      if (leakHere) {
                        cell = {
                          cls: 'anom-leak',
                          label: `⚠ ${isU ? 'U' : isT ? 'T' : '?'}`,
                          title: leakHere.message,
                          to: vlanFlowPath({ mode: 'single', vlanId: v.id, findingKey: findingKey(leakHere) }),
                        };
                      } else if (deadHere) {
                        cell = {
                          cls: 'anom-dead',
                          label: isT ? 'T ⊘' : isU ? 'U ⊘' : '✕ ⊘',
                          title: deadHere.message,
                          to: vlanFlowPath({ mode: 'single', vlanId: v.id, findingKey: findingKey(deadHere) }),
                        };
                      } else if (misHere) {
                        cell = {
                          cls: 'anom-dead',
                          label: `⇄ ${isT ? 'T' : 'U'}`,
                          title: misHere.message,
                          to: vlanFlowPath({ mode: 'single', vlanId: v.id, findingKey: findingKey(misHere) }),
                        };
                      } else if (isStrandedAccess) {
                        cell = {
                          cls: 'anom-dead',
                          label: 'U ⊘',
                          title: `Access port cannot reach VLAN ${v.id} gateway`,
                          to: vlanFlowPath({ mode: 'single', vlanId: v.id }),
                        };
                      } else if (isT) {
                        cell = { cls: 't', label: 'T' };
                      } else if (isU) {
                        cell = { cls: 'u', label: 'U' };
                      }

                      return (
                        <Table.Td key={v.id} ta="center">
                          {!cell ? (
                            <Text span c="dimmed" size="xs">·</Text>
                          ) : cell.to ? (
                            <button
                              type="button"
                              className={`np-tag-chip ${cell.cls}`}
                              title={cell.title}
                              onClick={() => void navigate(cell.to!)}
                            >
                              {cell.label}
                            </button>
                          ) : (
                            <span className={`np-tag-chip ${cell.cls}`}>{cell.label}</span>
                          )}
                        </Table.Td>
                      );
                    })}
                  </Table.Tr>
                );
              })}
              {matrixPorts.length === 0 && (
                <Table.Tr>
                  <Table.Td colSpan={Math.max(2, vlans.length + 2)}>
                    <Text size="sm" c="dimmed">
                      {anomaliesOnly ? 'No ports with VLAN anomalies match this filter.' : 'No ports with VLAN memberships reported.'}
                    </Text>
                  </Table.Td>
                </Table.Tr>
              )}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Card>

      {/* Non-VLAN L3 Segments (e.g. WAN, PPPoE, WireGuard, untagged routed links) */}
      {nonVlanSegments.length > 0 && (
        <Card withBorder radius="md" p={0}>
          <CardHeader>
            <Title order={4} size="h5">Other Segments ({nonVlanSegments.length})</Title>
          </CardHeader>
          <Table.ScrollContainer minWidth={0} type="native">
            <Table highlightOnHover className="data-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Device</Table.Th>
                  <Table.Th>Segment</Table.Th>
                  <Table.Th>VLAN</Table.Th>
                  <Table.Th>Kind</Table.Th>
                  <Table.Th>Parent Link</Table.Th>
                  <Table.Th>Addresses</Table.Th>
                  <Table.Th>State</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {nonVlanSegments.map((s) => (
                  <Table.Tr key={`${s.device_id}:${s.id}`}>
                    <Table.Td>
                      <Anchor component={Link} to={devicePath(s.device_id)} size="sm" fw={600}>
                        {deviceName(devicesById, s.device_id)}
                      </Anchor>
                    </Table.Td>
                    <Table.Td ff="monospace" fw={600} fz="xs">{s.name}</Table.Td>
                    <Table.Td ff="monospace" fz="xs">{s.vlan_id ?? '—'}</Table.Td>
                    <Table.Td><Badge size="xs" variant="light" color="gray">{s.kind}</Badge></Table.Td>
                    <Table.Td ff="monospace" fz="xs">{s.parent_link || '—'}</Table.Td>
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
    </Stack>
  );
}
