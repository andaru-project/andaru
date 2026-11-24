// SPDX-License-Identifier: AGPL-3.0-or-later
import type { KeyboardEvent } from 'react';
import type {
  Client,
  Device,
  Link,
  NetworkState,
  TopologyEdge,
  TopologyGraph,
  VLANFlow,
  VLANFlowFinding,
  VLANFlowHop,
  VLANFlowVLAN,
  VLANMode,
} from './api';

export interface SvgPalette {
  isDark: boolean;
  canvas: string;
  surface: string;
  subtle: string;
  border: string;
  borderSoft: string;
  text: string;
  dim: string;
  teal: string;
  blue: string;
  violet: string;
  green: string;
  amber: string;
  amberInk: string;
  red: string;
  gray: string;
}

export function svgPalette(isDark: boolean): SvgPalette {
  return {
    isDark,
    canvas: isDark ? '#121417' : '#fbfcfd',
    surface: isDark ? '#1a1b1e' : '#ffffff',
    subtle: isDark ? '#25262b' : '#f1f3f5',
    border: isDark ? '#373a40' : '#ced4da',
    borderSoft: isDark ? '#2c2e33' : '#e9ecef',
    text: isDark ? '#e9ecef' : '#17212c',
    dim: isDark ? '#909296' : '#526171',
    teal: isDark ? '#20c997' : '#0ca678',
    blue: isDark ? '#4dabf7' : '#1971c2',
    violet: isDark ? '#9775fa' : '#7048e8',
    green: isDark ? '#40c057' : '#2b8a3e',
    amber: isDark ? '#fab005' : '#e67700',
    amberInk: '#17212c',
    red: isDark ? '#ff6b6b' : '#c92a2a',
    gray: isDark ? '#787b82' : '#868e96',
  };
}

export function onActivate(fn: () => void) {
  return (e: KeyboardEvent) => {
    // Keys pressed on a nested link or button belong to that control.
    if (e.target !== e.currentTarget) return;
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      fn();
    }
  };
}

export interface ColorPair {
  light: string;
  dark: string;
}

const VLAN_PALETTE: ColorPair[] = [
  { light: '#0ca678', dark: '#20c997' }, // Teal
  { light: '#e8590c', dark: '#ff922b' }, // Orange
  { light: '#7048e8', dark: '#9775fa' }, // Violet
  { light: '#1c7ed6', dark: '#4dabf7' }, // Blue
  { light: '#4c6ef5', dark: '#748ffc' }, // Indigo
  { light: '#0c8599', dark: '#22b8cf' }, // Cyan
  { light: '#ae3ec9', dark: '#da77f2' }, // Grape
  { light: '#2b8a3e', dark: '#40c057' }, // Green
  { light: '#d6336c', dark: '#f06595' }, // Pink
  { light: '#5c940d', dark: '#94d82d' }, // Lime
];

export function buildVlanColorMap(vlans: VLANFlowVLAN[]): Map<number, ColorPair> {
  const map = new Map<number, ColorPair>();
  vlans.forEach((v, idx) => {
    map.set(v.id, VLAN_PALETTE[idx % VLAN_PALETTE.length]);
  });
  return map;
}

export function getVlanColor(
  vid: number,
  colorMap: Map<number, ColorPair>,
  isDark: boolean,
): string {
  const pair = colorMap.get(vid) ?? VLAN_PALETTE[Math.abs(vid) % VLAN_PALETTE.length];
  return isDark ? pair.dark : pair.light;
}

export function findingKey(f: VLANFlowFinding): string {
  return `${f.kind}:${f.edge_id}:${f.vlans.join(',')}`;
}

export function deviceName(devicesById: Map<string, Device>, id: string): string {
  return devicesById.get(id)?.name || id;
}

export function portLabel(devicesById: Map<string, Device>, deviceId: string, link?: string | null): string {
  const name = deviceName(devicesById, deviceId);
  return link ? `${name}:${link}` : name;
}

