// SPDX-License-Identifier: AGPL-3.0-or-later
import { Text, useComputedColorScheme } from '@mantine/core';
import { type ReactNode, useMemo } from 'react';
import {
  type Device,
  type TopologyGraph,
  type VLANFlow,
  type VLANFlowFinding,
  type VLANFlowVLAN,
  type VLANMode,
} from '../api';
import {
  computeAllVlansLayout,
  computeSingleVlanLayout,
  type FlowPortChip,
  type FlowRibbon,
} from '../vlanFlowLayout';
import {
  type ColorPair,
  deviceName,
  findingKey,
  getVlanColor,
  modeShort,
  onActivate,
  portLabel,
  type SvgPalette,
  svgPalette,
  type VlanHealth,
  vlanHealth,
} from '../vlanUtils';

const MONO = 'ui-monospace, monospace';
const SANS = 'system-ui, sans-serif';

function bezierControlPoints(x1: number, y1: number, x2: number, y2: number, sameColumn = false) {
  if (sameColumn) {
    const loopX = Math.max(x1, x2) + 54;
    return { cx1: loopX, cy1: y1, cx2: loopX, cy2: y2 };
  }
  const dx = Math.max(36, Math.abs(x2 - x1) * 0.46);
  return { cx1: x1 + dx, cy1: y1, cx2: x2 - dx, cy2: y2 };
}

function bezierPath(x1: number, y1: number, x2: number, y2: number, sameColumn = false): string {
  const { cx1, cy1, cx2, cy2 } = bezierControlPoints(x1, y1, x2, y2, sameColumn);
  return `M ${x1} ${y1} C ${cx1} ${cy1}, ${cx2} ${cy2}, ${x2} ${y2}`;
}

/** Splits a ribbon's cubic Bezier at t=0.5 (de Casteljau) so both dead-end halves follow the true curve. */
function splitBezier(x1: number, y1: number, x2: number, y2: number, sameColumn = false) {
  const { cx1, cy1, cx2, cy2 } = bezierControlPoints(x1, y1, x2, y2, sameColumn);
  const m01x = (x1 + cx1) / 2;
  const m01y = (y1 + cy1) / 2;
  const m12x = (cx1 + cx2) / 2;
  const m12y = (cy1 + cy2) / 2;
  const m23x = (cx2 + x2) / 2;
  const m23y = (cy2 + y2) / 2;
  const q1x = (m01x + m12x) / 2;
  const q1y = (m01y + m12y) / 2;
  const q2x = (m12x + m23x) / 2;
  const q2y = (m12y + m23y) / 2;
  const mx = (q1x + q2x) / 2;
  const my = (q1y + q2y) / 2;
  return {
    first: `M ${x1} ${y1} C ${m01x} ${m01y}, ${q1x} ${q1y}, ${mx} ${my}`,
    second: `M ${mx} ${my} C ${q2x} ${q2y}, ${m23x} ${m23y}, ${x2} ${y2}`,
    mx,
    my,
  };
}

function truncate(text: string, max: number): string {
  return text.length <= max ? text : `${text.slice(0, max - 1)}…`;
}

function ribbonMidX(r: { x1: number; x2: number; sameColumn: boolean }): number {
  return r.sameColumn ? Math.max(r.x1, r.x2) + 46 : (r.x1 + r.x2) / 2;
}

function SvgButton({
  label,
  onClick,
  transform,
  focusable = true,
  children,
}: {
  label: string;
  onClick?: () => void;
  transform?: string;
  /** false: pointer-only, for a control whose action another keyboard stop already offers. */
  focusable?: boolean;
  children: ReactNode;
}) {
  const kb = onClick != null && focusable;
  return (
    <g
      transform={transform}
      style={onClick ? { cursor: 'pointer' } : undefined}
      role={kb ? 'button' : undefined}
      tabIndex={kb ? 0 : undefined}
      aria-label={kb ? label : undefined}
      aria-hidden={onClick && !focusable ? true : undefined}
      onClick={onClick}
      onKeyDown={kb ? onActivate(onClick) : undefined}
    >
      {children}
    </g>
  );
}

function Pill({
  x, y, width, height, fill, textFill, stroke, strokeWidth, strokeDasharray,
  fontSize = 9.5, fontWeight = 700, label,
}: {
  x: number; y: number; width: number; height: number;
  fill: string; textFill: string; stroke?: string; strokeWidth?: number; strokeDasharray?: string;
  fontSize?: number; fontWeight?: number; label: string;
}) {
  return (
    <>
      <rect
        x={x - width / 2} y={y - height / 2} width={width} height={height} rx={4}
        fill={fill} stroke={stroke} strokeWidth={strokeWidth} strokeDasharray={strokeDasharray}
      />
      <text x={x} y={y + 3.5} textAnchor="middle" fill={textFill} fontSize={fontSize} fontWeight={fontWeight} fontFamily={MONO}>
        {label}
      </text>
    </>
  );
}

function ColumnHeaders({
  columns,
  C,
}: {
  columns: { index: number; label: string; x: number; width: number }[];
  C: SvgPalette;
}) {
  return (
    <>
      {columns.map((col) => (
        <g key={`col-${col.index}`}>
          <text x={col.x} y={22} fill={C.dim} fontSize={10.5} fontWeight={700} letterSpacing="0.06em" fontFamily={SANS}>
            {col.label}
          </text>
          <line x1={col.x} y1={30} x2={col.x + col.width} y2={30} stroke={C.borderSoft} strokeWidth={1} />
        </g>
      ))}
    </>
  );
}

