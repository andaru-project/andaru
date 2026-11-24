// SPDX-License-Identifier: AGPL-3.0-or-later
import { Text, useComputedColorScheme } from '@mantine/core';
import { useMemo, useState } from 'react';
import {
  type Device,
  formatSpeed,
  type NetworkState,
  type TopologyEdge,
  type TopologyGraph,
  type TopologyNode,
  type VLANFlow,
  type VLANFlowHop,
} from '../api';
import {
  findingsForDevice,
  findWirelessSignal,
  getSfpAlerts,
  isEdgeStpBlocked,
  onActivate,
  svgPalette,
} from '../vlanUtils';
import { RoleSvgIcon } from './Icons';

export interface TopologySelection {
  type: 'device' | 'edge';
  id: string;
}

interface Props {
  graph: TopologyGraph | null;
  state: NetworkState | null;
  flow: VLANFlow | null;
  devicesById: Map<string, Device>;
  vlanOverlay: number | null;
  showClients: boolean;
  showEvidence: boolean;
  selection: TopologySelection | null;
  onSelect: (sel: TopologySelection) => void;
}

// Narrowest layout width; graphs this small still shrink to fit a narrow panel.
const MIN_SVG_W = 760;

export function TopologyView({
  graph,
  state,
  flow,
  devicesById,
  vlanOverlay,
  showClients,
  showEvidence,
  selection,
  onSelect,
}: Props) {
  const isDark = useComputedColorScheme('light') === 'dark';
  const [focusedId, setFocusedId] = useState<string | null>(null);

  const overlayHopByEdge = useMemo(() => {
    const map = new Map<string, VLANFlowHop>();
    if (vlanOverlay == null || !flow) return map;
    const vl = flow.vlans.find((v) => v.id === vlanOverlay);
    if (!vl) return map;
    for (const h of vl.hops) {
      map.set(h.edge_id, h);
    }
    return map;
  }, [flow, vlanOverlay]);

  const layout = useMemo(() => {
    if (!graph || graph.nodes.length === 0) return null;

    const deviceNodes = graph.nodes.filter((n) => n.kind === 'device');
    const deviceIds = new Set(deviceNodes.map((n) => n.id));
    const infraEdges = graph.edges.filter((e) => deviceIds.has(e.from_id) && deviceIds.has(e.to_id));

    // Undirected adjacency for BFS layering from root_ids.
    const adj = new Map<string, string[]>();
    for (const e of infraEdges) {
      adj.set(e.from_id, [...(adj.get(e.from_id) ?? []), e.to_id]);
      adj.set(e.to_id, [...(adj.get(e.to_id) ?? []), e.from_id]);
    }

    const layer = new Map<string, number>();
    const roots = graph.root_ids.filter((id) => deviceIds.has(id));
    const startNodes = roots.length > 0 ? roots : deviceNodes.length > 0 ? [deviceNodes[0].id] : [];
    const queue: string[] = [];
    for (const r of startNodes) {
      layer.set(r, 0);
      queue.push(r);
    }
    while (queue.length > 0) {
      const cur = queue.shift()!;
      const d = layer.get(cur) ?? 0;
      for (const nxt of adj.get(cur) ?? []) {
        if (!layer.has(nxt)) {
          layer.set(nxt, d + 1);
          queue.push(nxt);
        }
      }
    }
    let maxDevLayer = 0;
    for (const lv of layer.values()) {
      if (lv > maxDevLayer) maxDevLayer = lv;
    }
    for (const n of deviceNodes) {
      if (!layer.has(n.id)) layer.set(n.id, maxDevLayer > 0 ? maxDevLayer : 0);
    }

    // Group devices by layer; sort by parent index to reduce crossings.
    const byLayer = new Map<number, TopologyNode[]>();
    for (const n of deviceNodes) {
      const lv = layer.get(n.id) ?? 0;
      byLayer.set(lv, [...(byLayer.get(lv) ?? []), n]);
    }
    const levels = Array.from(byLayer.keys()).sort((a, b) => a - b);
    const parentPos = new Map<string, number>();
    for (const lv of levels) {
      const arr = byLayer.get(lv) ?? [];
      arr.sort((a, b) => {
        const score = (id: string) => {
          const parents = (adj.get(id) ?? []).filter((p) => (layer.get(p) ?? 99) < lv);
          if (parents.length === 0) return 999;
          return parents.reduce((acc, p) => acc + (parentPos.get(p) ?? 0), 0) / parents.length;
        };
        return score(a.id) - score(b.id) || a.label.localeCompare(b.label);
      });
      arr.forEach((n, idx) => parentPos.set(n.id, idx));
    }

    const nodeW = 200;
    const nodeH = 68;
    const gapX = 56;
    const gapY = 92;
    const padX = 32;
    const padY = 28;

    // Optional client groups attached to each device
    const clientGroups: { devId: string; count: number; label: string; sub: string }[] = [];
    if (showClients && state) {
      const byDev = new Map<string, typeof state.clients>();
      for (const c of state.clients) {
        if (!c.online || !c.attached_device_id || !deviceIds.has(c.attached_device_id)) continue;
        byDev.set(c.attached_device_id, [...(byDev.get(c.attached_device_id) ?? []), c]);
      }
      for (const [devId, cls] of byDev.entries()) {
        const names = cls.slice(0, 3).map((c) => c.hostname || c.mac).join(', ');
        clientGroups.push({
          devId,
          count: cls.length,
          label: `${cls.length} Online Client${cls.length === 1 ? '' : 's'}`,
          sub: cls.length > 3 ? `${names} +${cls.length - 3}` : names,
        });
      }
    }

    const maxCols = Math.max(
      ...levels.map((lv) => (byLayer.get(lv) ?? []).length),
      showClients ? Math.min(clientGroups.length, 4) : 1,
      1,
    );
    let svgW = Math.max(MIN_SVG_W, maxCols * nodeW + (maxCols - 1) * gapX + padX * 2);
    const devRowsH = levels.length * nodeH + Math.max(0, levels.length - 1) * gapY;
    const clientRowY = padY + devRowsH + 68;
    const svgH = padY * 2 + devRowsH + (showClients && clientGroups.length > 0 ? 124 : 10);

    const pos = new Map<string, { x: number; y: number }>();
    levels.forEach((lv, rowIdx) => {
      const arr = byLayer.get(lv) ?? [];
      const rowW = arr.length * nodeW + Math.max(0, arr.length - 1) * gapX;
      const startX = (svgW - rowW) / 2;
      const y = padY + rowIdx * (nodeH + gapY);
      arr.forEach((n, colIdx) => {
        pos.set(n.id, { x: startX + colIdx * (nodeW + gapX), y });
      });
    });

    // Position client summary boxes cleanly in a bottom row ordered by parent X
    clientGroups.sort((a, b) => (pos.get(a.devId)?.x ?? 0) - (pos.get(b.devId)?.x ?? 0));
    const clientPos = new Map<string, { x: number; y: number }>();
    if (clientGroups.length > 0) {
      const cRowW = clientGroups.length * nodeW + Math.max(0, clientGroups.length - 1) * 24;
      svgW = Math.max(svgW, cRowW + padX * 2);
      const startX = (svgW - cRowW) / 2;
      clientGroups.forEach((cg, idx) => {
        clientPos.set(cg.devId, { x: startX + idx * (nodeW + 24), y: clientRowY });
      });
    }

    return {
      deviceNodes,
      infraEdges,
      layer,
      pos,
      clientGroups,
      clientPos,
      nodeW,
      nodeH,
      svgW,
      svgH,
    };
  }, [graph, state, showClients]);

  if (!layout || layout.deviceNodes.length === 0) {
    return (
      <div style={{ padding: 32, textAlign: 'center' }}>
        <Text c="dimmed" size="sm">
          No topology nodes discovered yet. Configure targets and poll to build the cross-vendor map.
        </Text>
      </div>
    );
  }

  const C = svgPalette(isDark);

  // Group parallel edges between the same pair so curves fan out cleanly without colliding.
  const pairGroups = new Map<string, TopologyEdge[]>();
  for (const e of layout.infraEdges) {
    const key = [e.from_id, e.to_id].sort().join('|');
    pairGroups.set(key, [...(pairGroups.get(key) ?? []), e]);
  }

  const findings = flow?.findings ?? [];

  return (
    <svg
      // A graph wider than the minimum layout keeps its natural size and scrolls in the stage;
      // shrinking it to the panel would scale its labels below readable size.
      style={{
        width: '100%',
        minWidth: layout.svgW > MIN_SVG_W ? layout.svgW : Math.min(layout.svgW, 680),
        height: 'auto',
      }}
      viewBox={`0 0 ${layout.svgW} ${layout.svgH}`}
      role="group"
      aria-label="Network physical and logical topology graph"
    >
      {/* Edges */}
      {layout.infraEdges.map((e) => {
        const pFrom = layout.pos.get(e.from_id);
        const pTo = layout.pos.get(e.to_id);
        if (!pFrom || !pTo) return null;

        // Orient top-to-bottom visually
        const swapped = pFrom.y > pTo.y;
        const p1 = swapped ? pTo : pFrom;
        const p2 = swapped ? pFrom : pTo;

        const key = [e.from_id, e.to_id].sort().join('|');
        const siblings = pairGroups.get(key) ?? [e];
        const sibIdx = siblings.indexOf(e);
        const offset = (sibIdx - (siblings.length - 1) / 2) * 34;

        const stpBlocked = state ? isEdgeStpBlocked(state, e) : false;
        const isSelected = selection?.type === 'edge' && selection.id === e.id;

        let pathD: string;
        let midX: number;
        let midY: number;

        if (Math.abs(p1.y - p2.y) < 5) {
          // Same-layer peer link: arch softly above
          const left = p1.x < p2.x ? p1 : p2;
          const right = p1.x < p2.x ? p2 : p1;
          const x1 = left.x + layout.nodeW;
          const y1 = left.y + layout.nodeH / 2 + offset * 0.4;
          const x2 = right.x;
          const y2 = right.y + layout.nodeH / 2 + offset * 0.4;
          const archY = Math.min(y1, y2) - 24 - Math.abs(offset);
          pathD = `M ${x1} ${y1} Q ${(x1 + x2) / 2} ${archY}, ${x2} ${y2}`;
          midX = (x1 + x2) / 2;
          // Labels stack apart from the curves: one 22-unit lane per sibling clears the 20-unit pill.
          const yc = Math.min(left.y, right.y) + layout.nodeH / 2;
          midY = yc - 8 + (sibIdx - (siblings.length - 1) / 2) * 22;
        } else {
          const x1 = p1.x + layout.nodeW / 2 + offset * 0.5;
          const y1 = p1.y + layout.nodeH;
          const x2 = p2.x + layout.nodeW / 2 + offset * 0.5;
          const y2 = p2.y;
          const dy = Math.max(36, (y2 - y1) * 0.45);
          const ctrlOffset = offset * 1.4;
          pathD = `M ${x1} ${y1} C ${x1 + ctrlOffset} ${y1 + dy}, ${x2 + ctrlOffset} ${y2 - dy}, ${x2} ${y2}`;
          midX = (x1 + x2) / 2 + ctrlOffset * 0.72;
          midY = (y1 + y2) / 2 + (sibIdx % 2 === 0 ? -6 : 14);
        }

        let stroke = e.kind === 'lag' ? C.blue : e.kind === 'wireless_uplink' ? C.violet : C.teal;
        let strokeW = e.kind === 'lag' ? 4.2 : 2.2;
        let dash = e.kind === 'wireless_uplink' ? '6,4' : '';
        let opacity = 1;
        let overlayBadge = '';

        if (stpBlocked) {
          stroke = C.gray;
          strokeW = 1.6;
          dash = '4,4';
          opacity = 0.78;
        }

        if (vlanOverlay != null) {
          const hop = overlayHopByEdge.get(e.id);
          if (stpBlocked) {
            stroke = C.gray;
            opacity = 0.45;
          } else if (!hop) {
            stroke = C.gray;
            opacity = 0.25;
          } else if (hop.state === 'carried') {
            stroke = C.green;
            strokeW = e.kind === 'lag' ? 4.5 : 3;
            const m = hop.from_mode === 'untagged' && hop.to_mode === 'untagged' ? 'U' : 'T';
            overlayBadge = `✓ ${m}`;
          } else if (hop.state === 'leak') {
            stroke = C.red;
            strokeW = 3.5;
            overlayBadge = '⚠ LEAK';
          } else if (hop.state === 'dead_end') {
            stroke = C.amber;
            strokeW = 3;
            dash = '6,4';
            overlayBadge = '⊘ DEAD-END';
          } else if (hop.state === 'mismatch') {
            stroke = C.amber;
            strokeW = 3;
            dash = '4,3';
            overlayBadge = '⇄ MISMATCH';
          } else if (hop.state === 'unverified') {
            stroke = C.gray;
            strokeW = 2.4;
            dash = '3,4';
            overlayBadge = '? UNVERIFIED';
          }
        }

        const wifiSig = e.kind === 'wireless_uplink' && state
          ? findWirelessSignal(state, e.from_id, e.to_id)
          : null;

        const mainCap = stpBlocked
          ? `STP BLK${e.speed_mbps ? ` · ${formatSpeed(e.speed_mbps)}` : ''}`
          : e.kind === 'lag'
            ? `LAG ${e.from_link || ''}${e.speed_mbps ? ` · ${formatSpeed(e.speed_mbps)}` : ''}`.trim()
            : e.kind === 'wireless_uplink'
              ? `Wi-Fi${wifiSig?.band ? ` ${wifiSig.band}` : ''}${wifiSig ? ` · ${wifiSig.signal_dbm}dBm` : ''}`
              : `${e.from_link || ''}${e.from_link && e.to_link ? '↔' : ''}${e.to_link || ''}${e.speed_mbps ? ` · ${formatSpeed(e.speed_mbps)}` : ''}`;

        const evTag = showEvidence && !stpBlocked
          ? ` · ${e.evidence === 'wireless_uplink' ? 'MESH' : e.evidence.toUpperCase()}`
          : '';
        const pillText = `${overlayBadge ? `${overlayBadge} · ` : ''}${mainCap}${evTag}`;
        const pillW = Math.max(82, pillText.length * 5.8 + 14);
        const pillBg = overlayBadge.startsWith('⚠')
          ? C.red
          : overlayBadge.startsWith('⊘') || overlayBadge.startsWith('⇄')
            ? C.amber
            : C.surface;
        const pillFg = overlayBadge.startsWith('⚠')
          ? '#ffffff'
          : overlayBadge.startsWith('⊘') || overlayBadge.startsWith('⇄')
            ? '#17212c'
            : C.dim;
        const pillBorder = isSelected ? '#ffb21c' : overlayBadge && !overlayBadge.startsWith('?') ? pillBg : C.border;

        return (
          <g
            key={e.id}
            style={{ cursor: 'pointer', opacity }}
            onClick={() => onSelect({ type: 'edge', id: e.id })}
            role="button"
            tabIndex={0}
            aria-label={`Inspect link ${mainCap}`}
            onKeyDown={onActivate(() => onSelect({ type: 'edge', id: e.id }))}
          >
            <path d={pathD} fill="none" stroke="transparent" strokeWidth={16} />
            {isSelected && (
              <path d={pathD} fill="none" stroke="#ffb21c" strokeWidth={strokeW + 5} strokeOpacity={0.45} />
            )}
            <path
              d={pathD}
              fill="none"
              stroke={stroke}
              strokeWidth={strokeW}
              strokeDasharray={dash || undefined}
              strokeLinecap="round"
            />
            {e.kind === 'lag' && (
              <path d={pathD} fill="none" stroke={C.surface} strokeWidth={1.4} />
            )}
            <g style={{ opacity: opacity < 0.4 ? 0.55 : 1 }}>
              <rect
                x={midX - pillW / 2}
                y={midY - 10}
                width={pillW}
                height={20}
                rx={4}
                fill={pillBg}
                stroke={pillBorder}
                strokeWidth={isSelected ? 1.8 : 1}
                strokeDasharray={overlayBadge.startsWith('?') ? '3,2' : undefined}
              />
              <text
                x={midX}
                y={midY + 3.5}
                textAnchor="middle"
                fill={pillFg}
                fontSize={10}
                fontWeight={600}
                fontFamily="ui-monospace, monospace"
              >
                {pillText}
              </text>
            </g>
          </g>
        );
      })}

      {/* Optional Client Group Boxes */}
      {showClients &&
        layout.clientGroups.map((cg) => {
          const dp = layout.pos.get(cg.devId);
          const cp = layout.clientPos.get(cg.devId);
          if (!dp || !cp) return null;
          return (
            <g key={`cg-${cg.devId}`}>
              <line
                x1={dp.x + layout.nodeW / 2}
                y1={dp.y + layout.nodeH}
                x2={cp.x + layout.nodeW / 2}
                y2={cp.y}
                stroke={C.gray}
                strokeWidth={1.3}
                strokeDasharray="3,3"
              />
              <g transform={`translate(${cp.x}, ${cp.y})`}>
                <rect
                  width={layout.nodeW}
                  height={48}
                  rx={6}
                  fill={C.subtle}
                  stroke={C.border}
                  strokeDasharray="4,2"
                />
                <text x={12} y={20} fill={C.text} fontSize={11.5} fontWeight={600} fontFamily="system-ui, sans-serif">
                  {cg.label}
                </text>
                <text x={12} y={36} fill={C.dim} fontSize={10} fontFamily="ui-monospace, monospace">
                  {cg.sub.slice(0, 26)}
                </text>
              </g>
            </g>
          );
        })}

      {/* Device Nodes */}
      {layout.deviceNodes.map((n) => {
        const p = layout.pos.get(n.id);
        if (!p) return null;
        const dev = devicesById.get(n.id);
        const devLinks = state ? state.links.filter((l) => l.device_id === n.id) : [];
        const upCount = devLinks.filter((l) => l.link_up).length;
        const devFindings = findingsForDevice(findings, n.id);
        const hasLeak = devFindings.some((f) => f.kind === 'leak');
        const hasWarn = devFindings.some((f) => f.kind !== 'leak') || getSfpAlerts(devLinks).length > 0;
        const isSel = selection?.type === 'device' && selection.id === n.id;

        const role = dev?.role || n.role || 'device';
        const accent =
          role === 'firewall' || role === 'gateway' || role === 'router'
            ? '#ffb21c'
            : role === 'ap'
              ? C.violet
              : C.teal;
        const stroke =
          isSel || focusedId === n.id
            ? '#ffb21c'
            : !n.online || hasLeak
              ? C.red
              : C.border;
        const strokeW = isSel || focusedId === n.id ? 2.5 : !n.online || hasLeak ? 1.8 : 1.2;

        return (
          <g
            key={n.id}
            transform={`translate(${p.x}, ${p.y})`}
            style={{ cursor: 'pointer', outline: 'none' }}
            role="button"
            tabIndex={0}
            aria-label={`Select ${n.label}`}
            onClick={() => onSelect({ type: 'device', id: n.id })}
            onKeyDown={onActivate(() => onSelect({ type: 'device', id: n.id }))}
            onFocus={() => setFocusedId(n.id)}
            onBlur={() => setFocusedId(null)}
          >
            <rect
              width={layout.nodeW}
              height={layout.nodeH}
              rx={6}
              fill={C.surface}
              stroke={stroke}
              strokeWidth={strokeW}
            />
            <rect width={5} height={layout.nodeH} rx={2} fill={accent} />
            <g transform="translate(14, 12)" style={{ color: accent }}>
              <RoleSvgIcon role={role} size={20} />
            </g>
            <text x={40} y={25} fill={C.text} fontSize={13} fontWeight={700} fontFamily="system-ui, sans-serif">
              {n.label.slice(0, 18)}
            </text>
            <text x={40} y={40} fill={C.dim} fontSize={10} fontFamily="ui-monospace, monospace">
              {[dev?.driver || n.driver, dev?.mgmt_ip || n.ip].filter(Boolean).join(' · ').slice(0, 24)}
            </text>
            <text x={14} y={58} fill={C.dim} fontSize={9.5} fontFamily="ui-monospace, monospace">
              {role.toUpperCase()}
              {devLinks.length > 0 ? ` · ${upCount}/${devLinks.length} up` : ''}
              {dev?.poe_budget ? ` · ${dev.poe_budget.used_watts.toFixed(1)}W` : ''}
            </text>
            {(!n.online || hasLeak) ? (
              <g transform={`translate(${layout.nodeW - 22}, 10)`}>
                <circle cx={6} cy={6} r={6} fill={C.red} />
                <text x={6} y={9.5} textAnchor="middle" fill="#fff" fontSize={9} fontWeight={700}>!</text>
              </g>
            ) : hasWarn ? (
              <g transform={`translate(${layout.nodeW - 22}, 10)`}>
                <circle cx={6} cy={6} r={6} fill={C.amber} />
                <text x={6} y={9.5} textAnchor="middle" fill="#17212c" fontSize={9} fontWeight={700}>!</text>
              </g>
            ) : null}
          </g>
        );
      })}
    </svg>
  );
}