/** "10 · LAN" for chips; with long, "VLAN 10 · LAN" for headings and selectors. */
export function vlanLabel(vlansById: Map<number, VLANFlowVLAN>, vid: number, long = false): string {
  const v = vlansById.get(vid);
  if (!v?.name) return `VLAN ${vid}`;
  return long ? `VLAN ${vid} · ${v.name}` : `${vid} · ${v.name}`;
}

export type VlanHealth = 'leak' | 'dead_end' | 'mismatch' | 'unverified' | 'ok';

export function vlanHealth(vlan: VLANFlowVLAN, findings: VLANFlowFinding[]): VlanHealth {
  const vf = findings.filter((f) => f.vlans.includes(vlan.id));
  if (vf.some((f) => f.kind === 'leak')) return 'leak';
  if (vf.some((f) => f.kind === 'dead_end')) return 'dead_end';
  if (vf.some((f) => f.kind === 'tag_mismatch')) return 'mismatch';
  if (vlan.hops.some((h) => h.state === 'unverified')) return 'unverified';
  return 'ok';
}

export function modeShort(mode: VLANMode): string {
  switch (mode) {
    case 'tagged':
      return 'T';
    case 'untagged':
      return 'U';
    case 'none':
      return '✕';
    case 'unknown':
      return '?';
  }
}

function portMatchesFinding(
  f: VLANFlowFinding,
  deviceId: string,
  ifName: string,
  lagGroupId?: string,
): boolean {
  const match = (dev: string, link: string) =>
    dev === deviceId && (link === ifName || (!!lagGroupId && link === lagGroupId));
  return match(f.device_id, f.link) || match(f.peer_id, f.peer_link);
}

export function findingsForPort(
  findings: VLANFlowFinding[],
  deviceId: string,
  ifName: string,
  lagGroupId?: string,
): VLANFlowFinding[] {
  return findings.filter((f) => portMatchesFinding(f, deviceId, ifName, lagGroupId));
}

export function findingsForDevice(findings: VLANFlowFinding[], deviceId: string): VLANFlowFinding[] {
  return findings.filter((f) => f.device_id === deviceId || f.peer_id === deviceId);
}

export function isStpBlocked(stpState?: string): boolean {
  return stpState === 'blocking' || stpState === 'discarding';
}

export function isEdgeStpBlocked(
  state: NetworkState,
  edge: Pick<TopologyEdge, 'from_id' | 'from_link' | 'to_id' | 'to_link'>,
): boolean {
  const fromLink = edge.from_link
    ? state.links.find((l) => l.device_id === edge.from_id && l.if_name === edge.from_link)
    : undefined;
  const toLink = edge.to_link
    ? state.links.find((l) => l.device_id === edge.to_id && l.if_name === edge.to_link)
    : undefined;
  return isStpBlocked(fromLink?.stp_state) || isStpBlocked(toLink?.stp_state);
}

export function findPeerForLink(
  topology: TopologyGraph | null,
  devicesById: Map<string, Device>,
  deviceId: string,
  ifName: string,
  lagGroupId?: string,
): { edge: TopologyEdge; peerDevice: Device | undefined; peerId: string; peerLink: string } | null {
  if (!topology) return null;
  for (const e of topology.edges) {
    if (e.from_id === deviceId && (e.from_link === ifName || (!!lagGroupId && e.from_link === lagGroupId))) {
      return { edge: e, peerDevice: devicesById.get(e.to_id), peerId: e.to_id, peerLink: e.to_link ?? '' };
    }
    if (e.to_id === deviceId && (e.to_link === ifName || (!!lagGroupId && e.to_link === lagGroupId))) {
      return { edge: e, peerDevice: devicesById.get(e.from_id), peerId: e.from_id, peerLink: e.from_link ?? '' };
    }
  }
  return null;
}

