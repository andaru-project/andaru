// SPDX-License-Identifier: AGPL-3.0-or-later
import type {
  Device,
  HopState,
  TopologyGraph,
  VLANFlow,
  VLANFlowAccessPort,
  VLANFlowFinding,
  VLANFlowGateway,
  VLANFlowSSID,
  VLANFlowVLAN,
  VLANMode,
} from './api';
import { findingKey } from './vlanUtils';

type FlowColumnKind = 'segments' | 'vlans' | 'devices' | 'endpoints';
type RibbonState = HopState | 'stranded';

interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

interface FlowColumn {
  index: number;
  kind: FlowColumnKind;
  hopDistance?: number;
  label: string;
  x: number;
  width: number;
}

interface FlowSegmentCard extends Rect {
  id: string;
  kind: 'gateway' | 'foreign_leak';
  vlanId: number;
  vlanName?: string;
  gateway?: VLANFlowGateway;
  finding?: VLANFlowFinding;
}

export interface FlowPortChip {
  id: string;
  side: 'left' | 'right';
  label: string;
  mode: VLANMode;
  state: HopState;
  hopEdgeId: string;
  x: number;
  y: number;
}

interface FlowDeviceCard extends Rect {
  id: string;
  deviceId: string;
  hopDistance: number;
  columnIndex: number;
  reached: boolean;
  hasGateway: boolean;
  leftPorts: FlowPortChip[];
  rightPorts: FlowPortChip[];
  localAccessCount: number;
  localSsidCount: number;
}

interface FlowEndpointLine {
  text: string;
  tone: 'normal' | 'dim' | 'warn';
}

interface FlowEndpointCard extends Rect {
  id: string;
  deviceId: string;
  vlanId: number;
  isForeignLeak: boolean;
  isWirelessOnly: boolean;
  lines: FlowEndpointLine[];
  allUnreachable: boolean;
  finding?: VLANFlowFinding;
}

export interface FlowRibbon {
  id: string;
  kind: 'segment_link' | 'hop' | 'endpoint_link';
  edgeId?: string;
  finding?: VLANFlowFinding;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  sameColumn: boolean;
  linkKind: string;
  fromMode: VLANMode;
  toMode: VLANMode;
  state: RibbonState;
  isForeignLeak?: boolean;
  foreignVlanId?: number;
}

interface SingleVlanFlowLayout {
  width: number;
  height: number;
  columns: FlowColumn[];
  segmentCards: FlowSegmentCard[];
  deviceCards: FlowDeviceCard[];
  endpointCards: FlowEndpointCard[];
  ribbons: FlowRibbon[];
}

interface AllVlansVlanCard extends Rect {
  id: string;
  vlan: VLANFlowVLAN;
  findings: VLANFlowFinding[];
}

interface AllVlansDeviceCard extends Rect {
  id: string;
  deviceId: string;
  hopDistance: number;
  columnIndex: number;
  vlanIds: number[];
  findings: VLANFlowFinding[];
}

interface AllVlansEndpointEntry {
  vlanId: number;
  accessCount: number;
  unreachableAccessCount: number;
  ssids: { name: string; reachable: boolean }[];
  hasLeak: boolean;
}

interface AllVlansEndpointCard extends Rect {
  id: string;
  deviceId: string;
  entries: AllVlansEndpointEntry[];
  hasUnreachable: boolean;
  hasLeak: boolean;
}

interface AllVlansEdgeBundle {
  edgeId: string;
  fromDeviceId: string;
  toDeviceId: string;
  linkKind: string;
  sameColumn: boolean;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  maxSpread: number;
  lanes: { vlanId: number; state: HopState; fromMode: VLANMode; toMode: VLANMode; finding?: VLANFlowFinding }[];
  findings: VLANFlowFinding[];
}

interface AllVlansSegmentLink {
  vlanId: number;
  deviceId: string;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
}

interface AllVlansEndpointLink {
  deviceId: string;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  hasLeak: boolean;
  hasUnreachable: boolean;
}

interface AllVlansFlowLayout {
  width: number;
  height: number;
  columns: FlowColumn[];
  vlanCards: AllVlansVlanCard[];
  deviceCards: AllVlansDeviceCard[];
  endpointCards: AllVlansEndpointCard[];
  segmentLinks: AllVlansSegmentLink[];
  edgeBundles: AllVlansEdgeBundle[];
  endpointLinks: AllVlansEndpointLink[];
}

const PAD_X = 20;
const TOP_HEADER_Y = 48;
const PAD_BOTTOM = 28;
const COL_GAP = 148;
const CARD_GAP_Y = 22;
const SEGMENT_COL_W = 204;
const DEVICE_COL_W = 228;
const ENDPOINT_COL_W = 228;

function pushMap<K, V>(map: Map<K, V[]>, key: K, val: V) {
  const arr = map.get(key);
  if (arr) arr.push(val);
  else map.set(key, [val]);
}

function addSet<K, V>(map: Map<K, Set<V>>, key: K, val: V) {
  let s = map.get(key);
  if (!s) map.set(key, (s = new Set()));
  s.add(val);
}

function addAdj(adj: Map<string, Set<string>>, a: string, b: string) {
  if (a === b) return;
  addSet(adj, a, b);
  addSet(adj, b, a);
}

function displayName(devicesById: Map<string, Device> | undefined, id: string): string {
  return devicesById?.get(id)?.name || id;
}

