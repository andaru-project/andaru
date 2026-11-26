import { parseIpv6Groups } from './dhcpEdit';
import { parsePortSpan } from './natEdit';
import { firewallZone, type AddressAlias, type Lease6, type LinkStatus, type PortAlias, type PortForward, type RuleSide, type SavedLink, type Segment, type Snapshot } from './types';
import { kernelName } from './zones';

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
  stage: StepStage;
  title: string;
  summary: string;
  outcome: StepOutcome;
  decidedBy?: DecidingRef;
  zone?: { name: string; color?: string };
  isFirewall?: boolean;
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
  ip: ParsedIp;
  network: bigint;
  mask: bigint;
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
  return `${groups.slice(0, bestStart).join(':')}::${groups.slice(bestStart + bestLen).join(':')}`;
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
    const mask = ip.family === 4 ? ipv4Max : ipv6Max;
    return { family: ip.family, ip, network: ip.value, mask, bits: ip.family === 4 ? 32 : 128, raw: s };
  }
  const ip = parseIp(s.slice(0, slash));
  const lenStr = s.slice(slash + 1);
  if (!ip || !/^(?:0|[1-9]\d{0,2})$/.test(lenStr)) return null;
  const bits = Number(lenStr);
  const maxBits = ip.family === 4 ? 32 : 128;
  if (bits > maxBits) return null;
  const full = ip.family === 4 ? ipv4Max : ipv6Max;
  const mask = bits === 0 ? 0n : (full << BigInt(maxBits - bits)) & full;
  return { family: ip.family, ip, network: ip.value & mask, mask, bits, raw: s };
}

function prefixContains(prefix: ParsedPrefix, ip: ParsedIp): boolean {
  return prefix.family === ip.family && (ip.value & prefix.mask) === prefix.network;
}

function isLinkLocalIpv6(ip: ParsedIp): boolean {
  return ip.family === 6 && ((ip.value >> 118n) & 0x3ffn) === 0x3fan; // fe80::/10
}

function isLinkLocalMulticast(ip: ParsedIp): boolean {
  if (ip.family === 4) return ((ip.value >> 8n) & 0xffffffn) === 0xe00000n; // 224.0.0.0/24
  return ((ip.value >> 112n) & 0xffffn) === 0xff02n; // ff02::/16
}

function isBroadcastOrMulticast(ip: ParsedIp, enabledPrefixes: ParsedPrefix[]): boolean {
  if (ip.family === 4) {
    if (ip.value === ipv4Max) return true; // 255.255.255.255
    if (((ip.value >> 24n) & 0xf0n) === 0xe0n) return true; // 224.0.0.0/4
    for (const p of enabledPrefixes) {
      if (p.family !== 4 || p.bits > 30) continue;
      const bcast = p.network | ((1n << BigInt(32 - p.bits)) - 1n);
      if (ip.value === bcast) return true;
    }
    return false;
  }
  return ((ip.value >> 120n) & 0xffn) === 0xffn; // ff00::/8
}

// Walk parent links (vlan/pppoe) with cycle guard to mirror internal/render/resolve.go usability.
function isLinkUsable(linkId: string, linksById: Map<string, SavedLink>, visited = new Set<string>()): boolean {
  if (visited.has(linkId)) return false;
  visited.add(linkId);
  const link = linksById.get(linkId);
  if (!link || !link.enabled) return false;
  if ((link.kind === 'vlan' || link.kind === 'pppoe') && link.parent) {
    return isLinkUsable(link.parent, linksById, visited);
  }
  return true;
}

function isUplink(segment: Segment, linksById: Map<string, SavedLink>): boolean {
  const link = linksById.get(segment.link);
  return Boolean(segment.addressing.ipv4.gateway) || segment.addressing.ipv4.mode === 'dhcp' || link?.kind === 'pppoe';
}

function isIpv6Uplink(segment: Segment): boolean {
  return Boolean(segment.addressing.ipv6.gateway) || segment.addressing.ipv6.mode === 'slaac' || segment.addressing.ipv6.mode === 'dhcp6';
}

// Match a saved link to its live LinkStatus by unique permanent MAC (Ethernet) or derived kernel name (virtual links).
function findLiveLink(link: SavedLink | undefined, liveLinks: LinkStatus[]): LinkStatus | undefined {
  if (!link) return undefined;
  if (link.kind === 'ethernet') {
    if (link.permanentMac) {
      const targetMac = link.permanentMac.toLowerCase();
      const matches = liveLinks.filter((l) => l.linkType === 'ether' && !l.kind && (l.permanentAddress ?? l.linkAddress)?.toLowerCase() === targetMac);
      return matches.length === 1 ? matches[0] : undefined;
    }
    return liveLinks.find((l) => l.name === link.name);
  }
  const derived = kernelName(link, new Map());
  return derived ? liveLinks.find((l) => l.name === derived) : undefined;
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
    const sameFamily = refs.filter((id) => aliasHasFamily(id, ip.family, byId));
    if (sameFamily.length === 0) return true;
    return sameFamily.every((id) => !matchesAddressAlias(id, ip, byId));
  }
  return refs.some((id) => matchesAddressAlias(id, ip, byId));
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
      return span !== null && dstPort >= span.first && dstPort <= span.last;
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