export function findWirelessSignal(
  state: NetworkState,
  fromId: string,
  toId: string,
): { signal_dbm: number; band?: string } | null {
  for (const wu of state.wireless_uplinks) {
    if (
      (wu.device_id === fromId && wu.peer_device_id === toId) ||
      (wu.device_id === toId && wu.peer_device_id === fromId)
    ) {
      return { signal_dbm: wu.signal_dbm, band: wu.band };
    }
  }
  return null;
}

export function findHopForFinding(flow: VLANFlow, f: VLANFlowFinding): VLANFlowHop | null {
  const vl = flow.vlans.find((v) => v.id === f.vlans[0]);
  return vl?.hops.find((h) => h.edge_id === f.edge_id) ?? null;
}

export interface FindingPresentation {
  title: string;
  shortLabel: string;
  consequence: string;
  affected: string[];
  remediation: string;
}

/**
 * Formats an API-returned VLANFlowFinding for display in the inspector and alerts.
 * Uses only fields returned by /api/v1/vlans/flow and /api/v1/state.
 */
export function describeFinding(
  f: VLANFlowFinding,
  flow: VLANFlow,
  state: NetworkState | null,
  devicesById: Map<string, Device>,
): FindingPresentation {
  const vlansById = new Map(flow.vlans.map((v) => [v.id, v]));
  const portA = portLabel(devicesById, f.device_id, f.link);
  const portB = portLabel(devicesById, f.peer_id, f.peer_link);

  if (f.kind === 'leak') {
    const v0 = f.vlans[0];
    const v1 = f.vlans[1];
    const vlan0 = vlansById.get(v0);
    const vlan1 = vlansById.get(v1);
    const z0 = vlan0?.gateways[0]?.zone_name || vlan0?.gateways[0]?.zone_id;
    const z1 = vlan1?.gateways[0]?.zone_name || vlan1?.gateways[0]?.zone_id;
    const zoneNote = z0 && z1 && z0 !== z1
      ? `Frames cross between security zone "${z0}" and zone "${z1}" at Layer 2 without traversing L3 firewall policy.`
      : `Untagged frames cross between VLAN ${v0} and VLAN ${v1} at Layer 2 across this link.`;

    const affected: string[] = [];
    for (const devId of [f.device_id, f.peer_id]) {
      const dName = deviceName(devicesById, devId);
      const cls = (state?.clients ?? []).filter(
        (c) => c.online && c.attached_device_id === devId && c.vlan != null && f.vlans.includes(c.vlan),
      );
      if (cls.length > 0) {
        const sample = cls.slice(0, 4).map((c) => c.hostname || c.mac).join(', ');
        affected.push(`${dName}: ${cls.length} online client(s) (${sample}${cls.length > 4 ? ', …' : ''})`);
      } else {
        affected.push(`${dName} (VLANs ${f.vlans.join(', ')})`);
      }
    }

    return {
      title: `Cross-VLAN Leak: PVID ${v0} (${vlan0?.name || `VLAN ${v0}`}) ⇄ PVID ${v1} (${vlan1?.name || `VLAN ${v1}`})`,
      shortLabel: `LEAK · PVID ${v0} ⇄ ${v1}`,
      consequence: zoneNote,
      affected,
      remediation: `Configure the same native PVID on both ${portA} and ${portB}, and tag VLANs [${f.vlans.join(', ')}] symmetrically on both ends.`,
    };
  }

  if (f.kind === 'tag_mismatch') {
    const v0 = f.vlans[0];
    const vlan0 = vlansById.get(v0);
    const affected = [f.device_id, f.peer_id].map((id) => `${deviceName(devicesById, id)} (${vlanLabel(vlansById, v0)})`);
    return {
      title: `VLAN ${v0}${vlan0?.name ? ` (${vlan0.name})` : ''} Tag Mismatch on ${portA} ↔ ${portB}`,
      shortLabel: `MISMATCH · VLAN ${v0}`,
      consequence: `One end carries VLAN ${v0} tagged while the peer carries it untagged, causing frames to be dropped or misclassified.`,
      affected,
      remediation: `Align VLAN ${v0} on ${portA} and ${portB} so both ends carry it tagged (T) or both untagged (U).`,
    };
  }

  // dead_end
  const v0 = f.vlans[0];
  const vlan0 = vlansById.get(v0);
  const gwAddr = vlan0?.gateways[0]?.addresses[0];
  const gwText = gwAddr ? `${gwAddr.address}/${gwAddr.prefix_length}` : 'its L3 gateway';

  const affected: string[] = [];
  for (const sDevId of f.stranded) {
    const sName = deviceName(devicesById, sDevId);
    const ports = (vlan0?.access_ports ?? []).filter((ap) => ap.device_id === sDevId && !ap.reachable);
    const ssids = (vlan0?.ssids ?? []).filter((s) => s.device_id === sDevId && !s.reachable);
    const cls = (state?.clients ?? []).filter((c) => c.attached_device_id === sDevId && c.vlan === v0);
    if (ports.length > 0) {
      affected.push(`${sName}: access port(s) ${ports.map((p) => p.link).join(', ')}`);
    }
    if (ssids.length > 0) {
      affected.push(`${sName}: SSID(s) ${ssids.map((s) => `"${s.name}"`).join(', ')}`);
    }
    if (cls.length > 0) {
      const names = cls.slice(0, 4).map((c) => c.hostname || c.mac).join(', ');
      affected.push(`${sName}: ${cls.length} client(s) (${names}${cls.length > 4 ? ', …' : ''})`);
    }
    if (ports.length === 0 && ssids.length === 0 && cls.length === 0) {
      affected.push(`${sName} (configured for VLAN ${v0})`);
    }
  }

  const consequence = f.stranded.length > 0
    ? `${f.stranded.length} device(s) (${f.stranded.map((id) => deviceName(devicesById, id)).join(', ')}) cannot reach ${gwText} through this link.`
    : f.message;

  return {
    title: `VLAN ${v0}${vlan0?.name ? ` (${vlan0.name})` : ''} Dead-End: missing on ${portB}`,
    shortLabel: `DEAD-END · ${portB}`,
    consequence,
    affected,
    remediation: `Add VLAN ${v0} on ${portB} (matching ${portA}), or remove unused VLAN ${v0} configuration from ${portA}.`,
  };
}