function computeHopDistances(
  participating: string[],
  seeds: string[],
  primaryAdj: Map<string, Set<string>>,
  fallbackAdj: Map<string, Set<string>>,
  devicesById?: Map<string, Device>,
): Map<string, number> {
  const partSet = new Set(participating);
  const dist = new Map<string, number>();
  if (participating.length === 0) return dist;

  let startNodes = Array.from(new Set(seeds.filter((s) => partSet.has(s))));
  if (startNodes.length === 0) {
    const sorted = [...partSet].sort(
      (a, b) =>
        (primaryAdj.get(b)?.size ?? 0) - (primaryAdj.get(a)?.size ?? 0) ||
        displayName(devicesById, a).localeCompare(displayName(devicesById, b)),
    );
    startNodes = [sorted[0]];
  }

  const runBfs = (adj: Map<string, Set<string>>, initialQueue: string[]) => {
    const queue = [...initialQueue];
    while (queue.length > 0) {
      const cur = queue.shift()!;
      const d = dist.get(cur)!;
      const neighbors = Array.from(adj.get(cur) ?? []).sort((a, b) =>
        displayName(devicesById, a).localeCompare(displayName(devicesById, b)),
      );
      for (const nxt of neighbors) {
        if (!partSet.has(nxt) || dist.has(nxt)) continue;
        dist.set(nxt, d + 1);
        queue.push(nxt);
      }
    }
  };

  for (const s of startNodes) dist.set(s, 0);
  runBfs(primaryAdj, startNodes);

  if (dist.size < partSet.size && fallbackAdj.size > 0) {
    runBfs(fallbackAdj, Array.from(dist.keys()).sort((a, b) => dist.get(a)! - dist.get(b)!));
  }
  if (dist.size < partSet.size) {
    const maxD = Math.max(-1, ...dist.values());
    const orphanDist = maxD >= 0 ? maxD + 1 : 0;
    for (const id of [...partSet].sort((a, b) => displayName(devicesById, a).localeCompare(displayName(devicesById, b)))) {
      if (!dist.has(id)) dist.set(id, orphanDist);
    }
  }
  return dist;
}

function buildTopoAdj(topology?: TopologyGraph | null): Map<string, Set<string>> {
  const adj = new Map<string, Set<string>>();
  if (!topology) return adj;
  const deviceIds = new Set(topology.nodes.filter((n) => n.kind === 'device').map((n) => n.id));
  for (const e of topology.edges) {
    if (deviceIds.has(e.from_id) && deviceIds.has(e.to_id)) addAdj(adj, e.from_id, e.to_id);
  }
  return adj;
}

function deviceColumnLabel(colNum: number, dist: number, hasGateways: boolean, maxDist: number): string {
  if (hasGateways && dist === 0) return `${colNum}. GATEWAYS`;
  if (dist === 0 || (dist === 1 && maxDist >= 2)) return `${colNum}. CORE FABRIC`;
  if (dist === 1) return `${colNum}. SWITCHES & APs`;
  if (dist === 2) return `${colNum}. DISTRIBUTION`;
  return `${colNum}. HOP ${dist}`;
}

function orderByBarycenter(
  deviceIds: string[],
  prevOrder: Map<string, number>,
  adj: Map<string, Set<string>>,
  devicesById?: Map<string, Device>,
): string[] {
  const score = (id: string) => {
    const parents = Array.from(adj.get(id) ?? []).filter((p) => prevOrder.has(p));
    return parents.length === 0 ? Number.MAX_SAFE_INTEGER : parents.reduce((s, p) => s + prevOrder.get(p)!, 0) / parents.length;
  };
  return [...deviceIds].sort(
    (a, b) => score(a) - score(b) || displayName(devicesById, a).localeCompare(displayName(devicesById, b)),
  );
}

function colHeight(items: { height: number }[]): number {
  return items.length === 0 ? 0 : items.reduce((s, it) => s + it.height, 0) + CARD_GAP_Y * (items.length - 1);
}

function stackColumn<T extends { height: number; y: number }>(items: T[], totalHeight: number) {
  if (items.length === 0) return;
  let y = TOP_HEADER_Y + Math.max(0, (totalHeight - TOP_HEADER_Y - PAD_BOTTOM - colHeight(items)) / 2);
  for (const it of items) {
    it.y = Math.round(y);
    y += it.height + CARD_GAP_Y;
  }
}

function railY(top: number, span: number, idx: number, count: number): number {
  return count === 1 ? Math.round(top + span / 2) : Math.round(top + (span * idx) / (count - 1));
}

// A same-column ribbon draws its pill 46 units right of the column (VlanFlowCanvas ribbonMidX),
// up to about 56 units wide on either side, so the layout must reach that far.
function widthWithSameColumnPills(width: number, ribbons: { sameColumn: boolean; x1: number; x2: number }[]): number {
  return ribbons.reduce((w, r) => (r.sameColumn ? Math.max(w, Math.round(Math.max(r.x1, r.x2) + 46 + 56 + PAD_X)) : w), width);
}

function orientEdge<T extends Rect & { columnIndex: number }>(cFrom: T, cTo: T) {
  const sameCol = cFrom.columnIndex === cTo.columnIndex;
  const swap = sameCol ? cFrom.y > cTo.y : cFrom.columnIndex > cTo.columnIndex;
  const leftCard = swap ? cTo : cFrom;
  const rightCard = swap ? cFrom : cTo;
  return {
    sameCol,
    swap,
    leftCard,
    rightCard,
    x1: leftCard.x + leftCard.width,
    x2: sameCol ? rightCard.x + rightCard.width : rightCard.x,
  };
}

