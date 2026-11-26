import { parseIpv6Groups } from './dhcpEdit';
import { firewallZone, type AddressAlias, type LinkStatus, type PortAlias, type PortForward, type RuleSide, type Segment, type Snapshot } from './types';

export type TrafficProtocol = 'tcp' | 'udp' | 'icmp' | 'icmpv6';
export type StepStage = 'ingress' | 'prerouting' | 'routing' | 'filter' | 'postrouting' | 'verdict';
export type StepOutcome = 'pass' | 'transform' | 'accept' | 'drop' | 'reject' | 'info' | 'warn';
export type FlowVerdict = 'accept' | 'drop' | 'reject' | 'no_route';

export interface TrafficProbe {
  ingressSegmentId: string;
  protocol: TrafficProtocol;
  srcIp: string;
  srcPort?: number;
  dstIp: string;
  dstPort?: number;
  icmpType?: string;
}

export interface DecidingRef {
  label: string;
  to: string;
}

export interface FlowStep {
  id: string;
  stage: StepStage;
  title: string;
  summary: string;
  outcome: StepOutcome;
  decidedBy?: DecidingRef;
  zone?: { name: string; color?: string };
  detail?: string;
}

export interface TrafficSimulation {
  verdict: FlowVerdict;
  verdictLabel: string;
  chain: 'input' | 'forward' | 'none';
  originalTuple: string;
  finalTuple: string;
  decidedBy?: DecidingRef;
  warnings: string[];
  steps: FlowStep[];
}

export interface ParsedIp {
  family: 4 | 6;
  value: bigint;
  canonical: string;
}

export interface ParsedPrefix {
  family: 4 | 6;
  addr: bigint;
  network: bigint;
  bits: number;
  raw: string;
}

const ipv4Max = (1n << 32n) - 1n;
const ipv6Max = (1n << 128n) - 1n;

export function parseIp(raw: string): ParsedIp | null {
  const s = raw.trim();
  if (!s || s.includes('/') || s.includes('%')) return null;
  if (!s.includes(':')) {
    const parts = s.split('.');
    if (parts.length !== 4) return null;
    let value = 0n;
    for (const part of parts) {
      if (!/^(?:0|[1-9]\d{0,2})$/.test(part)) return null;
      const n = Number(part);
      if (n > 255) return null;
      value = (value << 8n) | BigInt(n);
    }
    return { family: 4, value, canonical: s };
  }
  const groups = parseIpv6Groups(s);
  if (!groups) return null;
  if (groups.slice(0, 5).every((g) => g === '0') && groups[5] === 'ffff') return null;
  let value = 0n;
  for (const g of groups) {
    value = (value << 16n) | BigInt(parseInt(g, 16));
  }
  return { family: 6, value, canonical: formatIpv6(groups) };
}

function formatIpv6(groups: string[]): string {
  // RFC 5952 longest run of >= 2 zero groups compressed to ::.
  let bestStart = -1;
  let bestLen = 0;
  for (let i = 0; i < 8; ) {
    if (groups[i] !== '0') { i += 1; continue; }
    let j = i;
    while (j < 8 && groups[j] === '0') j += 1;
    if (j - i > bestLen && j - i >= 2) {
      bestStart = i;
      bestLen = j - i;
    }
    i = j;
  }
  if (bestStart < 0) return groups.join(':');
  const left = groups.slice(0, bestStart).join(':');
  const right = groups.slice(bestStart + bestLen).join(':');
  return `${left}::${right}`;
}

export function formatIp(family: 4 | 6, value: bigint): string {
  if (family === 4) {
    return [
      Number((value >> 24n) & 0xffn),
      Number((value >> 16n) & 0xffn),
      Number((value >> 8n) & 0xffn),
      Number(value & 0xffn),
    ].join('.');
  }
  const groups: string[] = [];
  for (let shift = 112n; shift >= 0n; shift -= 16n) {
    groups.push(Number((value >> shift) & 0xffffn).toString(16));
  }
  return formatIpv6(groups);
}

export function parsePrefix(raw: string): ParsedPrefix | null {
  const s = raw.trim();
  const slash = s.indexOf('/');
  if (slash < 0) {
    const ip = parseIp(s);
    if (!ip) return null;
    return { family: ip.family, addr: ip.value, network: ip.value, bits: ip.family === 4 ? 32 : 128, raw: s };
  }
  const ip = parseIp(s.slice(0, slash));
  const lenStr = s.slice(slash + 1);
  if (!ip || !/^(?:0|[1-9]\d{0,2})$/.test(lenStr)) return null;
  const bits = Number(lenStr);
  const maxBits = ip.family === 4 ? 32 : 128;
  if (bits > maxBits) return null;
  const hostBits = BigInt(maxBits - bits);
  const full = ip.family === 4 ? ipv4Max : ipv6Max;
  const mask = bits === 0 ? 0n : (full << hostBits) & full;
  return { family: ip.family, addr: ip.value, network: ip.value & mask, bits, raw: s };
}

function prefixContains(prefix: ParsedPrefix, ip: ParsedIp): boolean {
  if (prefix.family !== ip.family) return false;
  const maxBits = prefix.family === 4 ? 32 : 128;
  const hostBits = BigInt(maxBits - prefix.bits);
  const full = prefix.family === 4 ? ipv4Max : ipv6Max;
  const mask = prefix.bits === 0 ? 0n : (full << hostBits) & full;
  return (ip.value & mask) === prefix.network;
}

function prefixOverlaps(a: ParsedPrefix, b: ParsedPrefix): boolean {
  if (a.family !== b.family) return false;
  const shorter = a.bits <= b.bits ? a : b;
  const longer = a.bits <= b.bits ? b : a;
  return prefixContains(shorter, { family: longer.family, value: longer.network, canonical: '' });
}