/**
 * Checks whether the API marks a client's attached port, SSID, or device as unreachable
 * on its VLAN (strictly reading reachable/reached fields from /api/v1/vlans/flow).
 */
export function getClientVlanIssue(
  client: Client,
  flow: VLANFlow | null,
): { kind: 'unreachable'; vlanId: number } | null {
  if (!flow || client.vlan == null || !client.attached_device_id) return null;
  const vl = flow.vlans.find((v) => v.id === client.vlan);
  if (!vl || vl.gateways.length === 0) return null;
  if (client.attached_link) {
    const ap = vl.access_ports.find((p) => p.device_id === client.attached_device_id && p.link === client.attached_link);
    if (ap && !ap.reachable) return { kind: 'unreachable', vlanId: vl.id };
  }
  if (client.ssid) {
    const s = vl.ssids.find((x) => x.device_id === client.attached_device_id && x.name === client.ssid);
    if (s && !s.reachable) return { kind: 'unreachable', vlanId: vl.id };
  }
  if (vl.devices.includes(client.attached_device_id) && !vl.reached.includes(client.attached_device_id)) {
    return { kind: 'unreachable', vlanId: vl.id };
  }
  return null;
}

export function getSfpAlerts(links: Link[]): Link[] {
  return links.filter((l) => l.sfp?.present && (l.sfp.alert_state === 'warn' || l.sfp.alert_state === 'crit'));
}