// Shared 1:1 NAT matcher for inbound DNAT and outbound SNAT (matching internal/render/nat.go:258-326).
function matchOneToOne(
  entries: NonNullable<NonNullable<Snapshot['value']['nat']>['oneToOne']>,
  direction: 'inbound' | 'outbound',
  zoneId: string,
  allowReflection: boolean,
  translatedIp: ParsedIp,
  peerIp: ParsedIp,
  addressesById: Map<string, Partial<AddressAlias> & { id: string; name: string }>,
): { index: number; name: string; mappedIp: ParsedIp; reflected: boolean } | undefined {
  if (translatedIp.family !== 4) return undefined;
  const excludedByZone = new Map<string, ParsedPrefix[]>();
  for (let i = 0; i < entries.length; i += 1) {
    const o = entries[i];
    if (!o.enabled) continue;
    const internal = parsePrefix(o.internal);
    if (!internal || internal.family !== 4) continue;
    if (o.noBinat) {
      excludedByZone.set(o.zone, [...(excludedByZone.get(o.zone) ?? []), internal]);
      continue;
    }
    const external = parsePrefix(o.external ?? '');
    if (!external || external.family !== 4 || external.bits !== internal.bits) continue;

    const normalZone = o.zone === zoneId;
    const reflectZone = direction === 'inbound' && Boolean(o.reflection) && allowReflection;
    if (!normalZone && !reflectZone) continue;
    if (!matchesAddressList(o.destination, Boolean(o.destinationNegated), peerIp, addressesById)) continue;

    const fromPrefix = direction === 'inbound' ? external : internal;
    const toPrefix = direction === 'inbound' ? internal : external;
    if (!prefixContains(fromPrefix, translatedIp)) continue;

    const mappedVal = toPrefix.network + (translatedIp.value - fromPrefix.network);
    const mappedIp: ParsedIp = { family: 4, value: mappedVal, canonical: formatIp(4, mappedVal) };
    const internalIp = direction === 'inbound' ? mappedIp : translatedIp;
    if ((excludedByZone.get(o.zone) ?? []).some((x) => prefixContains(x, internalIp))) continue;

    return { index: i, name: o.name, mappedIp, reflected: !normalZone && reflectZone };
  }
  return undefined;
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
  linkLocal?: boolean;
}

interface FilterDecision {
  verdict: 'accept' | 'drop' | 'reject';
  decidedBy: DecidingRef;
  summary: string;
  outcome: StepOutcome;
  logged: boolean;
  rejectTcpReset: boolean;
}

function evaluateFilter(params: {
  chain: 'input' | 'forward';
  hasManagement: boolean;
  probe: TrafficProbe;
  srcIp: ParsedIp;
  srcPort?: number;
  effectiveDstIp: ParsedIp;
  effectiveDstPort?: number;
  icmpType?: string;
  ingressSegment: Segment;
  egressZoneId?: string;
  matchedPortForward?: { pf: PortForward; index: number; isLocalTarget: boolean };
  snapshot: Snapshot['value'];
  linksById: Map<string, SavedLink>;
  addressesById: Map<string, Partial<AddressAlias> & { id: string; name: string }>;
  portsById: Map<string, Partial<PortAlias> & { id: string; name: string }>;
}): FilterDecision {
  const {
    chain, hasManagement, probe, srcIp, srcPort, effectiveDstIp, effectiveDstPort, icmpType,
    ingressSegment, egressZoneId, matchedPortForward, snapshot, linksById, addressesById, portsById,
  } = params;

  if (!hasManagement) {
    if (chain === 'forward') {
      return {
        verdict: 'drop',
        decidedBy: { label: 'Management segment missing', to: '/network/interfaces' },
        summary: 'IP forwarding is disabled without a usable management segment so traffic cannot route unfiltered.',
        outcome: 'drop',
        logged: false,
        rejectTcpReset: false,
      };
    }
    return {
      verdict: 'accept',
      decidedBy: { label: 'Management segment missing', to: '/network/interfaces' },
      summary: 'Packet filter is not loaded without a usable management segment; local input passes unfiltered.',
      outcome: 'warn',
      logged: false,
      rejectTcpReset: false,
    };
  }

  const fixedRulesRef: DecidingRef = { label: 'Fixed rules (Firewall)', to: '/firewall/rules' };
  const ingressUsable = isLinkUsable(ingressSegment.link, linksById);

  // 4a: Fixed input rules (internal/render/nft.go:87-120)
  if (chain === 'input') {
    if (probe.protocol === 'icmpv6' && icmpType && ndpTypes.has(icmpType)) {
      return { verdict: 'accept', decidedBy: fixedRulesRef, summary: `Allowed by fixed IPv6 Neighbor/Router Discovery rule (${icmpType}).`, outcome: 'accept', logged: false, rejectTcpReset: false };
    }
    if (probe.protocol === 'udp' && isLinkLocalIpv6(srcIp) && srcPort === 547 && effectiveDstPort === 546) {
      return { verdict: 'accept', decidedBy: fixedRulesRef, summary: 'Allowed by fixed DHCPv6 client reply rule (UDP 547 → 546 from link-local).', outcome: 'accept', logged: false, rejectTcpReset: false };
    }
    if (probe.protocol === 'icmpv6' && isLinkLocalIpv6(srcIp) && icmpType && mldTypes.has(icmpType)) {
      return { verdict: 'accept', decidedBy: fixedRulesRef, summary: `Allowed by fixed IPv6 Multicast Listener Discovery rule (${icmpType}).`, outcome: 'accept', logged: false, rejectTcpReset: false };
    }
    const servesDhcp4 = ingressUsable
      && ingressSegment.addressing.ipv4.mode === 'static'
      && (ingressSegment.addressing.ipv4.addresses?.length ?? 0) > 0
      && (snapshot.dhcp ?? []).some((e) => e.segment === ingressSegment.id);
    if (probe.protocol === 'udp' && srcPort === 68 && effectiveDstPort === 67 && servesDhcp4) {
      return {
        verdict: 'accept',
        decidedBy: { label: `DHCPv4 on ${ingressSegment.name}`, to: '/network/dhcp' },
        summary: `Allowed by fixed DHCPv4 server rule on ${ingressSegment.name}.`,
        outcome: 'accept',
        logged: false,
        rejectTcpReset: false,
      };
    }
    const hasStatic64 = ingressSegment.addressing.ipv6.mode === 'static'
      && (ingressSegment.addressing.ipv6.addresses ?? []).some((raw) => {
        const p = parsePrefix(raw);
        return p !== null && p.family === 6 && p.bits === 64 && !isLinkLocalIpv6(p.ip);
      });
    const delegatedSource = ingressSegment.addressing.ipv6.mode === 'delegated' && ingressSegment.addressing.ipv6.from
      ? snapshot.segments.find((s) => s.id === ingressSegment.addressing.ipv6.from)
      : undefined;
    const hasDelegated = Boolean(delegatedSource && isLinkUsable(delegatedSource.link, linksById));
    const servesDhcp6 = ingressUsable && (hasStatic64 || hasDelegated) && (snapshot.dhcp6 ?? []).some((e) => e.segment === ingressSegment.id);
    if (probe.protocol === 'udp' && isLinkLocalIpv6(srcIp) && srcPort === 546 && effectiveDstPort === 547 && servesDhcp6) {
      return {
        verdict: 'accept',
        decidedBy: { label: `DHCPv6 on ${ingressSegment.name}`, to: '/network/dhcp' },
        summary: `Allowed by fixed DHCPv6 server rule on ${ingressSegment.name}.`,
        outcome: 'accept',
        logged: false,
        rejectTcpReset: false,
      };
    }
    if (
      probe.protocol === 'tcp'
      && effectiveDstPort === 22
      && snapshot.management?.segment === ingressSegment.id
      && matchesAddressList(snapshot.management.allowedSources ?? [], false, srcIp, addressesById)
    ) {
      return {
        verdict: 'accept',
        decidedBy: { label: `Management SSH (${ingressSegment.name})`, to: '/network/interfaces' },
        summary: `Allowed by fixed management SSH rule on ${ingressSegment.name} (TCP/22).`,
        outcome: 'accept',
        logged: false,
        rejectTcpReset: false,
      };
    }
  }

  // 4b: Bound port-forward auto filter rule (nft.go:140-143, nat.go:153-168)
  if (
    matchedPortForward
    && matchedPortForward.pf.filterRule === 'auto'
    && matchedPortForward.pf.ingressZones.includes(ingressSegment.zone)
    && chain === (matchedPortForward.isLocalTarget ? 'input' : 'forward')
  ) {
    return {
      verdict: 'accept',
      decidedBy: { label: `Port forward #${matchedPortForward.index + 1} auto rule (${matchedPortForward.pf.name})`, to: '/firewall/nat' },
      summary: `Allowed by automatic filter rule bound to port forward "${matchedPortForward.pf.name}".`,
      outcome: 'accept',
      logged: false,
      rejectTcpReset: false,
    };
  }

  // 4c: User firewall rules in order (nft.go:146-152, 382-385)
  const rules = snapshot.rules ?? [];
  for (let i = 0; i < rules.length; i += 1) {
    const rule = rules[i];
    if (!rule.enabled) continue;
    const isInputRule = rule.to.zones.includes(firewallZone);
    if ((chain === 'input') !== isInputRule) continue;
    if (!matchesRuleSide(rule.from, ingressSegment.zone, srcIp, false, addressesById)) continue;
    if (!matchesRuleSide(rule.to, egressZoneId ?? '', effectiveDstIp, isInputRule, addressesById)) continue;
    if (rule.ports.length > 0 && !rule.ports.some((pid) => matchesPortAlias(pid, probe.protocol, effectiveDstPort, icmpType, portsById))) continue;

    // nft.go:383 emits `reject with tcp reset` only when a TCP port alias matched (not when ports is empty).
    const rejectTcpReset = rule.action === 'reject' && probe.protocol === 'tcp' && rule.ports.length > 0;
    return {
      verdict: rule.action,
      decidedBy: { label: `Rule #${i + 1} (${rule.name})`, to: '/firewall/rules' },
      summary: `Matched rule #${i + 1} "${rule.name}": ${rule.action}${rejectTcpReset ? ' (TCP RST)' : ''}${rule.log ? ', logged' : ''}.`,
      outcome: rule.action,
      logged: rule.log,
      rejectTcpReset,
    };
  }

  // 4d: Default chain policy: drop (nft.go:165-166)
  return {
    verdict: 'drop',
    decidedBy: { label: `Default ${chain} policy (drop)`, to: '/firewall/rules' },
    summary: `No rule matched; dropped by default ${chain} chain policy.`,
    outcome: 'drop',
    logged: false,
    rejectTcpReset: false,
  };
}

