// SPDX-License-Identifier: AGPL-3.0-or-later
import {
  Badge,
  Button,
  Card,
  Group,
  Select,
  Stack,
  Switch,
  Text,
  Title,
} from '@mantine/core';
import { useMemo, useState } from 'react';
import { Link } from 'react-router';
import {
  type Device,
  formatSpeed,
  formatUptime,
  type NetworkState,
  type TopologyGraph,
  type VLANFlow,
  type VLANFlowVLAN,
} from '../api';
import { DriverBadge, VlanChip } from '../components/Badges';
import { type TopologySelection, TopologyView } from '../components/TopologyView';
import { FindingCallout, PortButton } from '../components/ui';
import { devicePath, vlanFlowPath } from '../routing';
import {
  type ColorPair,
  findingKey,
  findingsForDevice,
  findWirelessSignal,
  isEdgeStpBlocked,
  modeShort,
  portLabel,
  vlanLabel,
} from '../vlanUtils';

interface Props {
  graph: TopologyGraph | null;
  state: NetworkState | null;
  flow: VLANFlow | null;
  devicesById: Map<string, Device>;
  vlansById: Map<number, VLANFlowVLAN>;
  vlanColors: Map<number, ColorPair>;
}

export function TopologyScreen({ graph, state, flow, devicesById, vlansById, vlanColors }: Props) {
  const [vlanOverlay, setVlanOverlay] = useState<string>('');
  const [showClients, setShowClients] = useState(false);
  const [showEvidence, setShowEvidence] = useState(true);
  const [selection, setSelection] = useState<TopologySelection | null>(null);

  // A refresh can drop the overlaid VLAN: only trace VLANs the current report still has.
  const overlayVid = vlanOverlay && vlansById.has(Number(vlanOverlay)) ? Number(vlanOverlay) : null;

  const selectedDevice = selection?.type === 'device' ? devicesById.get(selection.id) ?? null : null;
  const selectedEdge = selection?.type === 'edge'
    ? graph?.edges.find((e) => e.id === selection.id) ?? null
    : null;

  const edgeHops = useMemo(() => {
    if (!selectedEdge || !flow) return [];
    return flow.vlans
      .map((v) => ({ vlan: v, hop: v.hops.find((h) => h.edge_id === selectedEdge.id) }))
      .filter((x): x is { vlan: (typeof flow.vlans)[number]; hop: NonNullable<typeof x.hop> } => x.hop != null);
  }, [selectedEdge, flow]);

  const edgeFindings = useMemo(() => {
    if (!selectedEdge || !flow) return [];
    return flow.findings.filter((f) => f.edge_id === selectedEdge.id);
  }, [selectedEdge, flow]);

  return (
    <Stack gap="md">
      <Group justify="space-between" align="flex-end" wrap="wrap" gap="sm">
        <div>
          <Title order={2} size="h3">Topology</Title>
          <Text size="sm" c="dimmed">
            Solved cross-vendor L2/L3/Wi-Fi graph with live VLAN overlay from <span className="np-mono">/vlans/flow</span>.
          </Text>
        </div>
        <Group gap="sm" wrap="wrap">
          <Select
            size="xs"
            w={200}
            aria-label="VLAN overlay"
            placeholder="VLAN Overlay: None"
            clearable
            value={overlayVid != null ? String(overlayVid) : null}
            onChange={(v) => setVlanOverlay(v ?? '')}
            data={(flow?.vlans ?? []).map((v) => ({
              value: String(v.id),
              label: vlanLabel(vlansById, v.id, true),
            }))}
          />
          <Switch
            size="xs"
            label="Show clients"
            checked={showClients}
            onChange={(e) => setShowClients(e.currentTarget.checked)}
          />
          <Switch
            size="xs"
            label="Show evidence"
            checked={showEvidence}
            onChange={(e) => setShowEvidence(e.currentTarget.checked)}
          />
          {overlayVid != null && (
            <Button
              size="xs"
              variant="light"
              component={Link}
              to={vlanFlowPath({ mode: 'single', vlanId: overlayVid })}
            >
              Trace VLAN {overlayVid} →
            </Button>
          )}
        </Group>
      </Group>

      <div className="np-grid-canvas-side">
        <Card withBorder radius="md" p={0}>
          <div className="np-svg-stage">
            <TopologyView
              graph={graph}
              state={state}
              flow={flow}
              devicesById={devicesById}
              vlanOverlay={overlayVid}
              showClients={showClients}
              showEvidence={showEvidence}
              selection={selection}
              onSelect={setSelection}
            />
          </div>
          <div className="np-legend-bar">
            <span className="np-legend-item">
              <svg width="24" height="8" aria-hidden="true"><line x1="0" y1="4" x2="24" y2="4" stroke="var(--np-primary)" strokeWidth="2.5" /></svg>
              Ethernet
            </span>
            <span className="np-legend-item">
              <svg width="24" height="10" aria-hidden="true">
                <line x1="0" y1="3" x2="24" y2="3" stroke="#1971c2" strokeWidth="2" />
                <line x1="0" y1="7" x2="24" y2="7" stroke="#1971c2" strokeWidth="2" />
              </svg>
              LAG Trunk
            </span>
            <span className="np-legend-item">
              <svg width="24" height="8" aria-hidden="true"><line x1="0" y1="4" x2="24" y2="4" stroke="#7048e8" strokeWidth="2" strokeDasharray="5,3" /></svg>
              Wireless Uplink
            </span>
            <span className="np-legend-item">
              <svg width="24" height="8" aria-hidden="true"><line x1="0" y1="4" x2="24" y2="4" stroke="var(--np-gray-muted)" strokeWidth="1.8" strokeDasharray="3,3" /></svg>
              STP Blocking
            </span>
            {overlayVid != null && (
              <>
                <span className="np-legend-item"><Badge size="xs" color="green" variant="light">✓ Carried</Badge></span>
                <span className="np-legend-item"><Badge size="xs" color="yellow" variant="light">⊘ Dead-end / Mismatch</Badge></span>
                <span className="np-legend-item"><Badge size="xs" color="red" variant="light">⚠ Leak</Badge></span>
                <span className="np-legend-item"><Badge size="xs" color="gray" variant="outline">? Unverified</Badge></span>
              </>
            )}
          </div>
        </Card>

        {/* Side Inspector */}
        <Card withBorder radius="md" p="md">
          {!selectedDevice && !selectedEdge && (
            <Stack gap="xs">
              <Title order={4} size="h5">Topology Inspector</Title>
              <Text size="sm" c="dimmed">
                Click any device or link on the topology map to inspect hardware details, discovery evidence, and carried VLANs.
              </Text>
              <Group gap="xs" mt="xs">
                <Badge variant="light" color="teal">
                  {graph?.nodes.filter((n) => n.kind === 'device').length ?? 0} devices
                </Badge>
                <Badge variant="light" color="gray">
                  {graph?.edges.length ?? 0} links
                </Badge>
              </Group>
            </Stack>
          )}

          {selectedDevice && (
            <Stack gap="sm">
              <Group justify="space-between" wrap="wrap" gap="xs">
                <Title order={4} size="h5">{selectedDevice.name}</Title>
                <Badge size="xs" color={selectedDevice.online ? 'green' : 'red'}>
                  {selectedDevice.online ? 'Online' : 'Offline'}
                </Badge>
              </Group>
              <Group gap={6} wrap="wrap">
                <DriverBadge driver={selectedDevice.driver} />
                <Badge size="xs" variant="outline" color="gray">{selectedDevice.role}</Badge>
              </Group>
              <Stack gap={4}>
                {selectedDevice.model && (
                  <Text size="xs">
                    <Text span c="dimmed">Model: </Text>
                    {selectedDevice.vendor ? `${selectedDevice.vendor} ` : ''}{selectedDevice.model}
                  </Text>
                )}
                {selectedDevice.mgmt_ip && (
                  <Text size="xs" ff="monospace">
                    <Text span c="dimmed" ff="sans-serif">Mgmt IP: </Text>{selectedDevice.mgmt_ip}
                  </Text>
                )}
                {selectedDevice.primary_mac && (
                  <Text size="xs" ff="monospace">
                    <Text span c="dimmed" ff="sans-serif">MAC: </Text>{selectedDevice.primary_mac}
                  </Text>
                )}
                <Text size="xs" ff="monospace">
                  <Text span c="dimmed" ff="sans-serif">Uptime: </Text>{formatUptime(selectedDevice.uptime_sec)}
                </Text>
                {state && (
                  <Text size="xs" ff="monospace">
                    <Text span c="dimmed" ff="sans-serif">Ports: </Text>
                    {state.links.filter((l) => l.device_id === selectedDevice.id && l.link_up).length} /{' '}
                    {state.links.filter((l) => l.device_id === selectedDevice.id).length} up
                  </Text>
                )}
              </Stack>

              {flow && findingsForDevice(flow.findings, selectedDevice.id).length > 0 && (
                <Stack gap={6}>
                  <Text size="xs" fw={700} tt="uppercase" c="dimmed">VLAN Findings</Text>
                  {findingsForDevice(flow.findings, selectedDevice.id).map((f) => (
                    <FindingCallout key={findingKey(f)} finding={f} actionLabel="Trace →" />
                  ))}
                </Stack>
              )}

              <Button size="xs" component={Link} to={devicePath(selectedDevice.id)}>
                Open Device & Faceplate →
              </Button>
            </Stack>
          )}

          {selectedEdge && (
            <Stack gap="sm">
              <Group justify="space-between" wrap="wrap" gap="xs">
                <Title order={4} size="h5">Link Inspector</Title>
                <Badge size="xs" variant="light" color="blue">{selectedEdge.kind}</Badge>
              </Group>

              <Text size="sm" fw={600} ff="monospace">
                {portLabel(devicesById, selectedEdge.from_id, selectedEdge.from_link)} ↔{' '}
                {portLabel(devicesById, selectedEdge.to_id, selectedEdge.to_link)}
              </Text>

              <Stack gap={4}>
                {selectedEdge.speed_mbps != null && selectedEdge.speed_mbps > 0 && (
                  <Text size="xs" ff="monospace">
                    <Text span c="dimmed" ff="sans-serif">Speed: </Text>{formatSpeed(selectedEdge.speed_mbps)}
                  </Text>
                )}
                <Text size="xs" ff="monospace">
                  <Text span c="dimmed" ff="sans-serif">Evidence: </Text>
                  {selectedEdge.evidence.toUpperCase()} ({Math.round(selectedEdge.confidence * 100)}%)
                </Text>
                {selectedEdge.member_links.length > 0 && (
                  <Text size="xs" ff="monospace">
                    <Text span c="dimmed" ff="sans-serif">LAG Members: </Text>
                    {selectedEdge.member_links.join(', ')}
                  </Text>
                )}
                {state && isEdgeStpBlocked(state, selectedEdge) && (
                  <Badge size="xs" color="yellow" variant="light" w="fit-content">STP Blocking</Badge>
                )}
                {selectedEdge.kind === 'wireless_uplink' && state && (() => {
                  const sig = findWirelessSignal(state, selectedEdge.from_id, selectedEdge.to_id);
                  return sig ? (
                    <Text size="xs" ff="monospace">
                      <Text span c="dimmed" ff="sans-serif">Wireless: </Text>
                      {sig.band ? `${sig.band} · ` : ''}{sig.signal_dbm} dBm
                    </Text>
                  ) : null;
                })()}
              </Stack>

              {edgeFindings.length > 0 && (
                <Stack gap={6}>
                  <Text size="xs" fw={700} tt="uppercase" c="dimmed">Findings on this Link</Text>
                  {edgeFindings.map((f) => (
                    <FindingCallout key={findingKey(f)} finding={f} actionLabel="VLAN Flow →" />
                  ))}
                </Stack>
              )}

              {edgeHops.length > 0 && (
                <Stack gap={6}>
                  <Text size="xs" fw={700} tt="uppercase" c="dimmed">VLANs on this Link</Text>
                  {edgeHops.map(({ vlan, hop }) => (
                    <Group key={vlan.id} justify="space-between" gap="xs" wrap="nowrap">
                      <VlanChip vid={vlan.id} vlansById={vlansById} colorMap={vlanColors} />
                      <Badge
                        size="xs"
                        ff="monospace"
                        variant={hop.state === 'unverified' ? 'outline' : 'light'}
                        color={
                          hop.state === 'carried'
                            ? 'green'
                            : hop.state === 'leak'
                              ? 'red'
                              : hop.state === 'unverified'
                                ? 'gray'
                                : 'yellow'
                        }
                      >
                        {modeShort(hop.from_mode)}↔{modeShort(hop.to_mode)} · {hop.state}
                      </Badge>
                    </Group>
                  ))}
                </Stack>
              )}

              <Group gap="xs" wrap="wrap">
                <PortButton devicesById={devicesById} deviceId={selectedEdge.from_id} link={selectedEdge.from_link} />
                <PortButton devicesById={devicesById} deviceId={selectedEdge.to_id} link={selectedEdge.to_link} />
              </Group>
            </Stack>
          )}
        </Card>
      </div>
    </Stack>
  );
}