function isLinkLocalIpv6(ip: ParsedIp): boolean {
  return ip.family === 6 && ((ip.value >> 118n) & 0x3ffn) === 0x3fan; // fe80::/10
}

function isBroadcastOrMulticast(ip: ParsedIp, segmentPrefixes: ParsedPrefix[]): boolean {
  if (ip.family === 4) {
    if (ip.value === ipv4Max) return true; // 255.255.255.255
    if (((ip.value >> 24n) & 0xf0n) === 0xe0n) return true; // 224.0.0.0/4
    for (const p of segmentPrefixes) {
      if (p.family !== 4 || p.bits > 30) continue;
      const bcast = p.network | ((1n << BigInt(32 - p.bits)) - 1n);
      if (ip.value === bcast) return true;
    }
    return false;
  }
  return ((ip.value >> 120n) & 0xffn) === 0xffn; // ff00::/8
}

function isUplink(segment: Segment, linksById: Map<string, Snapshot['value']['links'][number]>): boolean {
  const link = linksById.get(segment.link);
  return Boolean(segment.addressing.ipv4.gateway) || segment.addressing.ipv4.mode === 'dhcp' || link?.kind === 'pppoe';
}

function isIpv6Uplink(segment: Segment): boolean {
  return Boolean(segment.addressing.ipv6.gateway) || segment.addressing.ipv6.mode === 'slaac' || segment.addressing.ipv6.mode === 'dhcp6';
}

function aliasHasFamily(aliasId: string, family: 4 | 6, byId: Map<string, Partial<AddressAlias> & { id: string; name: string }>, seen = new Set<string>()): boolean {
  if (seen.has(aliasId)) return false;
  seen.add(aliasId);
  const alias = byId.get(aliasId);
  if (!alias) return false;
  switch (alias.kind) {
    case 'host':
      return parseIp(alias.address ?? '')?.family === family;
    case 'network':
      return parsePrefix(alias.prefix ?? '')?.family === family;
    case 'range':
      return parseIp(alias.first ?? '')?.family === family;
    case 'group':
      return (alias.members ?? []).some((m) => aliasHasFamily(m, family, byId, seen));
    default:
      return false;
  }
}

function matchesAddressAlias(aliasId: string, ip: ParsedIp, byId: Map<string, Partial<AddressAlias> & { id: string; name: string }>, seen = new Set<string>()): boolean {
  if (seen.has(aliasId)) return false;
  seen.add(aliasId);
  const alias = byId.get(aliasId);
  if (!alias) return false;
  switch (alias.kind) {
    case 'host': {
      const host = parseIp(alias.address ?? '');
      return host !== null && host.family === ip.family && host.value === ip.value;
    }
    case 'network': {
      const prefix = parsePrefix(alias.prefix ?? '');
      return prefix !== null && prefixContains(prefix, ip);
    }
    case 'range': {
      const first = parseIp(alias.first ?? '');
      const last = parseIp(alias.last ?? '');
      return first !== null && last !== null && first.family === ip.family && last.family === ip.family && ip.value >= first.value && ip.value <= last.value;
    }
    case 'group':
      return (alias.members ?? []).some((m) => matchesAddressAlias(m, ip, byId, seen));
    default:
      return false;
  }
}

function matchesAddressList(
  refs: string[],
  negated: boolean,
  ip: ParsedIp,
  byId: Map<string, Partial<AddressAlias> & { id: string; name: string }>,
): boolean {
  if (refs.length === 0) return true;
  if (negated) {
    // Matches nft.go:310-323: only aliases with elements in this family constrain it; if none do, all of this family matches.
    const sameFamily = refs.filter((id) => aliasHasFamily(id, ip.family, byId));
    if (sameFamily.length === 0) return true;
    return sameFamily.every((id) => !matchesAddressAlias(id, ip, byId));
  }
  return refs.some((id) => matchesAddressAlias(id, ip, byId));
}

function parsePortSpan(span: string): [number, number] | null {
  const trimmed = span.trim();
  const dash = trimmed.indexOf('-');
  if (dash < 0) {
    if (!/^\d+$/.test(trimmed)) return null;
    const p = Number(trimmed);
    return p >= 1 && p <= 65535 ? [p, p] : null;
  }
  const loStr = trimmed.slice(0, dash);
  const hiStr = trimmed.slice(dash + 1);
  if (!/^\d+$/.test(loStr) || !/^\d+$/.test(hiStr)) return null;
  const lo = Number(loStr);
  const hi = Number(hiStr);
  return lo >= 1 && hi <= 65535 && lo <= hi ? [lo, hi] : null;
}

function matchesPortAlias(
  aliasId: string,
  protocol: TrafficProtocol,
  dstPort: number | undefined,
  icmpType: string | undefined,
  byId: Map<string, Partial<PortAlias> & { id: string; name: string }>,
  seen = new Set<string>(),
): boolean {
  if (seen.has(aliasId)) return false;
  seen.add(aliasId);
  const alias = byId.get(aliasId);
  if (!alias) return false;
  if (alias.protocol === 'group') {
    return (alias.members ?? []).some((m) => matchesPortAlias(m, protocol, dstPort, icmpType, byId, seen));
  }
  if (alias.protocol !== protocol) return false;
  if (protocol === 'tcp' || protocol === 'udp') {
    if (dstPort === undefined) return false;
    return (alias.ports ?? []).some((entry) => {
      const span = parsePortSpan(entry);
      return span !== null && dstPort >= span[0] && dstPort <= span[1];
    });
  }
  const types = alias.types ?? [];
  if (types.length === 0) return true;
  if (!icmpType) return false;
  return types.includes(icmpType.trim().toLowerCase());
}

