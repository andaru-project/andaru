// SPDX-License-Identifier: AGPL-3.0-or-later
import {
  Badge,
  Button,
  Card,
  Group,
  SegmentedControl,
  SimpleGrid,
  Stack,
  Text,
  Title,
  useComputedColorScheme,
} from '@mantine/core';
import { useMemo } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import {
  type Device,
  type NetworkState,
  type TopologyGraph,
  type VLANFlow,
  type VLANFlowFinding,
  type VLANFlowVLAN,
} from '../api';
import { FindingBadge, VlanHealthBadge } from '../components/Badges';
import { AlertRow, CardHeader, FindingCounts, PortButton, PortSide, Stat } from '../components/ui';
import { VlanFlowCanvas } from '../components/VlanFlowCanvas';
import { devicePath, vlanFlowPath } from '../routing';
import {
  type ColorPair,
  describeFinding,
  findHopForFinding,
  findingKey,
  getVlanColor,
  portLabel,
  vlanLabel,
} from '../vlanUtils';

interface Props {
  flow: VLANFlow | null;
  state: NetworkState | null;
  topology: TopologyGraph | null;
  devicesById: Map<string, Device>;
  vlansById: Map<number, VLANFlowVLAN>;
  vlanColors: Map<number, ColorPair>;
}