interface LayeredGrid {
  byDist: Map<number, string[]>;
  distLevels: number[];
  columns: FlowColumn[];
  distToColIdx: Map<number, number>;
  devColMap: Map<string, number>;
  endpointColIdx: number;
  width: number;
}

/**
 * Shared layering & column-grid core for Single-VLAN and All-VLANs layouts.
 */
function buildFlowGrid(opts: {
  participating: string[];
  gatewayDeviceIds: string[];
  hopAdj: Map<string, Set<string>>;
  topology?: TopologyGraph | null;
  devicesById?: Map<string, Device>;
  leadColumn: { kind: 'segments' | 'vlans'; label: string } | null;
  hasEndpointCol: boolean;
  hasGateways: boolean;
  firstTierCompare: (a: string, b: string) => number;
  minEmptyColW: number;
}): LayeredGrid {
  const partSet = new Set(opts.participating);
  const topoAdj = buildTopoAdj(opts.topology);
  const seeds = opts.gatewayDeviceIds.length > 0
    ? opts.gatewayDeviceIds
    : (opts.topology?.root_ids ?? []).filter((id) => partSet.has(id));

  const hopDistances = computeHopDistances(opts.participating, seeds, opts.hopAdj, topoAdj, opts.devicesById);

  const byDist = new Map<number, string[]>();
  for (const devId of opts.participating) pushMap(byDist, hopDistances.get(devId) ?? 0, devId);
  const distLevels = Array.from(byDist.keys()).sort((a, b) => a - b);
  const maxDist = distLevels[distLevels.length - 1] ?? 0;

  const prevOrder = new Map<string, number>();
  const orderAdj = opts.hopAdj.size > 0 ? opts.hopAdj : topoAdj;
  for (const d of distLevels) {
    const ordered = d === distLevels[0]
      ? [...byDist.get(d)!].sort(opts.firstTierCompare)
      : orderByBarycenter(byDist.get(d)!, prevOrder, orderAdj, opts.devicesById);
    byDist.set(d, ordered);
    ordered.forEach((id, idx) => prevOrder.set(id, idx));
  }

  const columns: FlowColumn[] = [];
  let cursorX = PAD_X;
  const distToColIdx = new Map<number, number>();

  if (opts.leadColumn) {
    columns.push({ index: 0, kind: opts.leadColumn.kind, label: opts.leadColumn.label, x: cursorX, width: SEGMENT_COL_W });
    cursorX += SEGMENT_COL_W + COL_GAP;
  }

  for (const d of distLevels) {
    const colIdx = columns.length;
    distToColIdx.set(d, colIdx);
    columns.push({
      index: colIdx,
      kind: 'devices',
      hopDistance: d,
      label: deviceColumnLabel(colIdx + 1, d, opts.hasGateways, maxDist),
      x: cursorX,
      width: DEVICE_COL_W,
    });
    cursorX += DEVICE_COL_W + COL_GAP;
  }

  const endpointColIdx = opts.hasEndpointCol ? columns.length : -1;
  if (opts.hasEndpointCol) {
    columns.push({ index: endpointColIdx, kind: 'endpoints', label: `${endpointColIdx + 1}. ENDPOINTS`, x: cursorX, width: ENDPOINT_COL_W });
    cursorX += ENDPOINT_COL_W + PAD_X;
  } else {
    cursorX = Math.max(cursorX - COL_GAP + PAD_X, PAD_X * 2 + opts.minEmptyColW);
  }

  const devColMap = new Map<string, number>();
  for (const d of distLevels) {
    const cIdx = distToColIdx.get(d)!;
    for (const devId of byDist.get(d) ?? []) devColMap.set(devId, cIdx);
  }

  return { byDist, distLevels, columns, distToColIdx, devColMap, endpointColIdx, width: Math.max(720, Math.round(cursorX)) };
}

function buildDeviceColumnCards<T>(
  grid: LayeredGrid,
  makeCard: (devId: string, d: number, colIdx: number, col: FlowColumn) => T,
): { byId: Map<string, T>; byCol: Map<number, T[]> } {
  const byId = new Map<string, T>();
  const byCol = new Map<number, T[]>();
  for (const d of grid.distLevels) {
    const colIdx = grid.distToColIdx.get(d)!;
    const col = grid.columns[colIdx];
    const cards = (grid.byDist.get(d) ?? []).map((devId) => {
      const card = makeCard(devId, d, colIdx, col);
      byId.set(devId, card);
      return card;
    });
    byCol.set(colIdx, cards);
  }
  return { byId, byCol };
}

function stackFlowColumns<TLead extends Rect, TDev extends Rect, TEp extends Rect>(
  leadCards: TLead[],
  deviceCardsByCol: Map<number, TDev[]>,
  endpointCards: TEp[],
  minTotalH: number,
  minContentH: number,
  sortEndpoints: (a: TEp, b: TEp) => number,
): number {
  const heights = [
    colHeight(leadCards),
    ...Array.from(deviceCardsByCol.values(), (cards) => colHeight(cards)),
    colHeight(endpointCards),
  ];
  const totalHeight = Math.max(minTotalH, TOP_HEADER_Y + Math.max(...heights, minContentH) + PAD_BOTTOM);
  stackColumn(leadCards, totalHeight);
  for (const cards of deviceCardsByCol.values()) stackColumn(cards, totalHeight);
  endpointCards.sort(sortEndpoints);
  stackColumn(endpointCards, totalHeight);
  return Math.round(totalHeight);
}