function formatEndpoint(ip: ParsedIp, port?: number): string {
  if (port === undefined) return ip.canonical;
  return ip.family === 6 ? `[${ip.canonical}]:${port}` : `${ip.canonical}:${port}`;
}

function formatTuple(proto: TrafficProtocol, src: ParsedIp, srcPort: number | undefined, dst: ParsedIp, dstPort: number | undefined, icmpType?: string): string {
  const base = `${proto.toUpperCase()} ${formatEndpoint(src, srcPort)} → ${formatEndpoint(dst, dstPort)}`;
  return (proto === 'icmp' || proto === 'icmpv6') && icmpType ? `${base} (${icmpType})` : base;
}

function matchesRuleSide(
  side: RuleSide,
  zoneId: string,
  ip: ParsedIp,
  isInputTo: boolean,
  addressesById: Map<string, Partial<AddressAlias> & { id: string; name: string }>,
): boolean {
  if (!isInputTo && side.zones.length > 0 && !side.zones.includes(zoneId)) return false;
  return matchesAddressList(side.addresses, Boolean(side.not), ip, addressesById);
}

export function validateProbe(probe: TrafficProbe, snapshot: Snapshot['value']): Map<string, string> {
  const errors = new Map<string, string>();
  if (!probe.ingressSegmentId || !snapshot.segments.some((s) => s.id === probe.ingressSegmentId)) {
    errors.set('ingressSegmentId', 'Choose the interface/segment the packet enters on.');
  }
  const src = parseIp(probe.srcIp);
  const dst = parseIp(probe.dstIp);
  if (!src) errors.set('srcIp', 'Enter a valid bare IPv4 or IPv6 source address.');
  if (!dst) errors.set('dstIp', 'Enter a valid bare IPv4 or IPv6 destination address.');
  if (src && dst && src.family !== dst.family) {
    errors.set('dstIp', `Destination must be IPv${src.family} to match the source address.`);
  }
  if (probe.protocol === 'icmp' && ((src && src.family !== 4) || (dst && dst.family !== 4))) {
    errors.set('protocol', 'ICMP applies to IPv4 addresses; use ICMPv6 for IPv6.');
  }
  if (probe.protocol === 'icmpv6' && ((src && src.family !== 6) || (dst && dst.family !== 6))) {
    errors.set('protocol', 'ICMPv6 applies to IPv6 addresses; use ICMP for IPv4.');
  }
  if (probe.protocol === 'tcp' || probe.protocol === 'udp') {
    if (probe.srcPort !== undefined && (!Number.isInteger(probe.srcPort) || probe.srcPort < 1 || probe.srcPort > 65535)) {
      errors.set('srcPort', 'Port must be between 1 and 65535.');
    }
    if (probe.dstPort === undefined || !Number.isInteger(probe.dstPort) || probe.dstPort < 1 || probe.dstPort > 65535) {
      errors.set('dstPort', 'Enter a destination port from 1 to 65535.');
    }
  }
  return errors;
}

const ndpTypes = new Set(['nd-neighbor-solicit', 'nd-neighbor-advert', 'nd-router-solicit', 'nd-router-advert']);
const mldTypes = new Set(['mld-listener-query', 'mld-listener-report', 'mld-listener-done', 'mld2-listener-report']);

interface LocalOwner {
  label: string;
  to: string;
  segmentId?: string;
  isVirtual: boolean;
}