export function VlanFlowScreen({ flow, state, topology, devicesById, vlansById, vlanColors }: Props) {
  const isDark = useComputedColorScheme('light') === 'dark';
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();

  const vlans = flow?.vlans ?? [];
  const findings = useMemo(() => flow?.findings ?? [], [flow?.findings]);

  const mode: 'single' | 'all' = searchParams.get('mode') === 'all' ? 'all' : 'single';
  const rawVlan = searchParams.get('vlan');
  const parsedVlan = rawVlan ? Number(rawVlan) : null;
  const activeVlanId =
    parsedVlan != null && vlans.some((v) => v.id === parsedVlan)
      ? parsedVlan
      : vlans[0]?.id ?? null;

  const selectedFindingParam = searchParams.get('finding');

  const activeFinding: VLANFlowFinding | null = useMemo(() => {
    if (selectedFindingParam) {
      const exact = findings.find((f) => findingKey(f) === selectedFindingParam);
      // In single mode the inspector stays on the diagram's VLAN.
      if (exact && (mode === 'all' || (activeVlanId != null && exact.vlans.includes(activeVlanId)))) return exact;
    }
    if (mode === 'single' && activeVlanId != null) {
      return findings.find((f) => f.vlans.includes(activeVlanId)) ?? null;
    }
    return findings[0] ?? null;
  }, [findings, selectedFindingParam, mode, activeVlanId]);

  const activeVlan = vlans.find((v) => v.id === activeVlanId) ?? null;

  const updateParams = (next: { mode?: 'single' | 'all'; vlanId?: number | null; finding?: string | null }) => {
    const params = new URLSearchParams();
    const m = next.mode ?? mode;
    if (m === 'all') params.set('mode', 'all');
    const vid = next.vlanId !== undefined ? next.vlanId : activeVlanId;
    if (m === 'single' && vid != null) params.set('vlan', String(vid));
    if (next.finding) params.set('finding', next.finding);
    setSearchParams(params, { replace: true });
  };

  const handleSelectVlan = (vid: number) => {
    const firstForVlan = findings.find((f) => f.vlans.includes(vid));
    updateParams({ mode: 'single', vlanId: vid, finding: firstForVlan ? findingKey(firstForVlan) : null });
  };

  const handleSelectFinding = (f: VLANFlowFinding) => {
    const keepCurrentVlan = mode === 'single' && activeVlanId != null && f.vlans.includes(activeVlanId);
    updateParams({
      mode: 'single',
      vlanId: keepCurrentVlan ? activeVlanId : f.vlans[0],
      finding: findingKey(f),
    });
  };

  const activeFindingDesc = activeFinding && flow
    ? describeFinding(activeFinding, flow, state, devicesById)
    : null;
  const activeFindingHop = activeFinding && flow ? findHopForFinding(flow, activeFinding) : null;

  const hopMatchesOrder = activeFinding && activeFindingHop
    ? activeFindingHop.from_id === activeFinding.device_id &&
      (activeFindingHop.from_id !== activeFindingHop.to_id || activeFindingHop.from_link === activeFinding.link)
    : true;

  return (
    <Stack gap="md">
      <Group justify="space-between" align="flex-end" wrap="wrap" gap="sm">
        <div>
          <Title order={2} size="h3">VLAN Flow</Title>
          <Text size="sm" c="dimmed">
            Port-by-port VLAN propagation across trunks, LAGs, and wireless uplinks—highlighting dead-ends, mismatches, and cross-VLAN leaks.
          </Text>
        </div>
        <SegmentedControl
          size="xs"
          aria-label="VLAN Flow mode"
          value={mode}
          onChange={(v) => updateParams({ mode: v === 'all' ? 'all' : 'single', finding: selectedFindingParam })}
          data={[
            { label: 'Single VLAN', value: 'single' },
            { label: 'All VLANs', value: 'all' },
          ]}
        />
      </Group>

      {/* VLAN Selector Pill Bar */}
      <Card withBorder radius="md" p="sm">
        <Group justify="space-between" wrap="wrap" gap="xs">
          <Group gap={6} wrap="wrap" role="group" aria-label="Select VLAN">
            {vlans.map((v) => {
              const isActive = mode === 'single' && activeVlanId === v.id;
              return (
                <button
                  key={v.id}
                  type="button"
                  className={`np-vlan-pill${isActive ? ' active' : ''}`}
                  aria-pressed={isActive}
                  onClick={() => handleSelectVlan(v.id)}
                >
                  <span
                    className="np-vlan-swatch"
                    style={{ background: getVlanColor(v.id, vlanColors, isDark) }}
                  />
                  <span className="np-mono">{v.id}</span>
                  {v.name && <span>{v.name}</span>}
                  <VlanHealthBadge vlan={v} findings={findings} compact />
                </button>
              );
            })}
            {vlans.length === 0 && (
              <Text size="xs" c="dimmed">No VLANs reported by /api/v1/vlans/flow yet.</Text>
            )}
          </Group>

          <FindingCounts
            findings={findings}
            empty={vlans.length > 0 ? <Badge size="xs" variant="light" color="green">All VLANs clean</Badge> : undefined}
          />
        </Group>
      </Card>

      {/* Full-width Flow Diagram */}
      <Card withBorder radius="md" p={0}>
        <div className="np-svg-stage">
          {flow ? (
            <VlanFlowCanvas
              mode={mode}
              flow={flow}
              selectedVlanId={activeVlanId}
              selectedFindingKey={activeFinding ? findingKey(activeFinding) : null}
              devicesById={devicesById}
              topology={topology}
              vlanColors={vlanColors}
              onSelectVlan={handleSelectVlan}
              onSelectFinding={handleSelectFinding}
              onOpenDevice={(devId) => void navigate(devicePath(devId))}
            />
          ) : (
            <div style={{ padding: 36, textAlign: 'center' }}>
              <Text size="sm" c="dimmed">Loading VLAN flow…</Text>
            </div>
          )}
        </div>
        <div className="np-legend-bar">
          <span className="np-legend-item"><span className="np-tag-chip t">T</span> Tagged (solid)</span>
          <span className="np-legend-item"><span className="np-tag-chip u">U</span> Untagged / PVID (double-rail)</span>
          <span className="np-legend-item">
            <svg width="24" height="10" aria-hidden="true">
              <line x1="0" y1="3" x2="24" y2="3" stroke="var(--np-primary)" strokeWidth="2" />
              <line x1="0" y1="7" x2="24" y2="7" stroke="var(--np-primary)" strokeWidth="2" />
            </svg>
            LAG Trunk
          </span>
          <span className="np-legend-item">
            <span className="np-tag-chip unverified">?</span> Unverified (wireless / unreported)
          </span>
          <span className="np-legend-item"><Badge size="xs" variant="light" color="yellow">⊘ Dead-End</Badge> Missing on peer</span>
          <span className="np-legend-item"><Badge size="xs" variant="light" color="orange">⇄ Mismatch</Badge> Tagged vs Untagged</span>
          <span className="np-legend-item"><Badge size="xs" variant="light" color="red">⚠ Leak</Badge> PVID mismatch</span>
        </div>
      </Card>

      {/* Consequence Inspector + All Findings */}
      <div className="np-grid-2-asym">
        <Card withBorder radius="md" p={0}>
          <CardHeader>
            <Title order={4} size="h5">Consequence Inspector</Title>
            {activeFinding ? (
              <FindingBadge finding={activeFinding} />
            ) : (
              <Badge size="xs" variant="light" color="green">✓ Clean End-to-End</Badge>
            )}
          </CardHeader>

          <Stack gap="sm" p="md">
            {activeFinding && activeFindingDesc ? (
              <>
                <Text fw={700} size="sm">{activeFindingDesc.title}</Text>
                <Text size="sm">{activeFinding.message}</Text>

                {/* Side-by-side port comparison from API hop ends */}
                <SimpleGrid cols={{ base: 1, xs: 2 }} spacing={10}>
                  <PortSide
                    label={portLabel(devicesById, activeFinding.device_id, activeFinding.link)}
                    vlanId={activeFinding.vlans[0]}
                    mode={activeFindingHop ? (hopMatchesOrder ? activeFindingHop.from_mode : activeFindingHop.to_mode) : null}
                    pvid={activeFindingHop ? (hopMatchesOrder ? activeFindingHop.from_pvid : activeFindingHop.to_pvid) : undefined}
                    tagged={activeFindingHop ? (hopMatchesOrder ? activeFindingHop.from_tagged : activeFindingHop.to_tagged) : []}
                  />
                  <PortSide
                    label={portLabel(devicesById, activeFinding.peer_id, activeFinding.peer_link)}
                    vlanId={activeFinding.vlans[0]}
                    mode={activeFindingHop ? (hopMatchesOrder ? activeFindingHop.to_mode : activeFindingHop.from_mode) : null}
                    pvid={activeFindingHop ? (hopMatchesOrder ? activeFindingHop.to_pvid : activeFindingHop.from_pvid) : undefined}
                    tagged={activeFindingHop ? (hopMatchesOrder ? activeFindingHop.to_tagged : activeFindingHop.from_tagged) : []}
                  />
                </SimpleGrid>

                <Stat label="Consequence" mb={2} className={`np-callout ${activeFinding.severity}`}>
                  <Text size="xs">{activeFindingDesc.consequence}</Text>
                </Stat>

                {activeFindingDesc.affected.length > 0 && (
                  <Stat label="Affected Devices & Endpoints" mb={4}>
                    <ul style={{ margin: 0, paddingLeft: 18 }}>
                      {activeFindingDesc.affected.map((item) => (
                        <li key={item} className="np-mono" style={{ fontSize: 12, marginBottom: 2 }}>
                          {item}
                        </li>
                      ))}
                    </ul>
                  </Stat>
                )}

                <Stat label="Suggested Fix" mb={2} className="np-callout">
                  <Text size="xs">{activeFindingDesc.remediation}</Text>
                </Stat>

                <Group gap="xs" wrap="wrap">
                  <PortButton
                    devicesById={devicesById}
                    deviceId={activeFinding.device_id}
                    link={activeFinding.link}
                    prefix="Inspect "
                  />
                  <PortButton
                    devicesById={devicesById}
                    deviceId={activeFinding.peer_id}
                    link={activeFinding.peer_link}
                    prefix="Inspect "
                  />
                </Group>
              </>
            ) : activeVlan ? (
              <>
                <Text fw={700} size="sm">{vlanLabel(vlansById, activeVlan.id, true)}</Text>
                <Text size="sm" c="dimmed">
                  No dead-ends, tag mismatches, or cross-VLAN leaks detected for VLAN {activeVlan.id}.
                  {activeVlan.hops.some((h) => h.state === 'unverified')
                    ? ' Some hops (such as wireless uplinks) have unverified VLAN membership.'
                    : ''}
                </Text>
                <Group gap="md" wrap="wrap">
                  <Stat label="Gateways">
                    <Text size="xs" ff="monospace">
                      {activeVlan.gateways.length > 0
                        ? activeVlan.gateways
                            .map((g) => `${g.segment_name}${g.zone_name ? ` (${g.zone_name})` : ''}`)
                            .join(', ')
                        : 'L2 only (no L3 gateway)'}
                    </Text>
                  </Stat>
                  <Stat label="Devices Reached">
                    <Text size="xs" ff="monospace">
                      {activeVlan.gateways.length > 0
                        ? `${activeVlan.reached.length} / ${activeVlan.devices.length}`
                        : `${activeVlan.devices.length} configured`}
                    </Text>
                  </Stat>
                  <Stat label="Endpoints">
                    <Text size="xs" ff="monospace">
                      {activeVlan.access_ports.length} access · {activeVlan.ssids.length} SSID · {activeVlan.clients} clients
                    </Text>
                  </Stat>
                </Group>
              </>
            ) : (
              <Text size="sm" c="dimmed">Select a VLAN or finding to inspect its propagation consequences.</Text>
            )}
          </Stack>
        </Card>

        <Card withBorder radius="md" p={0}>
          <CardHeader>
            <Title order={4} size="h5">All Detected Findings ({findings.length})</Title>
          </CardHeader>
          <div>
            {findings.map((f) => {
              const fk = findingKey(f);
              const isSel = activeFinding != null && findingKey(activeFinding) === fk;
              const desc = flow ? describeFinding(f, flow, state, devicesById) : null;
              return (
                <AlertRow
                  key={fk}
                  severity={f.severity}
                  selected={isSel}
                  onClick={() => handleSelectFinding(f)}
                  badge={<FindingBadge finding={f} />}
                  title={<Text size="xs" fw={700}>{desc?.shortLabel ?? f.kind}</Text>}
                  subtitle={`${portLabel(devicesById, f.device_id, f.link)} ↔ ${portLabel(devicesById, f.peer_id, f.peer_link)}`}
                  subtitleMono
                  action={
                    <Button
                      size="compact-xs"
                      variant="subtle"
                      component={Link}
                      to={vlanFlowPath({ mode: 'single', vlanId: f.vlans[0], findingKey: fk })}
                      onClick={(e) => e.stopPropagation()}
                    >
                      Focus
                    </Button>
                  }
                />
              );
            })}
            {findings.length === 0 && (
              <AlertRow>
                <Text size="sm" c="dimmed">No VLAN leaks, dead-ends, or tag mismatches detected.</Text>
              </AlertRow>
            )}
          </div>
        </Card>
      </div>
    </Stack>
  );
}