// Simulate a hypothetical packet through Andaru's pre-routing NAT, routing, firewall filter, and post-routing NAT.
export function simulateTrafficFlow(
  probe: TrafficProbe,
  snapshot: Snapshot['value'],
  liveLinks: LinkStatus[] = [],
  options: { evaluatingSavedDiffersFromLive?: boolean; liveLeases6?: Lease6[] } = {},
): TrafficSimulation {
  const srcIp = parseIp(probe.srcIp)!;
  const origDstIp = parseIp(probe.dstIp)!;
  const icmpType = probe.icmpType?.trim().toLowerCase() || undefined;
  const srcPort = probe.protocol === 'tcp' || probe.protocol === 'udp' ? probe.srcPort : undefined;
  const origDstPort = probe.protocol === 'tcp' || probe.protocol === 'udp' ? probe.dstPort : undefined;

  const linksById = new Map(snapshot.links.map((l) => [l.id, l]));
  const segmentsById = new Map(snapshot.segments.map((s) => [s.id, s]));
  const zonesById = new Map(snapshot.zones.map((z) => [z.id, z]));
  const addressesById = new Map((snapshot.aliases?.addresses ?? []).map((a) => [a.id, a]));
  const portsById = new Map((snapshot.aliases?.ports ?? []).map((p) => [p.id, p]));
  const ingressSegment = segmentsById.get(probe.ingressSegmentId)!;
  const ingressLink = linksById.get(ingressSegment.link);
  const ingressUsable = isLinkUsable(ingressSegment.link, linksById);
  const ingressZone = zonesById.get(ingressSegment.zone);
  const ingressZoneInfo = ingressZone ? { name: ingressZone.name, color: ingressZone.color } : undefined;
  const ingressIsInternal = ingressUsable && !isUplink(ingressSegment, linksById);

  const warnings: string[] = [];
  const steps: FlowStep[] = [];
  const originalTuple = formatTuple(probe.protocol, srcIp, srcPort, origDstIp, origDstPort, icmpType);

  if (!ingressUsable) {
    warnings.push(`Ingress interface ${ingressLink?.name ?? ingressSegment.name} (or its parent) is disabled in this configuration.`);
  }
  const mgmtSegment = snapshot.management?.segment
    ? segmentsById.get(snapshot.management.segment)
    : undefined;
  const hasManagement = Boolean(mgmtSegment && isLinkUsable(mgmtSegment.link, linksById));
  if (!hasManagement) {
    warnings.push('No usable management segment is configured: Andaru does not load firewall rules, NAT, or IP forwarding without one.');
  }

  // Gather usable segment prefixes and firewall-owned addresses (static + virtual + dynamic live).
  const usableSegmentPrefixes: Array<{ segment: Segment; prefix: ParsedPrefix; staticIpv4: boolean }> = [];
  const localOwners: Array<{ ip: ParsedIp; owner: LocalOwner }> = [];
  const internalOwn4 = new Set<bigint>();
  const virtual4 = new Set<bigint>();
  let usedLiveDynamic = false;

  for (const seg of snapshot.segments) {
    if (!isLinkUsable(seg.link, linksById)) continue;
    const link = linksById.get(seg.link);
    const segUplink = isUplink(seg, linksById);

    if (seg.addressing.ipv4.mode === 'static') {
      for (const raw of seg.addressing.ipv4.addresses ?? []) {
        const p = parsePrefix(raw);
        if (!p || p.family !== 4) continue;
        usableSegmentPrefixes.push({ segment: seg, prefix: p, staticIpv4: true });
        localOwners.push({ ip: p.ip, owner: { label: `Interface ${seg.name}`, to: '/network/interfaces', segmentId: seg.id, isVirtual: false } });
        if (!segUplink) internalOwn4.add(p.ip.value);
      }
    }
    if (seg.addressing.ipv6.mode === 'static' || seg.addressing.ipv6.mode === 'delegated') {
      for (const raw of seg.addressing.ipv6.addresses ?? []) {
        const p = parsePrefix(raw);
        if (!p || p.family !== 6) continue;
        const linkLocal = isLinkLocalIpv6(p.ip);
        if (!linkLocal) usableSegmentPrefixes.push({ segment: seg, prefix: p, staticIpv4: false });
        localOwners.push({ ip: p.ip, owner: { label: `Interface ${seg.name}`, to: '/network/interfaces', segmentId: seg.id, isVirtual: false, linkLocal } });
      }
    }

    // Merge live addresses only for dynamic addressing modes (plus link-local on ingress for DHCPv6/NDP/MLD).
    const live = findLiveLink(link, liveLinks);
    if (!live) continue;
    const dynamicV4 = seg.addressing.ipv4.mode === 'dhcp' || link?.kind === 'pppoe';
    const dynamicV6 = seg.addressing.ipv6.mode === 'slaac' || seg.addressing.ipv6.mode === 'dhcp6' || seg.addressing.ipv6.mode === 'delegated';
    for (const a of live.addresses) {
      const isLinkLocal = a.scope === 'link';
      if (isLinkLocal) {
        if (seg.id !== ingressSegment.id || a.family !== 'ipv6') continue;
      } else if (a.family === 'ipv4' ? !dynamicV4 : !dynamicV6) {
        continue;
      }
      const p = parsePrefix(`${a.address}/${a.prefixLength}`);
      if (!p) continue;
      if (!isLinkLocal) {
        usedLiveDynamic = true;
        if (!usableSegmentPrefixes.some((e) => e.segment.id === seg.id && e.prefix.family === p.family && e.prefix.ip.value === p.ip.value && e.prefix.bits === p.bits)) {
          usableSegmentPrefixes.push({ segment: seg, prefix: p, staticIpv4: false });
        }
      }
      if (!localOwners.some((o) => o.ip.family === p.family && o.ip.value === p.ip.value && (!isLinkLocal || o.owner.segmentId === seg.id))) {
        localOwners.push({
          ip: p.ip,
          owner: { label: `Interface ${seg.name} (live)`, to: '/network/interfaces', segmentId: seg.id, isVirtual: false, linkLocal: isLinkLocal },
        });
      }
    }
  }

  if (usedLiveDynamic && options.evaluatingSavedDiffersFromLive) {
    warnings.push('Dynamic DHCP/PPPoE/SLAAC addresses are read from live interfaces because saved-only dynamic leases do not exist yet.');
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

  const findLocalOwner = (ip: ParsedIp): LocalOwner | undefined =>
    localOwners.find((o) => o.ip.family === ip.family && o.ip.value === ip.value && (!o.owner.linkLocal || o.owner.segmentId === ingressSegment.id))?.owner;

  const isLocalTarget4 = (ip: ParsedIp): boolean => Boolean(findLocalOwner(ip));

  // Table-wide hairpin reflection flag, matching nft.go:124-131 + nat.go:107,173,283-316.
  const hasActiveOneToOneReflection = (() => {
    const excludedByZone = new Map<string, ParsedPrefix[]>();
    for (const o of snapshot.nat?.oneToOne ?? []) {
      if (!o.enabled) continue;
      const internal = parsePrefix(o.internal);
      if (!internal || internal.family !== 4) continue;
      if (o.noBinat) {
        excludedByZone.set(o.zone, [...(excludedByZone.get(o.zone) ?? []), internal]);
        continue;
      }
      const wholeExcluded = (excludedByZone.get(o.zone) ?? []).some((x) => x.bits <= internal.bits && prefixContains(x, internal.ip));
      if (!wholeExcluded && o.reflection) return true;
    }
    return false;
  })();

  const reflectionEnabled = hasManagement && (
    (snapshot.nat?.portForwards ?? []).some((pf) => {
      if (!pf.enabled || pf.noRedirect) return false;
      if (pf.reflection) return true;
      if (pf.toNegated) {
        const six = pf.family === 'ipv6';
        const targetIp = pf.target?.address ? parseIp(pf.target.address) : null;
        const local = six || Boolean(pf.target?.self) || Boolean(targetIp && isLocalTarget4(targetIp));
        return !local;
      }
      return false;
    })
    || hasActiveOneToOneReflection
  );

  // Stage 1: Ingress
  const ingressRef: DecidingRef = { label: `Interface ${ingressSegment.name}`, to: '/network/interfaces' };
  steps.push({
    stage: 'ingress',
    title: 'Ingress',
    summary: `Enters on ${ingressSegment.name}${ingressLink && ingressLink.name !== ingressSegment.name ? ` (${ingressLink.name})` : ''} in zone ${ingressZone?.name ?? ingressSegment.zone}.`,
    outcome: !ingressUsable ? 'drop' : 'pass',
    decidedBy: ingressRef,
    zone: ingressZoneInfo,
    detail: originalTuple,
  });

  if (!ingressUsable) {
    steps.push({
      stage: 'verdict',
      title: 'Verdict',
      summary: `Ingress interface ${ingressSegment.name} is disabled; traffic cannot enter this interface.`,
      outcome: 'drop',
      decidedBy: ingressRef,
      detail: originalTuple,
    });
    return {
      verdict: 'drop',
      verdictLabel: 'Interface disabled',
      chain: 'none',
      originalTuple,
      finalTuple: originalTuple,
      decidedBy: ingressRef,
      warnings,
      steps,
    };
  }

  // Stage 2: Pre-routing NAT (Port forwards, then 1:1 NAT DNAT)
  let effectiveDstIp = origDstIp;
  let effectiveDstPort = origDstPort;
  let matchedPortForward: { pf: PortForward; index: number; isLocalTarget: boolean; toSelf: boolean } | undefined;
  let preroutingHandled = false;
  let didDnat = false;

  if (hasManagement && ingressUsable) {
    const enabledPrefixList = usableSegmentPrefixes.map((e) => e.prefix);
    const portForwards = snapshot.nat?.portForwards ?? [];
    for (let i = 0; i < portForwards.length; i += 1) {
      const pf = portForwards[i];
      if (!pf.enabled) continue;
      const pfFamily: 4 | 6 = pf.family === 'ipv6' ? 6 : 4;
      if (pfFamily !== origDstIp.family) continue;
      if (probe.protocol !== 'tcp' && probe.protocol !== 'udp') continue;
      if (pf.protocol !== 'tcp-udp' && pf.protocol !== probe.protocol) continue;
      const span = parsePortSpan(pf.ports);
      if (!span || origDstPort === undefined || origDstPort < span.first || origDstPort > span.last) continue;
      if (!matchesAddressList(pf.from, Boolean(pf.fromNegated), srcIp, addressesById)) continue;

      const normalIngress = pf.ingressZones.includes(ingressSegment.zone);
      const reflectionIngress = Boolean(pf.reflection) && !pf.noRedirect && pfFamily === 4 && ingressIsInternal;
      if (!normalIngress && !reflectionIngress) continue;

      const checkTo = (viaReflection: boolean): boolean => {
        if (pf.toNegated) {
          if (isBroadcastOrMulticast(origDstIp, enabledPrefixList)) return false;
          if (pf.to.self) return !findLocalOwner(origDstIp);
          return !matchesAddressAlias(pf.to.address, origDstIp, addressesById);
        }
        if (viaReflection) {
          if (pf.to.self) return Boolean(findLocalOwner(origDstIp)) && !internalOwn4.has(origDstIp.value);
          return matchesAddressAlias(pf.to.address, origDstIp, addressesById) && !virtual4.has(origDstIp.value);
        }
        if (pf.to.self) return Boolean(findLocalOwner(origDstIp));
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
        // Matches nat.go:114-115 + nft.go:121-123: `accept` stops prerouting chain (both later forwards and 1:1 DNAT).
        steps.push({
          stage: 'prerouting',
          title: 'Pre-routing NAT',
          summary: `Matched no-redirect exception "${pf.name}"; destination address and port are unchanged.`,
          outcome: 'pass',
          decidedBy: ref,
        });
        preroutingHandled = true;
        break;
      }

      const newPort = pf.targetPort !== undefined ? pf.targetPort + (origDstPort - span.first) : origDstPort;
      if (pf.target?.self) {
        // Linux netfilter `redirect` rewrites destination IP to the ingress interface's primary IP (drops if IPv4 is absent on ingress).
        const ingressPrimary = localOwners.find((o) => o.owner.segmentId === ingressSegment.id && !o.owner.isVirtual && !o.owner.linkLocal && o.ip.family === origDstIp.family)?.ip;
        if (!ingressPrimary && origDstIp.family === 4) {
          if (ingressSegment.addressing.ipv4.mode === 'none') {
            steps.push({
              stage: 'prerouting',
              title: 'Pre-routing NAT',
              summary: `Redirect "${pf.name}" dropped: ingress interface ${ingressSegment.name} has no IPv4 address.`,
              outcome: 'drop',
              decidedBy: ref,
            });
            steps.push({
              stage: 'verdict',
              title: 'Verdict',
              summary: `Dropped by Linux netfilter redirect because ${ingressSegment.name} has no IPv4 address.`,
              outcome: 'drop',
              decidedBy: ref,
              detail: originalTuple,
            });
            return { verdict: 'drop', verdictLabel: 'Dropped', chain: 'none', originalTuple, finalTuple: originalTuple, decidedBy: ref, warnings, steps };
          }
          warnings.push(`Ingress interface ${ingressSegment.name} has no known live IPv4 address; redirect target address is dynamic.`);
        }
        if (ingressPrimary) effectiveDstIp = ingressPrimary;
        effectiveDstPort = newPort;
        matchedPortForward = { pf, index: i, isLocalTarget: true, toSelf: true };
        preroutingHandled = true;
        didDnat = true;
        steps.push({
          stage: 'prerouting',
          title: 'Pre-routing NAT',
          summary: `${usedReflection ? 'Reflected and redirected' : 'Redirected'} to this firewall${ingressPrimary ? ` (${ingressPrimary.canonical})` : ' (dynamic ingress address)'}${newPort !== origDstPort ? ` on port ${newPort}` : ''}.`,
          outcome: 'transform',
          decidedBy: ref,
          detail: ingressPrimary ? `${formatEndpoint(origDstIp, origDstPort)} → ${formatEndpoint(ingressPrimary, newPort)}` : `${formatEndpoint(origDstIp, origDstPort)} → firewall:${newPort}`,
        });
        break;
      }
      if (pf.target?.address) {
        const targetIp = parseIp(pf.target.address);
        if (targetIp) {
          effectiveDstIp = targetIp;
          effectiveDstPort = newPort;
          const isLocalTarget = pfFamily === 6 || isLocalTarget4(targetIp);
          matchedPortForward = { pf, index: i, isLocalTarget, toSelf: false };
          preroutingHandled = true;
          didDnat = true;
          steps.push({
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

    if (!preroutingHandled) {
      const inboundOneToOne = matchOneToOne(
        snapshot.nat?.oneToOne ?? [],
        'inbound',
        ingressSegment.zone,
        ingressIsInternal,
        origDstIp,
        srcIp,
        addressesById,
      );
      if (inboundOneToOne) {
        effectiveDstIp = inboundOneToOne.mappedIp;
        preroutingHandled = true;
        didDnat = true;
        steps.push({
          stage: 'prerouting',
          title: 'Pre-routing NAT',
          summary: `1:1 NAT${inboundOneToOne.reflected ? ' reflection' : ''} translated destination to ${effectiveDstIp.canonical}.`,
          outcome: 'transform',
          decidedBy: { label: `1:1 NAT #${inboundOneToOne.index + 1} (${inboundOneToOne.name})`, to: '/firewall/nat' },
          detail: `${origDstIp.canonical} → ${effectiveDstIp.canonical}`,
        });
      }
    }
  }

  if (!preroutingHandled) {
    steps.push({
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
  let connectedRoutePrefix: ParsedPrefix | undefined;

  const ingressPrefixes = usableSegmentPrefixes.filter((e) => e.segment.id === ingressSegment.id).map((e) => e.prefix);
  const isIngressBroadcast = effectiveDstIp.family === 4 && (
    effectiveDstIp.value === ipv4Max
    || ingressPrefixes.some((p) => p.family === 4 && p.bits <= 30 && effectiveDstIp.value === (p.network | ((1n << BigInt(32 - p.bits)) - 1n)))
  );
  const isLocalMulticast = isLinkLocalMulticast(effectiveDstIp);

  const localOwner = matchedPortForward?.toSelf
    ? { label: `This firewall (${ingressSegment.name})`, to: '/network/interfaces', segmentId: ingressSegment.id, isVirtual: false }
    : findLocalOwner(effectiveDstIp);

  const stopUnroutable = (
    summary: string,
    verdictSummary: string,
    decidedBy: DecidingRef = { label: 'Routing table (Interfaces)', to: '/network/interfaces' },
  ): TrafficSimulation => {
    const finalTuple = formatTuple(probe.protocol, srcIp, srcPort, effectiveDstIp, effectiveDstPort, icmpType);
    steps.push({ stage: 'routing', title: 'Routing', summary, outcome: 'drop', decidedBy });
    steps.push({ stage: 'verdict', title: 'Verdict', summary: verdictSummary, outcome: 'drop', decidedBy, detail: finalTuple });
    return { verdict: 'no_route', verdictLabel: 'No route', chain: 'none', originalTuple, finalTuple, decidedBy, warnings, steps };
  };

  if (localOwner || isIngressBroadcast || isLocalMulticast) {
    chain = 'input';
    egressZoneId = firewallZone;
    const routeDecidedBy = localOwner
      ? { label: localOwner.label, to: localOwner.to }
      : { label: `Interface ${ingressSegment.name}`, to: '/network/interfaces' };
    const routeSummary = localOwner
      ? `Delivered locally to this firewall (${localOwner.label}).`
      : isIngressBroadcast
        ? `Local broadcast on ${ingressSegment.name}; delivered to this firewall.`
        : `Link-local multicast on ${ingressSegment.name}; delivered to this firewall.`;
    steps.push({
      stage: 'routing',
      title: 'Routing',
      summary: routeSummary,
      outcome: 'pass',
      decidedBy: routeDecidedBy,
      isFirewall: true,
      detail: 'Chain: input',
    });
  } else {
    let bestConnected: { segment: Segment; prefix: ParsedPrefix } | undefined;
    for (const entry of usableSegmentPrefixes) {
      if (!prefixContains(entry.prefix, effectiveDstIp)) continue;
      if (!bestConnected || entry.prefix.bits > bestConnected.prefix.bits) {
        bestConnected = { segment: entry.segment, prefix: entry.prefix };
      }
    }
    // Active DHCPv6 prefix-delegation lease routes (internal/generation/pdroutes.go:243-293) and
    // unreachable pool routes (internal/render/networkd.go:221-227).
    let bestDelegated: { segment: Segment; prefix: ParsedPrefix; hostname: string } | undefined;
    let bestUnreachable: { segment: Segment; entryName: string; prefix: ParsedPrefix } | undefined;
    if (effectiveDstIp.family === 6) {
      const dhcp6BySegment = new Map<string, Array<{ name: string; pool?: ParsedPrefix }>>();
      for (const e of snapshot.dhcp6 ?? []) {
        if (!e.prefixDelegation) continue;
        const seg = segmentsById.get(e.segment);
        if (!seg || !isLinkUsable(seg.link, linksById)) continue;
        const pool = e.prefixDelegation.prefix ? parsePrefix(e.prefixDelegation.prefix) : null;
        if (pool && pool.family === 6) {
          if (prefixContains(pool, effectiveDstIp) && (!bestUnreachable || pool.bits > bestUnreachable.prefix.bits)) {
            bestUnreachable = { segment: seg, entryName: e.name, prefix: pool };
          }
          dhcp6BySegment.set(e.segment, [...(dhcp6BySegment.get(e.segment) ?? []), { name: e.name, pool }]);
        } else if (!e.prefixDelegation.prefix) {
          dhcp6BySegment.set(e.segment, [...(dhcp6BySegment.get(e.segment) ?? []), { name: e.name }]);
        }
      }
      for (const lease of options.liveLeases6 ?? []) {
        if (lease.type !== 'prefix' || lease.state !== 'active') continue;
        const seg = segmentsById.get(lease.segment);
        const pools = dhcp6BySegment.get(lease.segment);
        if (!seg || !pools) continue;
        const p = parsePrefix(lease.address);
        if (!p || p.family !== 6 || !prefixContains(p, effectiveDstIp)) continue;
        const servedByPool = pools.some((entry) => !entry.pool || (p.bits >= entry.pool.bits && prefixContains(entry.pool, p.ip)));
        if (!servedByPool) continue;
        if (!bestDelegated || p.bits > bestDelegated.prefix.bits) {
          bestDelegated = { segment: seg, prefix: p, hostname: lease.hostname };
        }
      }
    }
    // Longest-prefix match: connected > active PD lease (replaces equal-length pool unreachable per pdroutes.go:278) > unreachable pool.
    const delegatedBeatsConnected = Boolean(bestDelegated && (!bestConnected || bestDelegated.prefix.bits > bestConnected.prefix.bits));
    const unreachableWins = Boolean(
      bestUnreachable
      && (!bestConnected || bestUnreachable.prefix.bits > bestConnected.prefix.bits)
      && (!bestDelegated || bestUnreachable.prefix.bits > bestDelegated.prefix.bits),
    );
    if (unreachableWins && bestUnreachable) {
      if (options.liveLeases6 === undefined) {
        warnings.push(`Active DHCPv6 prefix-delegation leases were unavailable; assuming ${effectiveDstIp.canonical} inside ${bestUnreachable.prefix.raw} is unleased.`);
      }
      return stopUnroutable(
        `Matched unreachable route ${bestUnreachable.prefix.raw} for DHCPv6 delegation pool "${bestUnreachable.entryName}" on ${bestUnreachable.segment.name}.`,
        `Destination falls in unleased DHCPv6 delegation pool ${bestUnreachable.prefix.raw} (unreachable route).`,
        { label: `DHCPv6 ${bestUnreachable.entryName}`, to: '/network/dhcp' },
      );
    }
    if (delegatedBeatsConnected && bestDelegated) {
      if ((isLinkLocalIpv6(srcIp) || isLinkLocalIpv6(effectiveDstIp)) && bestDelegated.segment.id !== ingressSegment.id) {
        return stopUnroutable(
          `IPv6 link-local traffic cannot be forwarded from ${ingressSegment.name} to ${bestDelegated.segment.name} (RFC 4291).`,
          'Packet cannot be forwarded across links (link-local scope).',
        );
      }
      if (options.evaluatingSavedDiffersFromLive) {
        warnings.push(`Delegated prefix ${bestDelegated.prefix.raw} is read from active live DHCPv6 leases.`);
      }
      chain = 'forward';
      egressSegment = bestDelegated.segment;
      egressZoneId = egressSegment.zone;
      const ez = zonesById.get(egressZoneId);
      steps.push({
        stage: 'routing',
        title: 'Routing',
        summary: `Delegated IPv6 prefix ${bestDelegated.prefix.raw}${bestDelegated.hostname ? ` (${bestDelegated.hostname})` : ''} via ${egressSegment.name} (zone ${ez?.name ?? egressZoneId}).`,
        outcome: 'pass',
        decidedBy: { label: `DHCPv6 PD lease (${egressSegment.name})`, to: '/network/dhcp' },
        zone: ez ? { name: ez.name, color: ez.color } : undefined,
        detail: 'Chain: forward',
      });
    } else if (bestConnected) {
      if ((isLinkLocalIpv6(srcIp) || isLinkLocalIpv6(effectiveDstIp)) && bestConnected.segment.id !== ingressSegment.id) {
        return stopUnroutable(
          `IPv6 link-local traffic cannot be forwarded from ${ingressSegment.name} to ${bestConnected.segment.name} (RFC 4291).`,
          'Packet cannot be forwarded across links (link-local scope).',
        );
      }
      chain = 'forward';
      egressSegment = bestConnected.segment;
      egressZoneId = egressSegment.zone;
      connectedRoutePrefix = bestConnected.prefix;
      const ez = zonesById.get(egressZoneId);
      steps.push({
        stage: 'routing',
        title: 'Routing',
        summary: `Directly connected subnet ${bestConnected.prefix.raw} on ${egressSegment.name} (zone ${ez?.name ?? egressZoneId}).`,
        outcome: 'pass',
        decidedBy: { label: `Interface ${egressSegment.name}`, to: '/network/interfaces' },
        zone: ez ? { name: ez.name, color: ez.color } : undefined,
        detail: 'Chain: forward',
      });
    } else {
      if (isLinkLocalIpv6(srcIp) || isLinkLocalIpv6(effectiveDstIp)) {
        return stopUnroutable(
          'IPv6 link-local addresses cannot be forwarded via a default route (RFC 4291).',
          'Packet cannot be forwarded across links (link-local scope).',
        );
      }
      const uplinks = snapshot.segments.filter((s) => {
        if (!isLinkUsable(s.link, linksById)) return false;
        return effectiveDstIp.family === 4 ? isUplink(s, linksById) : isIpv6Uplink(s);
      });
      if (uplinks.length > 0) {
        chain = 'forward';
        egressSegment = uplinks[0];
        egressZoneId = egressSegment.zone;
        const ez = zonesById.get(egressZoneId);
        const gw = effectiveDstIp.family === 4 ? egressSegment.addressing.ipv4.gateway : egressSegment.addressing.ipv6.gateway;
        if (!gw) {
          const hasLiveAddr = localOwners.some((o) => o.owner.segmentId === egressSegment!.id && !o.owner.linkLocal && o.ip.family === effectiveDstIp.family);
          if (!hasLiveAddr) {
            warnings.push(`Uplink ${egressSegment.name} uses dynamic IPv${effectiveDstIp.family} with no observed live address; assuming its lease installs a default route.`);
          }
        }
        if (uplinks.length > 1) {
          warnings.push(`Multiple IPv${effectiveDstIp.family} uplinks exist (${uplinks.map((u) => u.name).join(', ')}); assuming default route via ${egressSegment.name}.`);
        }
        steps.push({
          stage: 'routing',
          title: 'Routing',
          summary: `Default route via ${egressSegment.name}${gw ? ` (gateway ${gw})` : ''} in zone ${ez?.name ?? egressZoneId}.`,
          outcome: 'pass',
          decidedBy: { label: `Interface ${egressSegment.name}`, to: '/network/interfaces' },
          zone: ez ? { name: ez.name, color: ez.color } : undefined,
          detail: 'Chain: forward',
        });
      } else {
        return stopUnroutable(
          `No connected subnet or IPv${effectiveDstIp.family} default gateway reaches ${effectiveDstIp.canonical}.`,
          'Packet cannot be routed (no route to host).',
        );
      }
    }
  }

  // Stage 4: Firewall filter (input or forward chain)
  const filter = evaluateFilter({
    chain,
    hasManagement,
    probe,
    srcIp,
    srcPort,
    effectiveDstIp,
    effectiveDstPort,
    icmpType,
    ingressSegment,
    egressZoneId,
    matchedPortForward,
    snapshot,
    linksById,
    addressesById,
    portsById,
  });
  steps.push({
    stage: 'filter',
    title: `Firewall (${chain})`,
    summary: filter.summary,
    outcome: filter.outcome,
    decidedBy: filter.decidedBy,
  });

  // Stage 5: Post-routing NAT (only for accepted forwarded traffic)
  let effectiveSrcIp = srcIp;
  let snatNote = '';
  if (chain === 'forward' && filter.verdict === 'accept' && egressSegment && hasManagement) {
    let snatApplied = false;

    const outboundOneToOne = matchOneToOne(
      snapshot.nat?.oneToOne ?? [],
      'outbound',
      egressSegment.zone,
      false,
      srcIp,
      effectiveDstIp,
      addressesById,
    );
    if (outboundOneToOne) {
      effectiveSrcIp = outboundOneToOne.mappedIp;
      snatApplied = true;
      steps.push({
        stage: 'postrouting',
        title: 'Post-routing NAT',
        summary: `1:1 NAT translated source address to ${effectiveSrcIp.canonical}.`,
        outcome: 'transform',
        decidedBy: { label: `1:1 NAT #${outboundOneToOne.index + 1} (${outboundOneToOne.name})`, to: '/firewall/nat' },
        detail: `${srcIp.canonical} → ${effectiveSrcIp.canonical}`,
      });
    }

    // 5b: Hairpin NAT reflection masquerade (nft.go:124-131): eligible on source prefix match, source IP from dst next-hop route.
    if (!snatApplied && reflectionEnabled && didDnat && srcIp.family === 4 && !isUplink(egressSegment, linksById) && egressSegment.addressing.ipv4.mode === 'static') {
      const segPrefix = usableSegmentPrefixes.find((e) => e.segment.id === egressSegment.id && e.staticIpv4 && prefixContains(e.prefix, srcIp));
      if (segPrefix) {
        effectiveSrcIp = connectedRoutePrefix?.family === 4 ? connectedRoutePrefix.ip : segPrefix.prefix.ip;
        snatApplied = true;
        steps.push({
          stage: 'postrouting',
          title: 'Post-routing NAT',
          summary: `Hairpin reflection masqueraded source to ${effectiveSrcIp.canonical} on ${egressSegment.name}.`,
          outcome: 'transform',
          decidedBy: { label: 'NAT reflection masquerade', to: '/firewall/nat' },
          detail: `${srcIp.canonical} → ${effectiveSrcIp.canonical}`,
        });
      }
    }

    // 5c: Outbound automatic masquerade (nft.go:170-173): select source IP from the egress route's next hop (connected subnet or gateway).
    if (!snatApplied && srcIp.family === 4 && snapshot.nat?.outbound?.mode === 'automatic' && isUplink(egressSegment, linksById)) {
      let egressAddr: ParsedIp | undefined = connectedRoutePrefix?.family === 4 ? connectedRoutePrefix.ip : undefined;
      if (!egressAddr && egressSegment.addressing.ipv4.gateway) {
        const gwIp = parseIp(egressSegment.addressing.ipv4.gateway);
        if (gwIp?.family === 4) {
          let bestPrefix: ParsedPrefix | null = null;
          for (const entry of usableSegmentPrefixes) {
            if (entry.segment.id !== egressSegment.id || entry.prefix.family !== 4) continue;
            if (!prefixContains(entry.prefix, gwIp)) continue;
            if (!bestPrefix || entry.prefix.bits > bestPrefix.bits) {
              bestPrefix = entry.prefix;
            }
          }
          if (bestPrefix) egressAddr = bestPrefix.ip;
        }
      }
      if (!egressAddr) {
        egressAddr = localOwners.find((o) => o.owner.segmentId === egressSegment.id && !o.owner.isVirtual && !o.owner.linkLocal && o.ip.family === 4)?.ip;
      }
      snatApplied = true;
      if (egressAddr) {
        effectiveSrcIp = egressAddr;
      } else {
        snatNote = ` (masqueraded to ${egressSegment.name} IPv4)`;
      }
      steps.push({
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
        stage: 'postrouting',
        title: 'Post-routing NAT',
        summary: 'No source NAT applied; original source address is preserved.',
        outcome: 'info',
      });
    }
  }

  // Stage 6: Final verdict
  const finalTuple = formatTuple(probe.protocol, effectiveSrcIp, srcPort, effectiveDstIp, effectiveDstPort, icmpType) + snatNote;
  const verdictLabel = filter.verdict === 'accept'
    ? 'Allowed'
    : filter.verdict === 'reject'
      ? (filter.rejectTcpReset ? 'Rejected (TCP RST)' : 'Rejected')
      : 'Dropped';

  const verdictSummary = filter.verdict === 'accept'
    ? (chain === 'input'
      ? `Traffic is delivered to this firewall${filter.logged ? ' (logged)' : ''}.`
      : `Traffic is forwarded out ${egressSegment?.name ?? ''}${filter.logged ? ' (logged)' : ''}.`)
    : filter.verdict === 'reject'
      ? `Traffic is rejected${filter.rejectTcpReset ? ' with TCP RST' : ''}${filter.logged ? ' (logged)' : ''}.`
      : `Traffic is silently dropped${filter.logged ? ' (logged)' : ''}.`;

  steps.push({
    stage: 'verdict',
    title: 'Verdict',
    summary: verdictSummary,
    outcome: filter.verdict,
    decidedBy: filter.decidedBy,
    detail: finalTuple,
  });

  return {
    verdict: filter.verdict,
    verdictLabel,
    chain,
    originalTuple,
    finalTuple,
    decidedBy: filter.decidedBy,
    warnings,
    steps,
  };
}