function DeviceCardHead({ width, deviceId, dev, C }: { width: number; deviceId: string; dev?: Device; C: SvgPalette }) {
  return (
    <>
      <rect width={width} height={34} rx={6} fill={C.subtle} />
      <text x={12} y={16} fill={C.text} fontSize={12} fontWeight={700} fontFamily={SANS}>
        {truncate(dev?.name || deviceId, 22)}
      </text>
      <text x={12} y={29} fill={C.dim} fontSize={9.5} fontFamily={MONO}>
        {truncate([dev?.driver, dev?.mgmt_ip].filter(Boolean).join(' · ') || deviceId, 28)}
      </text>
    </>
  );
}

/**
 * One All-VLANs lane. The first lane carrying a finding is the keyboard stop that selects it (a leak
 * spans two lanes); a healthy lane only focuses its VLAN, which the VLAN cards already offer, so it
 * and any repeat of a finding stay pointer-only.
 */
function Lane({
  d, hitWidth, finding, focusable, onClick, children,
}: {
  d: string; hitWidth: number; finding?: VLANFlowFinding; focusable: boolean; onClick: () => void; children: ReactNode;
}) {
  // A transparent hit path as wide as the gap between lanes: easy to click, never over a neighbour.
  const hit = <path d={d} fill="none" stroke="transparent" strokeWidth={hitWidth} />;
  if (!finding || !focusable) {
    return <g style={{ cursor: 'pointer' }} onClick={onClick}>{hit}{children}</g>;
  }
  return (
    <g
      className="np-lane"
      style={{ cursor: 'pointer' }}
      role="button"
      tabIndex={0}
      aria-label={finding.message}
      onClick={onClick}
      onKeyDown={onActivate(onClick)}
    >
      {hit}
      <path className="np-lane-focus" d={d} fill="none" />
      {children}
    </g>
  );
}

function RibbonGroup({
  d, selected, onClick, hitWidth = 18, glowWidth = 8, glowOpacity = 0.4, children,
}: {
  d: string; selected: boolean; onClick?: () => void;
  hitWidth?: number; glowWidth?: number; glowOpacity?: number; children: ReactNode;
}) {
  return (
    <g style={{ cursor: onClick ? 'pointer' : undefined }} onClick={onClick}>
      <path d={d} fill="none" stroke="transparent" strokeWidth={hitWidth} />
      {selected && <path d={d} fill="none" stroke="#ffb21c" strokeWidth={glowWidth} strokeOpacity={glowOpacity} />}
      {children}
    </g>
  );
}

function Rail({
  d, stroke, width, innerWidth, innerDash, canvas,
}: {
  d: string; stroke: string; width: number; innerWidth: number; innerDash?: string; canvas: string;
}) {
  return (
    <>
      <path d={d} fill="none" stroke={stroke} strokeWidth={width} strokeLinecap="round" />
      <path d={d} fill="none" stroke={canvas} strokeWidth={innerWidth} strokeDasharray={innerDash} />
    </>
  );
}

function PortChipSvg({ chip, vColor, C }: { chip: FlowPortChip; vColor: string; C: SvgPalette }) {
  const label = truncate(chip.label, 10);
  const badgeW = 19;
  const textW = Math.max(34, label.length * 6.0 + 6);
  const totalW = textW + badgeW + 6;
  const startX = chip.side === 'left' ? chip.x : chip.x - totalW;
  const bx = chip.side === 'left' ? 3 : textW + 3;
  const tx = chip.side === 'left' ? badgeW + 7 : 5;

  const isLeak = chip.state === 'leak';
  const isDeadEndMissing = chip.state === 'dead_end' && chip.mode === 'none';
  const isMismatch = chip.state === 'mismatch';

  let bFill = C.subtle;
  let bStroke = C.border;
  let bText = C.dim;
  let bDash: string | undefined;
  let bWidth = 1;
  let glyph = modeShort(chip.mode);
  let outerStroke = C.borderSoft;

  if (isLeak) {
    bFill = bStroke = outerStroke = C.red;
    bText = '#ffffff';
    glyph = '⇄';
  } else if (isDeadEndMissing) {
    bFill = C.isDark ? 'rgba(250,176,5,0.2)' : 'rgba(230,119,0,0.14)';
    bStroke = bText = outerStroke = C.amber;
    glyph = '✕';
  } else if (chip.mode === 'unknown') {
    bStroke = bText = outerStroke = C.gray;
    bDash = '2,2';
    glyph = '?';
  } else if (chip.mode === 'tagged') {
    bFill = vColor;
    bStroke = isMismatch ? C.amber : vColor;
    bText = C.isDark ? '#101418' : '#ffffff';
    bWidth = isMismatch ? 1.8 : 1;
    glyph = 'T';
    if (isMismatch) outerStroke = C.amber;
  } else if (chip.mode === 'untagged') {
    bFill = C.surface;
    bStroke = bText = isMismatch ? C.amber : vColor;
    bDash = '3,1.5';
    bWidth = 1.6;
    glyph = 'U';
    if (isMismatch) outerStroke = C.amber;
  } else {
    glyph = '✕';
  }

  return (
    <g transform={`translate(${startX}, ${chip.y - 11})`}>
      <rect width={totalW} height={22} rx={4} fill={C.subtle} stroke={outerStroke} strokeWidth={1} strokeDasharray={chip.mode === 'unknown' ? '3,2' : undefined} />
      <rect x={bx} y={2.5} width={badgeW} height={17} rx={3} fill={bFill} stroke={bStroke} strokeWidth={bWidth} strokeDasharray={bDash} />
      <text x={bx + badgeW / 2} y={14.5} textAnchor="middle" fill={bText} fontSize={10.5} fontWeight={800} fontFamily={MONO}>
        {glyph}
      </text>
      <text x={tx} y={14.5} fill={C.text} fontSize={10} fontWeight={600} fontFamily={MONO}>
        {label}
      </text>
    </g>
  );
}