// Simulate a hypothetical packet through Andaru's pre-routing NAT, routing, firewall filter, and post-routing NAT.
export function simulateTrafficFlow(
  probe: TrafficProbe,
  snapshot: Snapshot['value'],
  liveLinks: LinkStatus[] = [],
): TrafficSimulation {
  const srcIp = parseIp(probe.srcIp)!;
  const origDstIp = parseIp(probe.dstIp)!;
  const icmpType = probe.icmpType?.trim().toLowerCase() || undefined;
  const srcPort = probe.protocol === 'tcp' || probe.protocol === 'udp' ? probe.srcPort : undefined;
  const origDstPort = probe.protocol === 'tcp' || probe.protocol === 'udp' ? probe.dstPort : undefined;

  const linksById = new Map(snapshot.links.map((l) => [l.id, l]));
  const zonesById = new Map(snapshot.zones.map((z) => [z.id, z]));
  const addressesById = new Map((snapshot.aliases?.addresses ?? []).map((a) => [a.id, a]));
  const portsById = new Map((snapshot.aliases?.ports ?? []).map((p) => [p.id, p]));
  const ingressSegment = snapshot.segments.find((s) => s.id === probe.ingressSegmentId)!;
  const ingressLink = linksById.get(ingressSegment.link);
  const ingressZone = zonesById.get(ingressSegment.zone);
  const ingressZoneInfo = ingressZone ? { name: ingressZone.name, color: ingressZone.color } : undefined;
  const ingressIsInternal = !isUplink(ingressSegment, linksById);

  const warnings: string[] = [];
  const steps: FlowStep[] = [];
  const originalTuple = formatTuple(probe.protocol, srcIp, srcPort, origDstIp, origDstPort, icmpType);

  if (ingressLink && !ingressLink.enabled) {
    warnings.push(`Ingress interface ${ingressLink.name} is disabled in this configuration.`);
  }
  const hasManagement = Boolean(snapshot.management?.segment);
  if (!hasManagement) {
    warnings.push('No management segment is configured: Andaru does not load firewall rules or NAT without one.');
  }

  // Gather all configured segment prefixes and firewall-owned addresses (static + virtual + live).
  const allSegmentPrefixes: Array<{ segment: Segment; prefix: ParsedPrefix; enabled: boolean }> = [];
  const localOwners: Array<{ ip: ParsedIp; owner: LocalOwner }> = [];
  const internalOwn4 = new Set<bigint>();
  const virtual4 = new Set<bigint>();

  for (const seg of snapshot.segments) {
    const enabled = linksById.get(seg.link)?.enabled !== false;
    const rawList = [...(seg.addressing.ipv4.addresses ?? []), ...(seg.addressing.ipv6.addresses ?? [])];
    for (const raw of rawList) {
      const p = parsePrefix(raw);
      if (!p) continue;
      allSegmentPrefixes.push({ segment: seg, prefix: p, enabled });
      const addrIp: ParsedIp = { family: p.family, value: p.addr, canonical: formatIp(p.family, p.addr) };
      localOwners.push({ ip: addrIp, owner: { label: `Interface ${seg.name}`, to: '/network/interfaces', segmentId: seg.id, isVirtual: false } });
      if (p.family === 4 && !isUplink(seg, linksById)) internalOwn4.add(p.addr);
    }
  }
  for (const vip of snapshot.virtualAddresses ?? []) {
    const ip = parseIp(vip.address);
    if (!ip) continue;
    localOwners.push({ ip, owner: { label: `Virtual IP ${vip.name}`, to: '/network/virtual-ips', isVirtual: true } });
    if (ip.family === 4) {
      virtual4.add(ip.value);
      internalOwn4.add(ip.value);
    }
  }
  // Live link addresses (e.g. DHCPv4/PPPoE/SLAAC addresses learned on the appliance) matched by link name.
  for (const live of liveLinks) {
    const seg = snapshot.segments.find((s) => linksById.get(s.link)?.name === live.name);
    if (!seg) continue;
    const enabled = linksById.get(seg.link)?.enabled !== false;
    for (const a of live.addresses) {
      if (a.scope === 'link') continue;
      const p = parsePrefix(`${a.address}/${a.prefixLength}`);
      if (!p) continue;
      if (!allSegmentPrefixes.some((e) => e.segment.id === seg.id && e.prefix.family === p.family && e.prefix.addr === p.addr && e.prefix.bits === p.bits)) {
        allSegmentPrefixes.push({ segment: seg, prefix: p, enabled });
      }
      if (!localOwners.some((o) => o.ip.family === p.family && o.ip.value === p.addr)) {
        localOwners.push({
          ip: { family: p.family, value: p.addr, canonical: formatIp(p.family, p.addr) },
          owner: { label: `Interface ${seg.name} (live)`, to: '/network/interfaces', segmentId: seg.id, isVirtual: false },
        });
      }
    }
  }

  const findLocalOwner = (ip: ParsedIp, onSegmentId?: string): LocalOwner | undefined => {
    if (onSegmentId) {
      const exact = localOwners.find((o) => o.ip.family === ip.family && o.ip.value === ip.value && (o.owner.segmentId === onSegmentId || o.owner.isVirtual));
      if (exact) return exact.owner;
    }
    return localOwners.find((o) => o.ip.family === ip.family && o.ip.value === ip.value)?.owner;
  };

  // Stage 1: Ingress
  steps.push({
    id: 'ingress',
    stage: 'ingress',
    title: 'Ingress',
    summary: `Enters on ${ingressSegment.name}${ingressLink && ingressLink.name !== ingressSegment.name ? ` (${ingressLink.name})` : ''} in zone ${ingressZone?.name ?? ingressSegment.zone}.`,
    outcome: ingressLink && !ingressLink.enabled ? 'warn' : 'pass',
    decidedBy: { label: `Interface ${ingressSegment.name}`, to: '/network/interfaces' },
    zone: ingressZoneInfo,
    detail: originalTuple,
  });

  // Stage 2: Pre-routing NAT (Port forwards, then 1:1 NAT DNAT)
  let effectiveDstIp = origDstIp;
  let effectiveDstPort = origDstPort;
  let matchedPortForward: { pf: PortForward; index: number; reflected: boolean; toSelf: boolean } | undefined;
  let dnatApplied = false;
  let reflectedNat = false;

  if (hasManagement) {
    const portForwards = snapshot.nat?.portForwards ?? [];
    for (let i = 0; i < portForwards.length; i += 1) {
      const pf = portForwards[i];
      if (!pf.enabled) continue;
      const pfFamily: 4 | 6 = pf.family === 'ipv6' ? 6 : 4;
      if (pfFamily !== origDstIp.family) continue;
      if (probe.protocol !== 'tcp' && probe.protocol !== 'udp') continue;
      if (pf.protocol !== 'tcp-udp' && pf.protocol !== probe.protocol) continue;
      const span = parsePortSpan(pf.ports);
      if (!span || origDstPort === undefined || origDstPort < span[0] || origDstPort > span[1]) continue;
      if (!matchesAddressList(pf.from, Boolean(pf.fromNegated), srcIp, addressesById)) continue;

      const normalIngress = pf.ingressZones.includes(ingressSegment.zone);
      const reflectionIngress = Boolean(pf.reflection) && !pf.noRedirect && pfFamily === 4 && ingressIsInternal;
      if (!normalIngress && !reflectionIngress) continue;

      const checkTo = (viaReflection: boolean): boolean => {
        if (pf.toNegated) {
          if (isBroadcastOrMulticast(origDstIp, allSegmentPrefixes.map((e) => e.prefix))) return false;
          if (pf.to.self) return !findLocalOwner(origDstIp, ingressSegment.id);
          return !matchesAddressAlias(pf.to.address, origDstIp, addressesById);
        }
        if (viaReflection) {
          if (pf.to.self) return Boolean(findLocalOwner(origDstIp)) && !internalOwn4.has(origDstIp.value);
          return matchesAddressAlias(pf.to.address, origDstIp, addressesById) && !virtual4.has(origDstIp.value);
        }
        if (pf.to.self) return Boolean(findLocalOwner(origDstIp, ingressSegment.id));
        return matchesAddressAlias(pf.to.address, origDstIp, addressesById);
      };

      let usedReflection = false;
      if (normalIngress && checkTo(false)) {
        usedReflection = false;
      } else if (reflectionIngress && checkTo(true)) {
        usedReflection = true;
      } else {
        continue;
      }

      const ref: DecidingRef = { label: `Port forward #${i + 1} (${pf.name})`, to: '/firewall/nat' };
      if (pf.noRedirect) {
        steps.push({
          id: 'prerouting',
          stage: 'prerouting',
          title: 'Pre-routing NAT',
          summary: `Matched no-redirect exception "${pf.name}"; destination address and port are unchanged.`,
          outcome: 'pass',
          decidedBy: ref,
        });
        dnatApplied = true; // Stop further port-forward matching, matching nft `accept` in prerouting.
        break;
      }

      const newPort = pf.targetPort !== undefined ? pf.targetPort + (origDstPort - span[0]) : origDstPort;
      if (pf.target?.self) {
        effectiveDstPort = newPort;
        matchedPortForward = { pf, index: i, reflected: usedReflection, toSelf: true };
        dnatApplied = true;
        reflectedNat = usedReflection;
        steps.push({
          id: 'prerouting',
          stage: 'prerouting',
          title: 'Pre-routing NAT',
          summary: `${usedReflection ? 'Reflected and redirected' : 'Redirected'} to this firewall${newPort !== origDstPort ? ` on port ${newPort}` : ''}.`,
          outcome: 'transform',
          decidedBy: ref,
          detail: `${formatEndpoint(origDstIp, origDstPort)} → firewall:${newPort}`,
        });
        break;
      }
      if (pf.target?.address) {
        const targetIp = parseIp(pf.target.address);
        if (targetIp) {
          effectiveDstIp = targetIp;
          effectiveDstPort = newPort;
          matchedPortForward = { pf, index: i, reflected: usedReflection, toSelf: false };
          dnatApplied = true;
          reflectedNat = usedReflection || Boolean(pf.toNegated);
          steps.push({
            id: 'prerouting',
            stage: 'prerouting',
            title: 'Pre-routing NAT',
            summary: `${usedReflection ? 'NAT reflection translated' : 'Translated'} destination to ${formatEndpoint(targetIp, newPort)}.`,
            outcome: 'transform',
            decidedBy: ref,
            detail: `${formatEndpoint(origDstIp, origDstPort)} → ${formatEndpoint(targetIp, newPort)}`,
          });
          break;
        }
      }
    }

    // 1:1 NAT inbound DNAT (IPv4 only, if no port forward matched).
    if (!dnatApplied && origDstIp.family === 4) {
      const excludedByZone = new Map<string, ParsedPrefix[]>();
      const oneToOne = snapshot.nat?.oneToOne ?? [];
      for (let i = 0; i < oneToOne.length; i += 1) {
        const o = oneToOne[i];
        if (!o.enabled) continue;
        const internal = parsePrefix(o.internal);
        if (!internal || internal.family !== 4) continue;
        if (o.noBinat) {
          excludedByZone.set(o.zone, [...(excludedByZone.get(o.zone) ?? []), internal]);
          continue;
        }
        const external = parsePrefix(o.external ?? '');
        if (!external || external.family !== 4 || external.bits !== internal.bits) continue;
        const normalZone = o.zone === ingressSegment.zone;
        const reflectZone = Boolean(o.reflection) && ingressIsInternal;
        if (!normalZone && !reflectZone) continue;
        if (!matchesAddressList(o.destination, Boolean(o.destinationNegated), srcIp, addressesById)) continue;
        if (!prefixContains(external, origDstIp)) continue;

        const mappedVal = internal.network + (origDstIp.value - external.network);
        const mappedIp: ParsedIp = { family: 4, value: mappedVal, canonical: formatIp(4, mappedVal) };
        const excluded = (excludedByZone.get(o.zone) ?? []).some((x) => prefixContains(x, mappedIp));
        if (excluded) continue;

        effectiveDstIp = mappedIp;
        dnatApplied = true;
        reflectedNat = !normalZone && reflectZone;
        steps.push({
          id: 'prerouting',
          stage: 'prerouting',
          title: 'Pre-routing NAT',
          summary: `1:1 NAT${reflectedNat ? ' reflection' : ''} translated destination to ${mappedIp.canonical}.`,
          outcome: 'transform',
          decidedBy: { label: `1:1 NAT #${i + 1} (${o.name})`, to: '/firewall/nat' },
          detail: `${origDstIp.canonical} → ${mappedIp.canonical}`,
        });
        break;
      }
    }
  }

  if (!dnatApplied) {
    steps.push({
      id: 'prerouting',
      stage: 'prerouting',
      title: 'Pre-routing NAT',
      summary: hasManagement ? 'No port forward or 1:1 DNAT entry matched.' : 'Skipped (packet filter inactive).',
      outcome: 'info',
    });
  }

  // Stage 3: Routing decision
  let chain: 'input' | 'forward' | 'none' = 'none';
  let egressSegment: Segment | undefined;
  let egressZoneId: string | undefined;

  const localOwner = matchedPortForward?.toSelf
    ? { label: `This firewall (${ingressSegment.name})`, to: '/network/interfaces', segmentId: ingressSegment.id, isVirtual: false }
    : findLocalOwner(effectiveDstIp);

  if (localOwner) {
    chain = 'input';
    egressZoneId = firewallZone;
    steps.push({
      id: 'routing',
      stage: 'routing',
      title: 'Routing',
      summary: `Delivered locally to this firewall (${localOwner.label}).`,
      outcome: 'pass',
      decidedBy: { label: localOwner.label, to: localOwner.to },
      zone: { name: 'This firewall' },
      detail: `Chain: input`,
    });
  } else {
    // Longest-prefix match among enabled connected segment subnets.
    let bestConnected: { segment: Segment; prefix: ParsedPrefix } | undefined;
    for (const entry of allSegmentPrefixes) {
      if (!entry.enabled || !prefixContains(entry.prefix, effectiveDstIp)) continue;
      if (!bestConnected || entry.prefix.bits > bestConnected.prefix.bits) {
        bestConnected = { segment: entry.segment, prefix: entry.prefix };
      }
    }
    if (bestConnected) {
      chain = 'forward';
      egressSegment = bestConnected.segment;
      egressZoneId = egressSegment.zone;
      const ez = zonesById.get(egressZoneId);
      steps.push({
        id: 'routing',
        stage: 'routing',
        title: 'Routing',
        summary: `Directly connected subnet ${bestConnected.prefix.raw} on ${egressSegment.name} (zone ${ez?.name ?? egressZoneId}).`,
        outcome: 'pass',
        decidedBy: { label: `Interface ${egressSegment.name}`, to: '/network/interfaces' },
        zone: ez ? { name: ez.name, color: ez.color } : undefined,
        detail: `Chain: forward`,
      });
    } else {
      // Default route via an enabled uplink segment for this IP family.
      const uplinks = snapshot.segments.filter((s) => {
        if (linksById.get(s.link)?.enabled === false) return false;
        return effectiveDstIp.family === 4 ? isUplink(s, linksById) : isIpv6Uplink(s);
      });
      if (uplinks.length > 0) {
        chain = 'forward';
        egressSegment = uplinks[0];
        egressZoneId = egressSegment.zone;
        const ez = zonesById.get(egressZoneId);
        const gw = effectiveDstIp.family === 4 ? egressSegment.addressing.ipv4.gateway : egressSegment.addressing.ipv6.gateway;
        if (uplinks.length > 1) {
          warnings.push(`Multiple IPv${effectiveDstIp.family} uplinks exist (${uplinks.map((u) => u.name).join(', ')}); assuming default route via ${egressSegment.name}.`);
        }
        steps.push({
          id: 'routing',
          stage: 'routing',
          title: 'Routing',
          summary: `Default route via ${egressSegment.name}${gw ? ` (gateway ${gw})` : ''} in zone ${ez?.name ?? egressZoneId}.`,
          outcome: 'pass',
          decidedBy: { label: `Interface ${egressSegment.name}`, to: '/network/interfaces' },
          zone: ez ? { name: ez.name, color: ez.color } : undefined,
          detail: `Chain: forward`,
        });
      } else {
        const finalTuple = formatTuple(probe.protocol, srcIp, srcPort, effectiveDstIp, effectiveDstPort, icmpType);
        const decidedBy: DecidingRef = { label: 'Routing table (Interfaces)', to: '/network/interfaces' };
        steps.push({
          id: 'routing',
          stage: 'routing',
          title: 'Routing',
          summary: `No connected subnet or IPv${effectiveDstIp.family} default gateway reaches ${effectiveDstIp.canonical}.`,
          outcome: 'drop',
          decidedBy,
        });
        steps.push({
          id: 'verdict',
          stage: 'verdict',
          title: 'Verdict',
          summary: 'Packet cannot be routed (no route to host).',
          outcome: 'drop',
          decidedBy,
          detail: finalTuple,
        });
        return {
          verdict: 'no_route',
          verdictLabel: 'No route',
          chain: 'none',
          originalTuple,
          finalTuple,
          decidedBy,
          warnings,
          steps,
        };
      }
    }
  }

  // Stage 4: Firewall filter (input or forward chain)
  let filterVerdict: 'accept' | 'drop' | 'reject' = 'drop';
  let filterDecidedBy: DecidingRef | undefined;
  let filterLogged = false;

  if (!hasManagement) {
    filterVerdict = 'accept';
    filterDecidedBy = { label: 'Management segment missing', to: '/network/interfaces' };
    steps.push({
      id: 'filter',
      stage: 'filter',
      title: `Firewall (${chain})`,
      summary: 'Packet filter is not loaded without a management segment; packet passes unfiltered.',
      outcome: 'warn',
      decidedBy: filterDecidedBy,
    });
  } else {
    let matchedFilter = false;

    // 4a: Fixed input rules (nft.go:87-120)
    if (chain === 'input') {
      if (probe.protocol === 'icmpv6' && icmpType && ndpTypes.has(icmpType)) {
        matchedFilter = true;
        filterVerdict = 'accept';
        steps.push({
          id: 'filter',
          stage: 'filter',
          title: 'Firewall (input)',
          summary: `Allowed by fixed IPv6 Neighbor/Router Discovery rule (${icmpType}).`,
          outcome: 'accept',
          decidedBy: { label: 'Fixed rules (Firewall)', to: '/firewall/rules' },
        });
      } else if (probe.protocol === 'udp' && isLinkLocalIpv6(srcIp) && srcPort === 547 && effectiveDstPort === 546) {
        matchedFilter = true;
        filterVerdict = 'accept';
        steps.push({
          id: 'filter',
          stage: 'filter',
          title: 'Firewall (input)',
          summary: 'Allowed by fixed DHCPv6 client reply rule (UDP 547 → 546 from link-local).',
          outcome: 'accept',
          decidedBy: { label: 'Fixed rules (Firewall)', to: '/firewall/rules' },
        });
      } else if (probe.protocol === 'icmpv6' && isLinkLocalIpv6(srcIp) && icmpType && mldTypes.has(icmpType)) {
        matchedFilter = true;
        filterVerdict = 'accept';
        steps.push({
          id: 'filter',
          stage: 'filter',
          title: 'Firewall (input)',
          summary: `Allowed by fixed IPv6 Multicast Listener Discovery rule (${icmpType}).`,
          outcome: 'accept',
          decidedBy: { label: 'Fixed rules (Firewall)', to: '/firewall/rules' },
        });
      } else if (
        probe.protocol === 'udp'
        && srcPort === 68
        && effectiveDstPort === 67
        && (snapshot.dhcp ?? []).some((e) => e.segment === ingressSegment.id)
      ) {
        matchedFilter = true;
        filterVerdict = 'accept';
        filterDecidedBy = { label: `DHCPv4 on ${ingressSegment.name}`, to: '/network/dhcp' };
        steps.push({
          id: 'filter',
          stage: 'filter',
          title: 'Firewall (input)',
          summary: `Allowed by fixed DHCPv4 server rule on ${ingressSegment.name}.`,
          outcome: 'accept',
          decidedBy: filterDecidedBy,
        });
      } else if (
        probe.protocol === 'udp'
        && isLinkLocalIpv6(srcIp)
        && srcPort === 546
        && effectiveDstPort === 547
        && (snapshot.dhcp6 ?? []).some((e) => e.segment === ingressSegment.id)
      ) {
        matchedFilter = true;
        filterVerdict = 'accept';
        filterDecidedBy = { label: `DHCPv6 on ${ingressSegment.name}`, to: '/network/dhcp' };
        steps.push({
          id: 'filter',
          stage: 'filter',
          title: 'Firewall (input)',
          summary: `Allowed by fixed DHCPv6 server rule on ${ingressSegment.name}.`,
          outcome: 'accept',
          decidedBy: filterDecidedBy,
        });
      } else if (
        probe.protocol === 'tcp'
        && effectiveDstPort === 22
        && snapshot.management?.segment === ingressSegment.id
        && ((snapshot.management.allowedSources ?? []).length === 0
          || matchesAddressList(snapshot.management.allowedSources ?? [], false, srcIp, addressesById))
      ) {
        matchedFilter = true;
        filterVerdict = 'accept';
        filterDecidedBy = { label: `Management SSH (${ingressSegment.name})`, to: '/network/interfaces' };
        steps.push({
          id: 'filter',
          stage: 'filter',
          title: 'Firewall (input)',
          summary: `Allowed by fixed management SSH rule on ${ingressSegment.name} (TCP/22).`,
          outcome: 'accept',
          decidedBy: filterDecidedBy,
        });
      }
    }

    // 4b: Bound port-forward auto filter rule (nft.go:140-143, nat.go:153-168)
    if (!matchedFilter && matchedPortForward && matchedPortForward.pf.filterRule === 'auto' && !matchedPortForward.reflected) {
      matchedFilter = true;
      filterVerdict = 'accept';
      filterDecidedBy = {
        label: `Port forward #${matchedPortForward.index + 1} auto rule (${matchedPortForward.pf.name})`,
        to: '/firewall/nat',
      };
      steps.push({
        id: 'filter',
        stage: 'filter',
        title: `Firewall (${chain})`,
        summary: `Allowed by automatic filter rule bound to port forward "${matchedPortForward.pf.name}".`,
        outcome: 'accept',
        decidedBy: filterDecidedBy,
      });
    }

    // 4c: User firewall rules in order (nft.go:146-152)
    if (!matchedFilter) {
      const rules = snapshot.rules ?? [];
      for (let i = 0; i < rules.length; i += 1) {
        const rule = rules[i];
        if (!rule.enabled) continue;
        const isInputRule = rule.to.zones.includes(firewallZone);
        if ((chain === 'input') !== isInputRule) continue;
        if (!matchesRuleSide(rule.from, ingressSegment.zone, srcIp, false, addressesById)) continue;
        if (!matchesRuleSide(rule.to, egressZoneId ?? '', effectiveDstIp, isInputRule, addressesById)) continue;
        if (rule.ports.length > 0 && !rule.ports.some((pid) => matchesPortAlias(pid, probe.protocol, effectiveDstPort, icmpType, portsById))) continue;

        matchedFilter = true;
        filterVerdict = rule.action;
        filterLogged = rule.log;
        filterDecidedBy = { label: `Rule #${i + 1} (${rule.name})`, to: '/firewall/rules' };
        const rejectDetail = rule.action === 'reject' && probe.protocol === 'tcp' ? ' (TCP RST)' : '';
        steps.push({
          id: 'filter',
          stage: 'filter',
          title: `Firewall (${chain})`,
          summary: `Matched rule #${i + 1} "${rule.name}": ${rule.action}${rejectDetail}${rule.log ? ', logged' : ''}.`,
          outcome: rule.action,
          decidedBy: filterDecidedBy,
        });
        break;
      }
    }

    // 4d: Default chain policy: drop (nft.go:165-166)
    if (!matchedFilter) {
      filterVerdict = 'drop';
      filterDecidedBy = { label: `Default ${chain} policy (drop)`, to: '/firewall/rules' };
      steps.push({
        id: 'filter',
        stage: 'filter',
        title: `Firewall (${chain})`,
        summary: `No rule matched; dropped by default ${chain} chain policy.`,
        outcome: 'drop',
        decidedBy: filterDecidedBy,
      });
    }
  }

  // Stage 5: Post-routing NAT (only for accepted forwarded traffic)
  let effectiveSrcIp = srcIp;
  let snatNote = '';
  if (chain === 'forward' && filterVerdict === 'accept' && egressSegment && hasManagement) {
    let snatApplied = false;

    // 5a: 1:1 NAT outbound SNAT (IPv4 only, nat.go:258-324)
    if (srcIp.family === 4) {
      const excludedByZone = new Map<string, ParsedPrefix[]>();
      const oneToOne = snapshot.nat?.oneToOne ?? [];
      for (let i = 0; i < oneToOne.length; i += 1) {
        const o = oneToOne[i];
        if (!o.enabled) continue;
        const internal = parsePrefix(o.internal);
        if (!internal || internal.family !== 4) continue;
        if (o.noBinat) {
          excludedByZone.set(o.zone, [...(excludedByZone.get(o.zone) ?? []), internal]);
          continue;
        }
        const external = parsePrefix(o.external ?? '');
        if (!external || external.family !== 4 || external.bits !== internal.bits) continue;
        if (o.zone !== egressSegment.zone) continue;
        if (!matchesAddressList(o.destination, Boolean(o.destinationNegated), effectiveDstIp, addressesById)) continue;
        if (!prefixContains(internal, srcIp)) continue;
        const excluded = (excludedByZone.get(o.zone) ?? []).some((x) => prefixOverlaps(x, internal) && prefixContains(x, srcIp));
        if (excluded) continue;

        const mappedVal = external.network + (srcIp.value - internal.network);
        effectiveSrcIp = { family: 4, value: mappedVal, canonical: formatIp(4, mappedVal) };
        snatApplied = true;
        steps.push({
          id: 'postrouting',
          stage: 'postrouting',
          title: 'Post-routing NAT',
          summary: `1:1 NAT translated source address to ${effectiveSrcIp.canonical}.`,
          outcome: 'transform',
          decidedBy: { label: `1:1 NAT #${i + 1} (${o.name})`, to: '/firewall/nat' },
          detail: `${srcIp.canonical} → ${effectiveSrcIp.canonical}`,
        });
        break;
      }
    }

    // 5b: Hairpin NAT reflection masquerade (nft.go:130)
    if (!snatApplied && reflectedNat && srcIp.family === 4 && egressSegment.id === ingressSegment.id) {
      const segPrefix = allSegmentPrefixes.find((e) => e.segment.id === egressSegment.id && e.prefix.family === 4 && prefixContains(e.prefix, srcIp));
      if (segPrefix) {
        effectiveSrcIp = { family: 4, value: segPrefix.prefix.addr, canonical: formatIp(4, segPrefix.prefix.addr) };
        snatApplied = true;
        steps.push({
          id: 'postrouting',
          stage: 'postrouting',
          title: 'Post-routing NAT',
          summary: `Hairpin reflection masqueraded source to ${effectiveSrcIp.canonical} on ${egressSegment.name}.`,
          outcome: 'transform',
          decidedBy: { label: 'NAT reflection masquerade', to: '/firewall/nat' },
          detail: `${srcIp.canonical} → ${effectiveSrcIp.canonical}`,
        });
      }
    }

    // 5c: Outbound automatic masquerade (nft.go:170-173)
    if (!snatApplied && srcIp.family === 4 && snapshot.nat?.outbound?.mode === 'automatic' && isUplink(egressSegment, linksById)) {
      const egressAddr = localOwners.find((o) => o.owner.segmentId === egressSegment.id && o.ip.family === 4)?.ip;
      snatApplied = true;
      if (egressAddr) {
        effectiveSrcIp = egressAddr;
      } else {
        snatNote = ` (masqueraded to ${egressSegment.name} IPv4)`;
      }
      steps.push({
        id: 'postrouting',
        stage: 'postrouting',
        title: 'Post-routing NAT',
        summary: egressAddr
          ? `Outbound NAT masqueraded source address to ${egressAddr.canonical} on ${egressSegment.name}.`
          : `Outbound NAT masqueraded source address to ${egressSegment.name}'s dynamic IPv4 address.`,
        outcome: 'transform',
        decidedBy: { label: 'Outbound NAT (Automatic)', to: '/firewall/nat' },
        detail: egressAddr ? `${srcIp.canonical} → ${egressAddr.canonical}` : `${srcIp.canonical} → ${egressSegment.name} IPv4`,
      });
    }

    if (!snatApplied) {
      steps.push({
        id: 'postrouting',
        stage: 'postrouting',
        title: 'Post-routing NAT',
        summary: 'No source NAT applied; original source address is preserved.',
        outcome: 'info',
      });
    }
  }

  // Stage 6: Final verdict
  const finalTuple = formatTuple(probe.protocol, effectiveSrcIp, srcPort, effectiveDstIp, effectiveDstPort, icmpType) + snatNote;
  const verdictLabel = filterVerdict === 'accept'
    ? 'Allowed'
    : filterVerdict === 'reject'
      ? (probe.protocol === 'tcp' ? 'Rejected (TCP RST)' : 'Rejected')
      : 'Dropped';

  const verdictSummary = filterVerdict === 'accept'
    ? (chain === 'input'
      ? `Traffic is delivered to this firewall${filterLogged ? ' (logged)' : ''}.`
      : `Traffic is forwarded out ${egressSegment?.name ?? ''}${filterLogged ? ' (logged)' : ''}.`)
    : filterVerdict === 'reject'
      ? `Traffic is rejected${probe.protocol === 'tcp' ? ' with TCP RST' : ''}${filterLogged ? ' (logged)' : ''}.`
      : `Traffic is silently dropped${filterLogged ? ' (logged)' : ''}.`;

  steps.push({
    id: 'verdict',
    stage: 'verdict',
    title: 'Verdict',
    summary: verdictSummary,
    outcome: filterVerdict,
    decidedBy: filterDecidedBy,
    detail: finalTuple,
  });

  return {
    verdict: filterVerdict,
    verdictLabel,
    chain,
    originalTuple,
    finalTuple,
    decidedBy: filterDecidedBy,
    warnings,
    steps,
  };
}