/**
 * Computes a left-to-right layered column layout for a single VLAN from `/api/v1/vlans/flow`.
 */
export function computeSingleVlanLayout(
  vlan: VLANFlowVLAN,
  flow: VLANFlow,
  devicesById?: Map<string, Device>,
  topology?: TopologyGraph | null,
): SingleVlanFlowLayout {
  const vlansById = new Map(flow.vlans.map((v) => [v.id, v]));
  const vlanFindings = flow.findings.filter((f) => f.vlans.includes(vlan.id));
  const findingByEdge = new Map<string, VLANFlowFinding>();
  for (const f of vlanFindings) {
    const existing = findingByEdge.get(f.edge_id);
    if (!existing || (f.severity === 'crit' && existing.severity !== 'crit')) findingByEdge.set(f.edge_id, f);
  }

  const participatingSet = new Set<string>(vlan.devices);
  const hopAdj = new Map<string, Set<string>>();
  for (const g of vlan.gateways) participatingSet.add(g.device_id);
  for (const h of vlan.hops) {
    participatingSet.add(h.from_id);
    participatingSet.add(h.to_id);
    addAdj(hopAdj, h.from_id, h.to_id);
  }
  const accessByDev = new Map<string, VLANFlowAccessPort[]>();
  for (const ap of vlan.access_ports) {
    participatingSet.add(ap.device_id);
    pushMap(accessByDev, ap.device_id, ap);
  }
  const ssidsByDev = new Map<string, VLANFlowSSID[]>();
  for (const s of vlan.ssids) {
    participatingSet.add(s.device_id);
    pushMap(ssidsByDev, s.device_id, s);
  }

  const gatewayDeviceIds = Array.from(new Set(vlan.gateways.map((g) => g.device_id)));

  // Identify foreign VLANs and foreign leaked endpoints from leak findings.
  type EndpointSpec = {
    id: string;
    deviceId: string;
    vlanId: number;
    isForeignLeak: boolean;
    accessPorts: VLANFlowAccessPort[];
    ssids: VLANFlowSSID[];
    finding?: VLANFlowFinding;
  };
  const foreignLeaks: { foreignVid: number; finding: VLANFlowFinding }[] = [];
  const foreignEndpointSpecs: EndpointSpec[] = [];
  const seenForeign = new Set<number>();
  for (const lf of vlanFindings) {
    if (lf.kind !== 'leak') continue;
    const otherVid = lf.vlans.find((v) => v !== vlan.id);
    if (otherVid == null) continue;
    if (!seenForeign.has(otherVid)) {
      seenForeign.add(otherVid);
      foreignLeaks.push({ foreignVid: otherVid, finding: lf });
    }
    const foreignVlan = vlansById.get(otherVid);
    if (!foreignVlan) continue;
    // A link leak's foreign VLAN lives on the far end; a LAG leak (members with different PVIDs) has
    // both VLANs on the LAG's own device, so fall back to the other end when the first has none.
    const ends = lf.vlans[0] === vlan.id ? [lf.peer_id, lf.device_id] : [lf.device_id, lf.peer_id];
    const onDev = (d: string) => ({
      aps: foreignVlan.access_ports.filter((ap) => ap.device_id === d),
      ssids: foreignVlan.ssids.filter((s) => s.device_id === d),
    });
    const targetDev = ends.find((d) => { const e = onDev(d); return e.aps.length + e.ssids.length > 0; }) ?? ends[0];
    const { aps: fAps, ssids: fSsids } = onDev(targetDev);
    if (fAps.length > 0 || fSsids.length > 0) {
      foreignEndpointSpecs.push({
        id: `ep-leak:${targetDev}:v${otherVid}:${findingKey(lf)}`,
        deviceId: targetDev,
        vlanId: otherVid,
        isForeignLeak: true,
        accessPorts: fAps,
        ssids: fSsids,
        finding: lf,
      });
    }
  }

  const hasSegmentCol = vlan.gateways.length > 0 || foreignLeaks.length > 0;
  const hasEndpointCol = vlan.access_ports.length > 0 || vlan.ssids.length > 0 || foreignEndpointSpecs.length > 0;

  const grid = buildFlowGrid({
    participating: Array.from(participatingSet),
    gatewayDeviceIds,
    hopAdj,
    topology,
    devicesById,
    leadColumn: hasSegmentCol ? { kind: 'segments', label: '1. L3 ZONES & SEGMENTS' } : null,
    hasEndpointCol,
    hasGateways: vlan.gateways.length > 0,
    firstTierCompare: (a, b) => {
      const ga = gatewayDeviceIds.indexOf(a);
      const gb = gatewayDeviceIds.indexOf(b);
      if (ga !== -1 || gb !== -1) return (ga === -1 ? 999 : ga) - (gb === -1 ? 999 : gb);
      return displayName(devicesById, a).localeCompare(displayName(devicesById, b));
    },
    minEmptyColW: DEVICE_COL_W,
  });

  const endpointSpecs: EndpointSpec[] = [];
  for (const d of grid.distLevels) {
    for (const devId of grid.byDist.get(d) ?? []) {
      const aps = accessByDev.get(devId) ?? [];
      const ssids = ssidsByDev.get(devId) ?? [];
      if (aps.length > 0 || ssids.length > 0) {
        endpointSpecs.push({ id: `ep:${devId}:v${vlan.id}`, deviceId: devId, vlanId: vlan.id, isForeignLeak: false, accessPorts: aps, ssids });
      }
    }
  }
  endpointSpecs.push(...foreignEndpointSpecs);

  const deviceColOffset = hasSegmentCol ? 1 : 0;
  const leftPortCount = new Map<string, number>();
  const rightPortCount = new Map<string, number>();
  const inc = (m: Map<string, number>, k: string) => m.set(k, (m.get(k) ?? 0) + 1);

  for (const g of vlan.gateways) inc(leftPortCount, g.device_id);
  for (const h of vlan.hops) {
    const cFrom = grid.devColMap.get(h.from_id) ?? deviceColOffset;
    const cTo = grid.devColMap.get(h.to_id) ?? deviceColOffset;
    if (cFrom === cTo) {
      inc(rightPortCount, h.from_id);
      inc(rightPortCount, h.to_id);
    } else {
      inc(rightPortCount, cFrom < cTo ? h.from_id : h.to_id);
      inc(leftPortCount, cFrom < cTo ? h.to_id : h.from_id);
    }
  }
  for (const ep of endpointSpecs) inc(rightPortCount, ep.deviceId);

  const segmentCards: FlowSegmentCard[] = [];
  if (hasSegmentCol) {
    const segCol = grid.columns[0];
    vlan.gateways.forEach((gw, idx) => {
      const addrLines = Math.min(2, Math.max(1, gw.addresses.length));
      segmentCards.push({
        id: `gw:${gw.device_id}:${gw.segment_id}:${idx}`,
        kind: 'gateway',
        vlanId: vlan.id,
        vlanName: vlan.name,
        gateway: gw,
        x: segCol.x,
        y: 0,
        width: segCol.width,
        height: 92 + addrLines * 16 + (gw.is_management ? 24 : 0),
      });
    });
    for (const fl of foreignLeaks) {
      const fv = vlansById.get(fl.foreignVid);
      segmentCards.push({
        id: `foreign:${fl.foreignVid}:${findingKey(fl.finding)}`,
        kind: 'foreign_leak',
        vlanId: fl.foreignVid,
        vlanName: fv?.name,
        gateway: fv?.gateways[0],
        finding: fl.finding,
        x: segCol.x,
        y: 0,
        width: segCol.width,
        height: 112,
      });
    }
  }

  const reachedSet = new Set(vlan.reached);
  const hasGateway = vlan.gateways.length > 0;
  const { byId: deviceCardsById, byCol: deviceCardsByCol } = buildDeviceColumnCards<FlowDeviceCard>(
    grid,
    (devId, d, colIdx, col) => ({
      id: `dev:${devId}`,
      deviceId: devId,
      hopDistance: d,
      columnIndex: colIdx,
      reached: hasGateway ? reachedSet.has(devId) : true,
      hasGateway,
      x: col.x,
      y: 0,
      width: col.width,
      height: Math.max(114, 52 + Math.max(leftPortCount.get(devId) ?? 0, rightPortCount.get(devId) ?? 0, 1) * 29 + 30),
      leftPorts: [],
      rightPorts: [],
      localAccessCount: (accessByDev.get(devId) ?? []).length,
      localSsidCount: (ssidsByDev.get(devId) ?? []).length,
    }),
  );

  const endpointCards: FlowEndpointCard[] = endpointSpecs.map((spec) => {
    const allUnreachable =
      !spec.isForeignLeak && hasGateway && spec.accessPorts.every((ap) => !ap.reachable) && spec.ssids.every((s) => !s.reachable);
    const lines: FlowEndpointLine[] = [
      ...spec.accessPorts.slice(0, 4).map((ap): FlowEndpointLine => ({
        text: `[U] ${ap.link}${ap.label ? ` · ${ap.label}` : ''}`,
        // A mixed card still flags each port the gateway cannot reach.
        tone: allUnreachable ? 'dim' : hasGateway && !spec.isForeignLeak && !ap.reachable ? 'warn' : 'normal',
      })),
      ...(spec.accessPorts.length > 4 ? [{ text: `+${spec.accessPorts.length - 4} more access port(s)`, tone: 'dim' as const }] : []),
      ...spec.ssids.slice(0, 3).map((s): FlowEndpointLine => ({
        text: `SSID ${s.name} (${s.client_count} cl)`,
        tone: !s.reachable && hasGateway ? 'warn' : 'normal',
      })),
      ...(spec.ssids.length > 3 ? [{ text: `+${spec.ssids.length - 3} more SSID(s)`, tone: 'dim' as const }] : []),
    ];
    const col = grid.columns[grid.endpointColIdx];
    return {
      id: spec.id,
      deviceId: spec.deviceId,
      vlanId: spec.vlanId,
      isForeignLeak: spec.isForeignLeak,
      isWirelessOnly: spec.ssids.length > 0 && spec.accessPorts.length === 0,
      lines,
      allUnreachable,
      finding: spec.finding,
      x: col.x,
      y: 0,
      width: col.width,
      height: Math.max(76, 46 + lines.length * 18 + (spec.isForeignLeak ? 18 : 0)),
    };
  });

  const height = stackFlowColumns(segmentCards, deviceCardsByCol, endpointCards, 320, 220, (a, b) => {
    const da = deviceCardsById.get(a.deviceId);
    const db = deviceCardsById.get(b.deviceId);
    const ya = da ? da.y + da.height / 2 : 0;
    const yb = db ? db.y + db.height / 2 : 0;
    return ya !== yb ? ya - yb : Number(a.isForeignLeak) - Number(b.isForeignLeak);
  });

  type PortSlot = {
    side: 'left' | 'right';
    peerY: number;
    assignY: (y: number) => void;
    chip?: { label: string; mode: VLANMode; state: HopState; hopEdgeId: string };
  };
  const slotsByDevice = new Map<string, PortSlot[]>();
  const ribbons: FlowRibbon[] = [];

  for (const sc of segmentCards) {
    if (sc.kind !== 'gateway' || !sc.gateway) continue;
    const devCard = deviceCardsById.get(sc.gateway.device_id);
    if (!devCard) continue;
    const ribbon: FlowRibbon = {
      id: `seg-link:${sc.id}`,
      kind: 'segment_link',
      x1: sc.x + sc.width,
      y1: sc.y + sc.height / 2,
      x2: devCard.x,
      y2: devCard.y + devCard.height / 2,
      sameColumn: false,
      linkKind: 'wired',
      fromMode: 'tagged',
      toMode: 'tagged',
      state: 'carried',
    };
    ribbons.push(ribbon);
    pushMap(slotsByDevice, devCard.deviceId, { side: 'left', peerY: ribbon.y1, assignY: (y) => (ribbon.y2 = y) });
  }

  for (const h of vlan.hops) {
    const cFrom = deviceCardsById.get(h.from_id);
    const cTo = deviceCardsById.get(h.to_id);
    if (!cFrom || !cTo) continue;

    const finding = findingByEdge.get(h.edge_id);
    const { sameCol, swap, leftCard, rightCard, x1, x2 } = orientEdge(cFrom, cTo);
    const leftLink = (swap ? h.to_link : h.from_link) || (h.kind === 'wireless_uplink' ? 'mesh' : 'link');
    const rightLink = (swap ? h.from_link : h.to_link) || (h.kind === 'wireless_uplink' ? 'mesh' : 'link');
    const leftMode: VLANMode = swap ? h.to_mode : h.from_mode;
    const rightMode: VLANMode = swap ? h.from_mode : h.to_mode;

    const ribbon: FlowRibbon = {
      id: `hop:${h.edge_id}`,
      kind: 'hop',
      edgeId: h.edge_id,
      finding,
      x1,
      y1: leftCard.y + leftCard.height / 2,
      x2,
      y2: rightCard.y + rightCard.height / 2,
      sameColumn: sameCol,
      linkKind: h.kind,
      fromMode: leftMode,
      toMode: rightMode,
      state: h.state,
      foreignVlanId: finding?.kind === 'leak' ? finding.vlans.find((v) => v !== vlan.id) : undefined,
    };
    ribbons.push(ribbon);

    pushMap(slotsByDevice, leftCard.deviceId, {
      side: 'right',
      peerY: rightCard.y + rightCard.height / 2,
      assignY: (y) => (ribbon.y1 = y),
      chip: { label: leftLink, mode: leftMode, state: h.state, hopEdgeId: h.edge_id },
    });
    pushMap(slotsByDevice, rightCard.deviceId, {
      side: sameCol ? 'right' : 'left',
      peerY: leftCard.y + leftCard.height / 2,
      assignY: (y) => (ribbon.y2 = y),
      chip: { label: rightLink, mode: rightMode, state: h.state, hopEdgeId: h.edge_id },
    });
  }

  for (const ep of endpointCards) {
    const devCard = deviceCardsById.get(ep.deviceId);
    if (!devCard) continue;
    const ribbon: FlowRibbon = {
      id: `ep-link:${ep.id}`,
      kind: 'endpoint_link',
      finding: ep.finding,
      x1: devCard.x + devCard.width,
      y1: devCard.y + devCard.height / 2,
      x2: ep.x,
      y2: ep.y + ep.height / 2,
      sameColumn: false,
      linkKind: ep.isWirelessOnly ? 'wireless_uplink' : 'wired',
      fromMode: 'untagged',
      toMode: 'untagged',
      state: ep.isForeignLeak ? 'leak' : ep.allUnreachable ? 'stranded' : 'carried',
      isForeignLeak: ep.isForeignLeak,
      foreignVlanId: ep.isForeignLeak ? ep.vlanId : undefined,
    };
    ribbons.push(ribbon);
    pushMap(slotsByDevice, devCard.deviceId, { side: 'right', peerY: ep.y + ep.height / 2, assignY: (y) => (ribbon.y1 = y) });
  }

  const deviceCards = Array.from(deviceCardsById.values());
  for (const card of deviceCards) {
    const slots = slotsByDevice.get(card.deviceId) ?? [];
    for (const side of ['left', 'right'] as const) {
      const sideSlots = slots.filter((s) => s.side === side).sort((a, b) => a.peerY - b.peerY);
      const span = Math.max(0, card.height - 72);
      sideSlots.forEach((slot, idx) => {
        const y = railY(card.y + 46, span, idx, sideSlots.length);
        slot.assignY(y);
        if (slot.chip) {
          const target = side === 'left' ? card.leftPorts : card.rightPorts;
          target.push({
            id: `${card.deviceId}:${side}:${slot.chip.hopEdgeId}:${idx}`,
            side,
            ...slot.chip,
            x: side === 'left' ? card.x + 6 : card.x + card.width - 6,
            y,
          });
        }
      });
    }
  }

  return {
    width: widthWithSameColumnPills(grid.width, ribbons),
    height,
    columns: grid.columns,
    segmentCards,
    deviceCards,
    endpointCards,
    ribbons,
  };
}