function modePill(fromMode: VLANMode, toMode: VLANMode, linkKind: string): string {
  const m = fromMode === toMode ? modeShort(fromMode) : `${modeShort(fromMode)}→${modeShort(toMode)}`;
  if (linkKind === 'lag') return `LAG · ${m}`;
  if (linkKind === 'wireless_uplink') return `Wi-Fi · ${m}`;
  return m;
}

function vlanStatusMeta(health: VlanHealth, C: SvgPalette): { text: string; color: string } {
  switch (health) {
    case 'leak': return { text: '⚠ LEAK', color: C.red };
    case 'dead_end': return { text: '⊘ DEAD-END', color: C.amber };
    case 'mismatch': return { text: '⇄ MISMATCH', color: C.amber };
    case 'unverified': return { text: '? UNVERIFIED', color: C.gray };
    case 'ok': return { text: '✓ OK', color: C.green };
  }
}

interface Props {
  mode: 'single' | 'all';
  flow: VLANFlow;
  selectedVlanId: number | null;
  selectedFindingKey: string | null;
  devicesById: Map<string, Device>;
  topology: TopologyGraph | null;
  vlanColors: Map<number, ColorPair>;
  onSelectVlan: (vlanId: number) => void;
  onSelectFinding: (finding: VLANFlowFinding) => void;
  onOpenDevice: (deviceId: string) => void;
}