/**
 * Computes a left-to-right column layout for the "All VLANs" overview mode.
 */
export function computeAllVlansLayout(
  flow: VLANFlow,
  devicesById?: Map<string, Device>,
  topology?: TopologyGraph | null,
): AllVlansFlowLayout {
  const vlans = [...flow.vlans].sort((a, b) => a.id - b.id);
  const participatingSet = new Set<string>();
  const gatewayDeviceSet = new Set<string>();
  const hopAdj = new Map<string, Set<string>>();
  const devVlans = new Map<string, Set<number>>();

  const trackDevVlan = (devId: string, vid: number) => {
    participatingSet.add(devId);
    addSet(devVlans, devId, vid);
  };

  for (const v of vlans) {
    for (const g of v.gateways) {
      gatewayDeviceSet.add(g.device_id);
      trackDevVlan(g.device_id, v.id);
    }
    for (const d of v.devices) trackDevVlan(d, v.id);
    for (const h of v.hops) {
      trackDevVlan(h.from_id, v.id);
      trackDevVlan(h.to_id, v.id);
      addAdj(hopAdj, h.from_id, h.to_id);
    }
    for (const ap of v.access_ports) trackDevVlan(ap.device_id, v.id);
    for (const s of v.ssids) trackDevVlan(s.device_id, v.id);
  }

  const endpointEntriesByDev = new Map<string, AllVlansEndpointEntry[]>();
  for (const v of vlans) {
    const hasGateway = v.gateways.length > 0;
    const vLeaks = flow.findings.filter((f) => f.kind === 'leak' && f.vlans.includes(v.id));
    const devsHere = new Set<string>([...v.access_ports.map((ap) => ap.device_id), ...v.ssids.map((s) => s.device_id)]);
    for (const devId of devsHere) {
      const aps = v.access_ports.filter((ap) => ap.device_id === devId);
      const ssids = v.ssids.filter((s) => s.device_id === devId);
      pushMap(endpointEntriesByDev, devId, {
        vlanId: v.id,
        accessCount: aps.length,
        unreachableAccessCount: hasGateway ? aps.filter((ap) => !ap.reachable).length : 0,
        ssids: ssids.map((s) => ({ name: s.name, reachable: !hasGateway || s.reachable })),
        hasLeak: vLeaks.some((f) => f.device_id === devId || f.peer_id === devId),
      });
    }
  }

  const hasEndpointCol = endpointEntriesByDev.size > 0;
  const grid = buildFlowGrid({
    participating: Array.from(participatingSet),
    gatewayDeviceIds: Array.from(gatewayDeviceSet),
    hopAdj,
    topology,
    devicesById,
    leadColumn: { kind: 'vlans', label: '1. ALL VLANS & SEGMENTS' },
    hasEndpointCol,
    hasGateways: gatewayDeviceSet.size > 0,
    firstTierCompare: (a, b) => displayName(devicesById, a).localeCompare(displayName(devicesById, b)),
    minEmptyColW: SEGMENT_COL_W,
  });

  const vlanCards: AllVlansVlanCard[] = vlans.map((v) => ({
    id: `vlan:${v.id}`,
    vlan: v,
    findings: flow.findings.filter((f) => f.vlans.includes(v.id)),
    x: grid.columns[0].x,
    y: 0,
    width: grid.columns[0].width,
    height: 78,
  }));
  const vlanCardById = new Map(vlanCards.map((c) => [c.vlan.id, c]));

  const { byId: deviceCardsById, byCol: deviceCardsByCol } = buildDeviceColumnCards<AllVlansDeviceCard>(
    grid,
    (devId, d, colIdx, col) => {
      const vIds = Array.from(devVlans.get(devId) ?? []).sort((a, b) => a - b);
      return {
        id: `dev:${devId}`,
        deviceId: devId,
        hopDistance: d,
        columnIndex: colIdx,
        vlanIds: vIds,
        findings: flow.findings.filter((f) => f.device_id === devId || f.peer_id === devId),
        x: col.x,
        y: 0,
        width: col.width,
        height: Math.max(106, 68 + Math.min(vIds.length, 10) * 12),
      };
    },
  );

  const endpointCards: AllVlansEndpointCard[] = hasEndpointCol
    ? Array.from(endpointEntriesByDev.entries(), ([devId, entries]) => ({
        id: `all-ep:${devId}`,
        deviceId: devId,
        entries,
        hasUnreachable: entries.some((e) => e.unreachableAccessCount > 0 || e.ssids.some((s) => !s.reachable)),
        hasLeak: entries.some((e) => e.hasLeak),
        x: grid.columns[grid.endpointColIdx].x,
        y: 0,
        width: grid.columns[grid.endpointColIdx].width,
        height: Math.max(82, 46 + Math.min(entries.length, 5) * 18 + (entries.length > 5 ? 16 : 0)),
      }))
    : [];

  const height = stackFlowColumns(
    vlanCards,
    deviceCardsByCol,
    endpointCards,
    340,
    240,
    (a, b) => (deviceCardsById.get(a.deviceId)?.y ?? 0) - (deviceCardsById.get(b.deviceId)?.y ?? 0),
  );

  const segmentLinks: AllVlansSegmentLink[] = [];
  const segLinksByDev = new Map<string, AllVlansSegmentLink[]>();
  for (const vc of vlanCards) {
    const l2Anchor = vc.vlan.gateways.length === 0
      ? vc.vlan.devices
          .map((id) => deviceCardsById.get(id))
          .filter((dc): dc is AllVlansDeviceCard => dc != null)
          .sort((a, b) => a.columnIndex - b.columnIndex || a.y - b.y)[0]?.deviceId
      : undefined;
    const targets = vc.vlan.gateways.length > 0
      ? Array.from(new Set(vc.vlan.gateways.map((g) => g.device_id)))
      : l2Anchor ? [l2Anchor] : [];
    for (const devId of targets) {
      const dc = deviceCardsById.get(devId);
      if (!dc) continue;
      const link: AllVlansSegmentLink = {
        vlanId: vc.vlan.id,
        deviceId: devId,
        x1: vc.x + vc.width,
        y1: vc.y + vc.height / 2,
        x2: dc.x,
        y2: dc.y + dc.height / 2,
      };
      segmentLinks.push(link);
      pushMap(segLinksByDev, devId, link);
    }
  }
  for (const [devId, links] of segLinksByDev.entries()) {
    const dc = deviceCardsById.get(devId);
    if (!dc) continue;
    links.sort((a, b) => (vlanCardById.get(a.vlanId)?.y ?? 0) - (vlanCardById.get(b.vlanId)?.y ?? 0));
    const span = Math.max(0, dc.height - 58);
    links.forEach((l, i) => (l.y2 = railY(dc.y + 42, span, i, links.length)));
  }

  const bundleMap = new Map<string, AllVlansEdgeBundle>();
  for (const v of vlans) {
    for (const h of v.hops) {
      const cFrom = deviceCardsById.get(h.from_id);
      const cTo = deviceCardsById.get(h.to_id);
      if (!cFrom || !cTo) continue;
      const { sameCol, swap, leftCard, rightCard, x1, x2 } = orientEdge(cFrom, cTo);
      let bundle = bundleMap.get(h.edge_id);
      if (!bundle) {
        bundle = {
          edgeId: h.edge_id,
          fromDeviceId: leftCard.deviceId,
          toDeviceId: rightCard.deviceId,
          linkKind: h.kind,
          sameColumn: sameCol,
          x1,
          y1: leftCard.y + leftCard.height / 2,
          x2,
          y2: rightCard.y + rightCard.height / 2,
          maxSpread: 64,
          lanes: [],
          findings: flow.findings.filter((f) => f.edge_id === h.edge_id),
        };
        bundleMap.set(h.edge_id, bundle);
      }
      bundle.lanes.push({
        vlanId: v.id,
        state: h.state,
        fromMode: swap ? h.to_mode : h.from_mode,
        toMode: swap ? h.from_mode : h.to_mode,
        finding: bundle.findings.find((f) => f.vlans.includes(v.id)),
      });
    }
  }

  const edgeBundles = Array.from(bundleMap.values());
  const outByDev = new Map<string, AllVlansEdgeBundle[]>();
  for (const b of edgeBundles) pushMap(outByDev, b.fromDeviceId, b);
  for (const [devId, bundles] of outByDev.entries()) {
    const dc = deviceCardsById.get(devId);
    if (!dc || bundles.length <= 1) continue;
    bundles.sort((a, b) => a.y2 - b.y2);
    const span = Math.max(0, dc.height - 62);
    bundles.forEach((b, i) => (b.y1 = railY(dc.y + 44, span, i, bundles.length)));
  }
  const halfRoom = (dc: AllVlansDeviceCard, y: number) =>
    Math.max(0, Math.min(y - dc.y, dc.y + dc.height - y) - 10);
  for (const b of edgeBundles) {
    const cFrom = deviceCardsById.get(b.fromDeviceId);
    const cTo = deviceCardsById.get(b.toDeviceId);
    if (cFrom && cTo) {
      b.maxSpread = Math.min(halfRoom(cFrom, b.y1), halfRoom(cTo, b.y2)) * 2;
    }
  }

  const endpointLinks: AllVlansEndpointLink[] = [];
  for (const ep of endpointCards) {
    const dc = deviceCardsById.get(ep.deviceId);
    if (!dc) continue;
    endpointLinks.push({
      deviceId: ep.deviceId,
      x1: dc.x + dc.width,
      y1: dc.y + dc.height / 2,
      x2: ep.x,
      y2: ep.y + ep.height / 2,
      hasLeak: ep.hasLeak,
      hasUnreachable: ep.hasUnreachable,
    });
  }

  return {
    width: widthWithSameColumnPills(grid.width, edgeBundles),
    height,
    columns: grid.columns,
    vlanCards,
    deviceCards: Array.from(deviceCardsById.values()),
    endpointCards,
    segmentLinks,
    edgeBundles,
    endpointLinks,
  };
}