export function VlanFlowCanvas({
  mode,
  flow,
  selectedVlanId,
  selectedFindingKey,
  devicesById,
  topology,
  vlanColors,
  onSelectVlan,
  onSelectFinding,
  onOpenDevice,
}: Props) {
  const isDark = useComputedColorScheme('light') === 'dark';
  const C = useMemo(() => svgPalette(isDark), [isDark]);

  const activeVlan: VLANFlowVLAN | null = useMemo(() => {
    if (flow.vlans.length === 0) return null;
    return flow.vlans.find((v) => v.id === selectedVlanId) ?? flow.vlans[0];
  }, [flow.vlans, selectedVlanId]);

  const singleLayout = useMemo(
    () => (mode === 'single' && activeVlan ? computeSingleVlanLayout(activeVlan, flow, devicesById, topology) : null),
    [mode, activeVlan, flow, devicesById, topology],
  );

  const allLayout = useMemo(
    () => (mode === 'all' && flow.vlans.length > 0 ? computeAllVlansLayout(flow, devicesById, topology) : null),
    [mode, flow, devicesById, topology],
  );

  if (flow.vlans.length === 0) {
    return (
      <div style={{ padding: 36, textAlign: 'center' }}>
        <Text c="dimmed" size="sm">
          No VLANs discovered yet. Configure switches, routers, or APs with VLAN memberships and poll to trace flow.
        </Text>
      </div>
    );
  }

  if (mode === 'all' && allLayout) {
    return (
      <svg
        style={{ width: '100%', minWidth: Math.min(allLayout.width, 860), height: 'auto' }}
        viewBox={`0 0 ${allLayout.width} ${allLayout.height}`}
        role="group"
        aria-label="All VLANs propagation overview"
      >
        <ColumnHeaders columns={allLayout.columns} C={C} />

        {/* Segment -> gateway ribbons */}
        {allLayout.segmentLinks.map((sl) => (
          <path
            key={`seg-${sl.vlanId}-${sl.deviceId}`}
            d={bezierPath(sl.x1, sl.y1, sl.x2, sl.y2)}
            fill="none"
            stroke={getVlanColor(sl.vlanId, vlanColors, isDark)}
            strokeWidth={2.6}
            style={{ cursor: 'pointer' }}
            onClick={() => onSelectVlan(sl.vlanId)}
          />
        ))}

        {/* Multi-lane edge bundles */}
        {allLayout.edgeBundles.map((bundle) => {
          const laneCount = bundle.lanes.length;
          const targetSpread = Math.min(bundle.maxSpread, Math.max(64, Math.max(laneCount - 1, 0) * 5));
          const laneGap = laneCount <= 1 ? 0 : Math.min(11, targetSpread / (laneCount - 1));
          const hitWidth = laneCount <= 1 ? 11 : laneGap;
          const midX = ribbonMidX(bundle);
          const midY = (bundle.y1 + bundle.y2) / 2;
          const topFinding = bundle.findings[0];
          const stops = new Set<string>(); // findings that already have a lane keyboard stop in this bundle
          // The pill repeats its lane's action: when a lane is already that keyboard stop, the pill is pointer-only.
          const topOnLane = topFinding != null && bundle.lanes.some((l) => l.finding != null && findingKey(l.finding) === findingKey(topFinding));
          const allUnverified = bundle.lanes.every((l) => l.state === 'unverified');

          return (
            <g key={`bundle-${bundle.edgeId}`}>
              {bundle.lanes.map((lane, idx) => {
                const offset = (idx - (bundle.lanes.length - 1) / 2) * laneGap;
                const y1 = bundle.y1 + offset;
                const y2 = bundle.y2 + offset;
                const col = getVlanColor(lane.vlanId, vlanColors, isDark);
                const onLaneClick = () => (lane.finding ? onSelectFinding(lane.finding) : onSelectVlan(lane.vlanId));
                const laneStop = lane.finding != null && !stops.has(findingKey(lane.finding));
                if (lane.finding) stops.add(findingKey(lane.finding));

                if (lane.state === 'dead_end') {
                  // The layout already swapped the modes into display order: style the half that lacks the VLAN.
                  const fromCarries = lane.fromMode !== 'none';
                  const carried = { stroke: col, strokeWidth: 2.4 };
                  const missing = { stroke: C.amber, strokeWidth: 1.8, strokeDasharray: '4,3' };
                  const { first, second, mx, my } = splitBezier(bundle.x1, y1, bundle.x2, y2, bundle.sameColumn);
                  return (
                    <Lane
                      key={`${bundle.edgeId}-v${lane.vlanId}`}
                      d={`${first} ${second}`}
                      finding={lane.finding} hitWidth={hitWidth} focusable={laneStop} onClick={onLaneClick}>
                      <path d={first} fill="none" {...(fromCarries ? carried : missing)} />
                      <path d={second} fill="none" {...(fromCarries ? missing : carried)} />
                      {bundle.sameColumn ? (
                        <line x1={mx - 7} y1={my} x2={mx + 7} y2={my} stroke={C.amber} strokeWidth={2.6} />
                      ) : (
                        <line x1={mx} y1={my - 7} x2={mx} y2={my + 7} stroke={C.amber} strokeWidth={2.6} />
                      )}
                    </Lane>
                  );
                }

                const laneD = bezierPath(bundle.x1, y1, bundle.x2, y2, bundle.sameColumn);
                // Healthy LAG and untagged lanes use the legend's double rail, as in the single-VLAN view.
                if (
                  lane.state === 'carried' &&
                  (bundle.linkKind === 'lag' || (lane.fromMode === 'untagged' && lane.toMode === 'untagged'))
                ) {
                  return (
                    <Lane key={`${bundle.edgeId}-v${lane.vlanId}`} d={laneD} finding={lane.finding} hitWidth={hitWidth} focusable={laneStop} onClick={onLaneClick}>
                      <Rail d={laneD} stroke={col} width={bundle.linkKind === 'lag' ? 4.4 : 3.8} innerWidth={1.4} canvas={C.canvas} />
                    </Lane>
                  );
                }
                const strokeColor = lane.state === 'leak' ? C.red : lane.state === 'unverified' ? C.gray : col;
                const dash =
                  lane.state === 'unverified' ? '3,4' : bundle.linkKind === 'wireless_uplink' ? '5,4' : lane.state === 'mismatch' ? '6,3' : undefined;
                return (
                  <Lane key={`${bundle.edgeId}-v${lane.vlanId}`} d={laneD} finding={lane.finding} hitWidth={hitWidth} focusable={laneStop} onClick={onLaneClick}>
                    <path
                      d={laneD}
                      fill="none" stroke={strokeColor} strokeWidth={lane.state === 'leak' ? 3.2 : 2.3} strokeDasharray={dash}
                    />
                  </Lane>
                );
              })}

              {topFinding ? (
                <SvgButton label={topFinding.message} onClick={() => onSelectFinding(topFinding)} focusable={!topOnLane}>
                  {(() => {
                    const label =
                      topFinding.kind === 'leak' ? `⚠ LEAK ${topFinding.vlans[0]}⇄${topFinding.vlans[1]}`
                        : topFinding.kind === 'tag_mismatch' ? `⇄ V${topFinding.vlans[0]} MISMATCH`
                          : `⊘ V${topFinding.vlans[0]} DROP`;
                    return (
                      <Pill
                        x={midX} y={midY} width={Math.max(82, label.length * 6.0 + 14)} height={20}
                        fill={topFinding.severity === 'crit' ? C.red : C.amber}
                        textFill={topFinding.severity === 'crit' ? '#ffffff' : C.amberInk}
                        stroke={C.surface} strokeWidth={1.2} fontWeight={800} label={label}
                      />
                    );
                  })()}
                </SvgButton>
              ) : allUnverified ? (
                <g>
                  <Pill
                    x={midX} y={midY} width={88} height={19} fill={C.surface} textFill={C.gray}
                    stroke={C.gray} strokeWidth={1.2} strokeDasharray="3,2" fontSize={9} label="? UNVERIFIED"
                  />
                </g>
              ) : null}
            </g>
          );
        })}

        {/* Device -> endpoint links */}
        {allLayout.endpointLinks.map((el) => (
          <path
            key={`ep-l-${el.deviceId}`}
            d={bezierPath(el.x1, el.y1, el.x2, el.y2)}
            fill="none"
            stroke={el.hasLeak ? C.red : el.hasUnreachable ? C.amber : C.border}
            strokeWidth={el.hasLeak ? 2.4 : 1.8}
            strokeDasharray={el.hasUnreachable ? '4,3' : undefined}
          />
        ))}

        {/* Col 0: VLAN cards */}
        {allLayout.vlanCards.map((vc) => {
          const col = getVlanColor(vc.vlan.id, vlanColors, isDark);
          const gw = vc.vlan.gateways[0];
          const addr = !gw ? 'L2 only' : gw.addresses[0] ? `${gw.addresses[0].address}/${gw.addresses[0].prefix_length}` : gw.segment_name || gw.segment_id;
          const zone = gw?.zone_name || gw?.zone_id;
          const status = vlanStatusMeta(vlanHealth(vc.vlan, vc.findings), C);

          return (
            <SvgButton key={vc.id} transform={`translate(${vc.x}, ${vc.y})`} label={`Focus VLAN ${vc.vlan.id}`} onClick={() => onSelectVlan(vc.vlan.id)}>
              <rect width={vc.width} height={vc.height} rx={6} fill={C.surface} stroke={C.border} strokeWidth={1.2} />
              <rect width={5} height={vc.height} rx={2} fill={col} />
              <text x={14} y={23} fill={C.text} fontSize={12.5} fontWeight={700} fontFamily={SANS}>
                {truncate(`VLAN ${vc.vlan.id}${vc.vlan.name ? ` · ${vc.vlan.name}` : ''}`, 24)}
              </text>
              <text x={14} y={41} fill={C.dim} fontSize={10.5} fontFamily={MONO}>{truncate(addr, 26)}</text>
              <text x={14} y={60} fill={status.color} fontSize={10} fontWeight={700} fontFamily={MONO}>
                {truncate(`${status.text}${zone ? ` · ${zone}` : ''}`, 27)}
              </text>
            </SvgButton>
          );
        })}

        {/* Device cards */}
        {allLayout.deviceCards.map((dc) => {
          const dev = devicesById.get(dc.deviceId);
          const dName = dev?.name || dc.deviceId;
          const hasLeak = dc.findings.some((f) => f.kind === 'leak');
          const hasWarn = dc.findings.some((f) => f.kind !== 'leak');
          const stroke = hasLeak ? C.red : hasWarn ? C.amber : C.border;
          return (
            <SvgButton key={dc.id} transform={`translate(${dc.x}, ${dc.y})`} label={`Open device ${dName}`} onClick={() => onOpenDevice(dc.deviceId)}>
              <rect width={dc.width} height={dc.height} rx={6} fill={C.surface} stroke={stroke} strokeWidth={hasLeak || hasWarn ? 1.6 : 1.2} />
              <DeviceCardHead width={dc.width} deviceId={dc.deviceId} dev={dev} C={C} />
              <text x={12} y={54} fill={C.dim} fontSize={10} fontFamily={MONO}>
                {truncate(`VLANs: ${dc.vlanIds.join(', ')}`, 28)}
              </text>
              <text x={12} y={dc.height - 14} fill={hasLeak ? C.red : hasWarn ? C.amber : C.green} fontSize={10} fontWeight={700} fontFamily={MONO}>
                {hasLeak
                  ? `⚠ ${dc.findings.filter((f) => f.kind === 'leak').length} leak finding(s)`
                  : hasWarn
                    ? `⊘ ${dc.findings.length} warning(s)`
                    : `✓ ${dc.vlanIds.length} VLAN${dc.vlanIds.length === 1 ? '' : 's'}`}
              </text>
            </SvgButton>
          );
        })}

        {/* Endpoint summary cards */}
        {allLayout.endpointCards.map((ep) => {
          const dName = deviceName(devicesById, ep.deviceId);
          return (
            <SvgButton key={ep.id} transform={`translate(${ep.x}, ${ep.y})`} label={`Inspect endpoints on ${dName}`} onClick={() => onOpenDevice(ep.deviceId)}>
              <rect
                width={ep.width} height={ep.height} rx={6} fill={C.surface}
                stroke={ep.hasLeak ? C.red : ep.hasUnreachable ? C.amber : C.border}
                strokeWidth={ep.hasLeak || ep.hasUnreachable ? 1.5 : 1.2}
                strokeDasharray={ep.hasUnreachable && !ep.hasLeak ? '5,3' : undefined}
              />
              <text x={12} y={21} fill={C.text} fontSize={11.5} fontWeight={700} fontFamily={SANS}>
                {truncate(`${dName} Endpoints`, 25)}
              </text>
              {ep.entries.slice(0, 5).map((ent, idx) => {
                const unreach = ent.unreachableAccessCount > 0 || ent.ssids.some((s) => !s.reachable);
                const parts = [
                  ent.accessCount > 0 ? `${ent.accessCount}p` : '',
                  ent.ssids.length > 0 ? `${ent.ssids.length} SSID` : '',
                ].filter(Boolean);
                const prefix = ent.hasLeak ? '⚠ ' : unreach ? '⊘ ' : '';
                return (
                  <text
                    key={`${ep.id}-v${ent.vlanId}`} x={12} y={41 + idx * 18}
                    fill={ent.hasLeak ? C.red : unreach ? C.amber : C.dim}
                    fontSize={10} fontWeight={ent.hasLeak || unreach ? 700 : 500} fontFamily={MONO}
                  >
                    {truncate(`${prefix}V${ent.vlanId}: ${parts.join(' · ')}`, 30)}
                  </text>
                );
              })}
              {ep.entries.length > 5 && (
                <text x={12} y={41 + 5 * 18} fill={C.dim} fontSize={9.5} fontFamily={MONO}>
                  +{ep.entries.length - 5} more VLAN(s)
                </text>
              )}
            </SvgButton>
          );
        })}
      </svg>
    );
  }

  if (!singleLayout || !activeVlan) return null;

  const vColor = getVlanColor(activeVlan.id, vlanColors, isDark);

  // One gradient per leak ribbon in user space: an objectBoundingBox gradient paints nothing on a
  // horizontal ribbon, whose bounding box has zero height.
  const leakGradients = singleLayout.ribbons.flatMap((r, idx) =>
    r.state === 'leak' && r.foreignVlanId != null
      ? [{ r, id: `np-grad-leak-${activeVlan.id}-${idx}`, foreignColor: getVlanColor(r.foreignVlanId, vlanColors, isDark) }]
      : [],
  );
  const leakGradientId = new Map(leakGradients.map((g) => [g.r, g.id]));

  const renderRibbonPaths = (r: FlowRibbon) => {
    const d = bezierPath(r.x1, r.y1, r.x2, r.y2, r.sameColumn);
    const isSelFinding = r.finding != null && findingKey(r.finding) === selectedFindingKey;
    const handleClick = r.finding ? () => onSelectFinding(r.finding!) : undefined;

    if (r.kind === 'segment_link') {
      return <g key={`path-${r.id}`}><path d={d} fill="none" stroke={vColor} strokeWidth={3.2} strokeLinecap="round" /></g>;
    }

    if (r.kind === 'endpoint_link') {
      if (r.isForeignLeak) {
        return (
          <g key={`path-${r.id}`} style={{ cursor: handleClick ? 'pointer' : undefined }} onClick={handleClick}>
            <Rail d={d} stroke={C.red} width={4} innerWidth={1.6} canvas={C.canvas} />
          </g>
        );
      }
      if (r.state === 'stranded') {
        return <g key={`path-${r.id}`}><path d={d} fill="none" stroke={C.amber} strokeWidth={2.2} strokeDasharray="5,4" opacity={0.8} /></g>;
      }
      if (r.linkKind === 'wireless_uplink') {
        return <g key={`path-${r.id}`}><path d={d} fill="none" stroke={vColor} strokeWidth={2.6} strokeDasharray="5,4" strokeLinecap="round" /></g>;
      }
      return <g key={`path-${r.id}`}><Rail d={d} stroke={vColor} width={4.6} innerWidth={1.8} canvas={C.canvas} /></g>;
    }

    if (r.state === 'leak') {
      const gid = leakGradientId.get(r);
      return (
        <RibbonGroup key={`path-${r.id}`} d={d} selected={isSelFinding} onClick={handleClick} hitWidth={20} glowWidth={10} glowOpacity={0.45}>
          <Rail d={d} stroke={gid ? `url(#${gid})` : C.red} width={6.4} innerWidth={2} innerDash="5,5" canvas={C.canvas} />
        </RibbonGroup>
      );
    }

    if (r.state === 'dead_end') {
      const fromCarries = r.fromMode !== 'none';
      const { first, second, mx, my } = splitBezier(r.x1, r.y1, r.x2, r.y2, r.sameColumn);
      return (
        <RibbonGroup key={`path-${r.id}`} d={d} selected={isSelFinding} onClick={handleClick}>
          <path
            d={first} fill="none"
            stroke={fromCarries ? vColor : C.amber} strokeWidth={fromCarries ? 3.2 : 2}
            strokeDasharray={fromCarries ? undefined : '4,4'} opacity={fromCarries ? 1 : 0.72}
          />
          <path
            d={second} fill="none"
            stroke={fromCarries ? C.amber : vColor} strokeWidth={fromCarries ? 2 : 3.2}
            strokeDasharray={fromCarries ? '4,4' : undefined} opacity={fromCarries ? 0.72 : 1}
          />
          {r.sameColumn ? (
            <line x1={mx - 12} y1={my} x2={mx + 12} y2={my} stroke={C.amber} strokeWidth={3.2} strokeLinecap="round" />
          ) : (
            <line x1={mx} y1={my - 12} x2={mx} y2={my + 12} stroke={C.amber} strokeWidth={3.2} strokeLinecap="round" />
          )}
        </RibbonGroup>
      );
    }

    if (r.state === 'mismatch') {
      return (
        <RibbonGroup key={`path-${r.id}`} d={d} selected={isSelFinding} onClick={handleClick}>
          <path d={d} fill="none" stroke={C.amber} strokeWidth={4} strokeDasharray="7,4" strokeLinecap="round" />
        </RibbonGroup>
      );
    }

    if (r.state === 'unverified') {
      return <g key={`path-${r.id}`}><path d={d} fill="none" stroke={C.gray} strokeWidth={2.6} strokeDasharray="3,4" strokeLinecap="round" /></g>;
    }
    if (r.linkKind === 'lag') {
      return <g key={`path-${r.id}`}><Rail d={d} stroke={vColor} width={6.5} innerWidth={2} canvas={C.canvas} /></g>;
    }
    if (r.fromMode === 'untagged' && r.toMode === 'untagged') {
      return <g key={`path-${r.id}`}><Rail d={d} stroke={vColor} width={5.2} innerWidth={2.2} canvas={C.canvas} /></g>;
    }
    if (r.linkKind === 'wireless_uplink') {
      return <g key={`path-${r.id}`}><path d={d} fill="none" stroke={vColor} strokeWidth={2.8} strokeDasharray="6,4" strokeLinecap="round" /></g>;
    }
    return <g key={`path-${r.id}`}><path d={d} fill="none" stroke={vColor} strokeWidth={3.2} strokeLinecap="round" /></g>;
  };

  const renderRibbonOverlay = (r: FlowRibbon) => {
    if (r.kind === 'segment_link') return null;

    const mx = ribbonMidX(r);
    const my = (r.y1 + r.y2) / 2;
    const handleClick = r.finding ? () => onSelectFinding(r.finding!) : undefined;

    if (r.kind === 'endpoint_link') {
      const label = r.isForeignLeak ? `LEAK → ${r.foreignVlanId}` : r.state === 'stranded' ? 'STRANDED' : r.linkKind === 'wireless_uplink' ? 'Wi-Fi' : 'U';
      const color = r.isForeignLeak ? C.red : r.state === 'stranded' ? C.amber : vColor;
      return (
        <g key={`ov-${r.id}`} style={{ cursor: handleClick ? 'pointer' : undefined }} onClick={handleClick}>
          <Pill x={mx} y={my} width={Math.max(26, label.length * 6.0 + 12)} height={19} fill={C.surface} textFill={color} stroke={color} strokeWidth={1.3} label={label} />
        </g>
      );
    }

    if (r.state === 'leak') {
      const lbl = r.finding ? `⚠ LEAK ${r.finding.vlans[0]}⇄${r.finding.vlans[1]}` : '⚠ LEAK';
      return (
        <SvgButton key={`ov-${r.id}`} label={r.finding?.message || 'VLAN leak'} onClick={handleClick}>
          <Pill x={mx} y={my} width={Math.max(96, lbl.length * 6.2 + 16)} height={22} fill={C.red} textFill="#ffffff" stroke={C.surface} strokeWidth={1.2} fontWeight={800} label={lbl} />
        </SvgButton>
      );
    }

    if (r.state === 'dead_end') {
      return (
        <SvgButton key={`ov-${r.id}`} label={r.finding?.message || 'VLAN dead-end'} onClick={handleClick}>
          <Pill x={mx} y={my - 18.5} width={88} height={19} fill={C.amber} textFill={C.amberInk} fontWeight={800} label="⊘ DEAD-END" />
        </SvgButton>
      );
    }

    if (r.state === 'mismatch') {
      const lbl = `⇄ ${modeShort(r.fromMode)}≠${modeShort(r.toMode)}`;
      return (
        <SvgButton key={`ov-${r.id}`} label={r.finding?.message || 'VLAN tag mismatch'} onClick={handleClick}>
          <Pill x={mx} y={my} width={Math.max(76, lbl.length * 6.4 + 14)} height={20} fill={C.amber} textFill={C.amberInk} fontWeight={800} label={lbl} />
        </SvgButton>
      );
    }

    if (r.state === 'unverified') {
      const lbl = r.linkKind === 'wireless_uplink' ? '? Wi-Fi' : '? UNVERIFIED';
      return (
        <g key={`ov-${r.id}`}>
          <Pill x={mx} y={my} width={Math.max(66, lbl.length * 6.0 + 14)} height={19} fill={C.surface} textFill={C.gray} stroke={C.gray} strokeWidth={1.3} strokeDasharray="3,2" label={lbl} />
        </g>
      );
    }

    const lbl = modePill(r.fromMode, r.toMode, r.linkKind);
    return (
      <g key={`ov-${r.id}`}>
        <Pill x={mx} y={my} width={Math.max(28, lbl.length * 6.0 + 12)} height={19} fill={C.surface} textFill={vColor} stroke={vColor} strokeWidth={1.3} label={lbl} />
      </g>
    );
  };

  return (
    <svg
      style={{ width: '100%', minWidth: Math.min(singleLayout.width, 860), height: 'auto' }}
      viewBox={`0 0 ${singleLayout.width} ${singleLayout.height}`}
      role="group"
      aria-label={`VLAN ${activeVlan.id}${activeVlan.name ? ` ${activeVlan.name}` : ''} flow diagram`}
    >
      <defs>
        <pattern id="np-hatch-unreach" width="8" height="8" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
          <line x1="0" y1="0" x2="0" y2="8" stroke={C.amber} strokeWidth="1.5" strokeOpacity="0.24" />
        </pattern>
        {leakGradients.map((g) => (
          <linearGradient key={g.id} id={g.id} gradientUnits="userSpaceOnUse" x1={g.r.x1} y1={g.r.y1} x2={g.r.x2} y2={g.r.y2}>
            <stop offset="0%" stopColor={vColor} />
            <stop offset="46%" stopColor={C.red} />
            <stop offset="54%" stopColor={C.red} />
            <stop offset="100%" stopColor={g.foreignColor} />
          </linearGradient>
        ))}
      </defs>

      <ColumnHeaders columns={singleLayout.columns} C={C} />
      {singleLayout.ribbons.map(renderRibbonPaths)}

      {/* Segment / Zone cards */}
      {singleLayout.segmentCards.map((sc) => {
        if (sc.kind === 'foreign_leak' && sc.finding) {
          const fColor = getVlanColor(sc.vlanId, vlanColors, isDark);
          const fZone = sc.gateway?.zone_name || sc.gateway?.zone_id;
          const fAddr = sc.gateway?.addresses[0] ? `${sc.gateway.addresses[0].address}/${sc.gateway.addresses[0].prefix_length}` : null;
          return (
            <SvgButton key={sc.id} transform={`translate(${sc.x}, ${sc.y})`} label={`Inspect foreign VLAN ${sc.vlanId} leak`} onClick={() => onSelectFinding(sc.finding!)}>
              <rect
                width={sc.width} height={sc.height} rx={6}
                fill={C.isDark ? 'rgba(255,107,107,0.10)' : 'rgba(201,42,42,0.06)'}
                stroke={C.red} strokeWidth={1.6} strokeDasharray="5,3"
              />
              <rect width={5} height={sc.height} rx={2} fill={fColor} />
              <text x={14} y={21} fill={C.red} fontSize={9.5} fontWeight={800} fontFamily={MONO}>⚠ FOREIGN VLAN LEAKED</text>
              <text x={14} y={40} fill={C.text} fontSize={13} fontWeight={700} fontFamily={SANS}>
                {truncate(`VLAN ${sc.vlanId}${sc.vlanName ? ` · ${sc.vlanName}` : ''}`, 24)}
              </text>
              {(fZone || fAddr) && (
                <text x={14} y={58} fill={C.dim} fontSize={10} fontFamily={MONO}>
                  {truncate([fZone ? `zone:${fZone}` : '', fAddr || ''].filter(Boolean).join(' · '), 28)}
                </text>
              )}
              <text x={14} y={78} fill={C.red} fontSize={10} fontWeight={600} fontFamily={SANS}>Cross-VLAN untagged bridge</text>
              <text x={14} y={96} fill={C.dim} fontSize={9.5} fontFamily={MONO}>
                {truncate(portLabel(devicesById, sc.finding.device_id, sc.finding.link), 28)}
              </text>
            </SvgButton>
          );
        }

        const gw = sc.gateway!;
        const gwDev = devicesById.get(gw.device_id);
        const zoneLabel = gw.zone_name || gw.zone_id;
        return (
          <SvgButton key={sc.id} transform={`translate(${sc.x}, ${sc.y})`} label={`Open gateway ${gwDev?.name || gw.device_id}`} onClick={() => onOpenDevice(gw.device_id)}>
            <rect width={sc.width} height={sc.height} rx={6} fill={C.surface} stroke={vColor} strokeWidth={1.6} />
            <rect width={5} height={sc.height} rx={2} fill={vColor} />
            <text x={14} y={22} fill={vColor} fontSize={10} fontWeight={800} fontFamily={MONO}>
              {truncate(zoneLabel ? `ZONE: ${zoneLabel.toUpperCase()}` : 'L3 SEGMENT', 26)}
            </text>
            <text x={14} y={41} fill={C.text} fontSize={13.5} fontWeight={700} fontFamily={SANS}>
              {truncate(`VLAN ${sc.vlanId}${sc.vlanName ? ` · ${sc.vlanName}` : ''}`, 23)}
            </text>
            <text x={14} y={58} fill={C.dim} fontSize={10.5} fontFamily={MONO}>
              {truncate(`${gw.segment_name} (${gwDev?.name || gw.device_id})`, 27)}
            </text>
            <line x1={14} y1={68} x2={sc.width - 14} y2={68} stroke={C.borderSoft} />
            {gw.addresses.slice(0, 2).map((a, idx) => (
              <text key={`${a.address}/${a.prefix_length}`} x={14} y={85 + idx * 16} fill={C.text} fontSize={10.5} fontFamily={MONO}>
                {truncate(`${a.address}/${a.prefix_length}`, 26)}
              </text>
            ))}
            {gw.addresses.length === 0 && (
              <text x={14} y={85} fill={C.dim} fontSize={10.5} fontFamily={MONO}>No L3 IP configured</text>
            )}
            {gw.is_management && (
              <g transform={`translate(14, ${sc.height - 25})`}>
                <rect width={84} height={17} rx={3} fill={C.subtle} stroke={vColor} />
                <text x={42} y={12} textAnchor="middle" fill={vColor} fontSize={9.5} fontWeight={700} fontFamily={MONO}>MANAGEMENT</text>
              </g>
            )}
          </SvgButton>
        );
      })}

      {/* Device cards + port chips */}
      {singleLayout.deviceCards.map((dc) => {
        const dev = devicesById.get(dc.deviceId);
        const dName = dev?.name || dc.deviceId;
        const hasLeak = flow.findings.some(
          (f) => f.kind === 'leak' && f.vlans.includes(activeVlan.id) && (f.device_id === dc.deviceId || f.peer_id === dc.deviceId),
        );
        const unreached = dc.hasGateway && !dc.reached;
        const stroke = hasLeak ? C.red : unreached ? C.amber : C.border;
        const footerParts = [
          dc.localAccessCount > 0 ? `${dc.localAccessCount} access` : '',
          dc.localSsidCount > 0 ? `${dc.localSsidCount} SSID` : '',
        ].filter(Boolean);

        return (
          <g key={dc.id}>
            <SvgButton transform={`translate(${dc.x}, ${dc.y})`} label={`Open device ${dName}`} onClick={() => onOpenDevice(dc.deviceId)}>
              <rect
                width={dc.width} height={dc.height} rx={6}
                fill={unreached ? 'url(#np-hatch-unreach)' : C.surface}
                stroke={stroke} strokeWidth={hasLeak || unreached ? 1.7 : 1.3}
                strokeDasharray={unreached && !hasLeak ? '5,3' : undefined}
              />
              <DeviceCardHead width={dc.width} deviceId={dc.deviceId} dev={dev} C={C} />
              <text x={12} y={dc.height - 10} fill={hasLeak ? C.red : unreached ? C.amber : C.dim} fontSize={9.5} fontWeight={hasLeak || unreached ? 700 : 500} fontFamily={MONO}>
                {hasLeak
                  ? '⚠ CROSS-VLAN LEAK'
                  : unreached
                    ? `⊘ UNREACHABLE ON V${activeVlan.id}`
                    : footerParts.length > 0
                      ? footerParts.join(' · ')
                      : dc.hasGateway ? '✓ Reaches gateway' : 'L2 transit'}
              </text>
            </SvgButton>
            {dc.leftPorts.map((chip) => <PortChipSvg key={chip.id} chip={chip} vColor={vColor} C={C} />)}
            {dc.rightPorts.map((chip) => <PortChipSvg key={chip.id} chip={chip} vColor={vColor} C={C} />)}
          </g>
        );
      })}

      {/* Endpoint cards */}
      {singleLayout.endpointCards.map((ep) => {
        const dName = deviceName(devicesById, ep.deviceId);
        const handleClick = ep.finding ? () => onSelectFinding(ep.finding!) : () => onOpenDevice(ep.deviceId);
        const fill = ep.isForeignLeak
          ? C.isDark ? 'rgba(255,107,107,0.10)' : 'rgba(201,42,42,0.06)'
          : ep.allUnreachable ? 'url(#np-hatch-unreach)' : C.surface;
        const stroke = ep.isForeignLeak ? C.red : ep.allUnreachable ? C.amber : vColor;
        const title = ep.isForeignLeak ? `⚠ LEAKED V${ep.vlanId} ON ${dName}` : ep.allUnreachable ? `⊘ Stranded · ${dName}` : `${dName} Endpoints`;

        return (
          <SvgButton key={ep.id} transform={`translate(${ep.x}, ${ep.y})`} label={title} onClick={handleClick}>
            <rect
              width={ep.width} height={ep.height} rx={6} fill={fill} stroke={stroke}
              strokeWidth={ep.isForeignLeak || ep.allUnreachable ? 1.5 : 1.3}
              strokeDasharray={ep.allUnreachable && !ep.isForeignLeak ? '5,3' : undefined}
            />
            <text x={12} y={21} fill={ep.isForeignLeak ? C.red : ep.allUnreachable ? C.amber : C.text} fontSize={11} fontWeight={700} fontFamily={SANS}>
              {truncate(title, 28)}
            </text>
            {ep.lines.map((ln, idx) => (
              <text key={`${ep.id}-ln-${idx}`} x={12} y={41 + idx * 18} fill={ln.tone === 'warn' ? C.amber : ln.tone === 'dim' ? C.dim : C.text} fontSize={10} fontFamily={MONO}>
                {truncate(ln.text, 31)}
              </text>
            ))}
          </SvgButton>
        );
      })}

      {singleLayout.ribbons.map(renderRibbonOverlay)}
    </svg>
  );
}
