import { parseIp, parsePrefix, prefixContains, type ParsedPrefix } from './trafficFlow';
import type { SavedObject, Segment, Snapshot, VirtualAddress, VirtualAddressKind } from './types';

export interface VirtualIpUse {
  key: string;
  label: string;
  to: string;
  locksAddress: boolean;
}

export interface StaticIpv4Subnet {
  cidr: string;
  prefix: ParsedPrefix;
  bits: number;
}

export const maxVirtualAddresses = 64;

export const virtualAddressKindLabel: Record<VirtualAddressKind, string> = {
  address: 'Held address',
  proxyArp: 'Proxy ARP',
};

export function virtualAddressKind(vip: Pick<VirtualAddress, 'kind'>): VirtualAddressKind {
  return vip.kind === 'proxyArp' ? 'proxyArp' : 'address';
}

function prefixesOverlap(a: ParsedPrefix, b: ParsedPrefix): boolean {
  if (a.family !== b.family) return false;
  const mask = a.bits <= b.bits ? a.mask : b.mask;
  return (a.network & mask) === (b.network & mask);
}

function prefixContainsPrefix(outer: ParsedPrefix, inner: ParsedPrefix): boolean {
  return outer.family === inner.family && outer.bits <= inner.bits && (inner.network & outer.mask) === outer.network;
}

export function ipFamily(address: string): 'IPv4' | 'IPv6' | undefined {
  const parsed = parseIp(address);
  if (!parsed) return undefined;
  return parsed.family === 4 ? 'IPv4' : 'IPv6';
}

export function virtualAddressFamily(vip: Pick<VirtualAddress, 'address' | 'kind'>): 'IPv4' | 'IPv6' | undefined {
  const parsed = parsePrefix(vip.address);
  if (!parsed) return undefined;
  if (virtualAddressKind(vip) === 'proxyArp') {
    return parsed.family === 4 ? 'IPv4' : undefined;
  }
  return parsed.family === 4 ? 'IPv4' : 'IPv6';
}

// Single held address (/32 IPv4 or /128 IPv6) eligible as a DNS listener.
export function isSingleVirtualAddress(vip: Pick<VirtualAddress, 'address' | 'kind'>): boolean {
  if (virtualAddressKind(vip) !== 'address') return false;
  const parsed = parsePrefix(vip.address);
  if (!parsed) return false;
  return parsed.bits === (parsed.family === 4 ? 32 : 128);
}

export function staticIpv4Subnets(segment: Segment): StaticIpv4Subnet[] {
  if (segment.addressing.ipv4.mode !== 'static') return [];
  const out: StaticIpv4Subnet[] = [];
  for (const raw of segment.addressing.ipv4.addresses ?? []) {
    if (!raw.includes('/')) continue;
    const prefix = parsePrefix(raw);
    if (!prefix || prefix.family !== 4) continue;
    out.push({ cidr: raw, prefix, bits: prefix.bits });
  }
  return out;
}

function staticSegmentPrefixes(segment: Segment): ParsedPrefix[] {
  const out: ParsedPrefix[] = [];
  if (segment.addressing.ipv4.mode === 'static') {
    for (const raw of segment.addressing.ipv4.addresses ?? []) {
      if (!raw.includes('/')) continue;
      const prefix = parsePrefix(raw);
      if (prefix && prefix.family === 4) out.push(prefix);
    }
  }
  if (segment.addressing.ipv6.mode === 'static' || segment.addressing.ipv6.mode === 'delegated') {
    for (const raw of segment.addressing.ipv6.addresses ?? []) {
      if (!raw.includes('/')) continue;
      const prefix = parsePrefix(raw);
      if (prefix && prefix.family === 6) out.push(prefix);
    }
  }
  return out;
}

// A /32 static address leaves no room for another address on the subnet (the only IP is the segment's own).
export function isProxyArpEligibleSegment(segment: Segment): boolean {
  return staticIpv4Subnets(segment).some((sub) => sub.bits < 32);
}

function isAllowedIpv4Value(val: bigint): boolean {
  const o0 = Number((val >> 24n) & 0xffn);
  const o1 = Number((val >> 16n) & 0xffn);
  if (val === 0n) return false; // 0.0.0.0 unspecified
  if (o0 === 127) return false; // 127.0.0.0/8 loopback
  if (o0 === 169 && o1 === 254) return false; // 169.254.0.0/16 link-local unicast
  if ((o0 & 0xf0) === 0xe0) return false; // 224.0.0.0/4 multicast
  if (val === 0xffffffffn) return false; // 255.255.255.255 limited broadcast
  return true;
}

function isAllowedIpv6Value(val: bigint): boolean {
  if (val === 0n) return false; // :: unspecified
  if (val === 1n) return false; // ::1 loopback
  if (((val >> 118n) & 0x3ffn) === 0x3fan) return false; // fe80::/10 link-local unicast
  if (((val >> 120n) & 0xffn) === 0xffn) return false; // ff00::/8 multicast
  return true;
}

// Matches internal/config/virtual.go: validUnicast(a) && !a.IsLinkLocalUnicast() && !a.Is4In6() across the prefix.
function isAllowedVirtualPrefix(prefix: ParsedPrefix): boolean {
  const maxBits = prefix.family === 4 ? 32 : 128;
  const last = prefix.network | ((1n << BigInt(maxBits - prefix.bits)) - 1n);
  if (prefix.family === 4) {
    return isAllowedIpv4Value(prefix.ip.value) && isAllowedIpv4Value(prefix.network) && isAllowedIpv4Value(last);
  }
  return isAllowedIpv6Value(prefix.ip.value) && isAllowedIpv6Value(prefix.network) && isAllowedIpv6Value(last);
}

// Compare two bare IP addresses regardless of IPv6 casing or zero compression.
export function sameIpAddress(a: string, b: string): boolean {
  const pa = parseIp(a);
  if (!pa) return false;
  const pb = parseIp(b);
  return pb !== null && pa.family === pb.family && pa.value === pb.value;
}

// Where a saved virtual IP is referenced by DNS or targeted by NAT port forwards.
export function virtualAddressUses(vip: VirtualAddress, snapshot: Snapshot): VirtualIpUse[] {
  const found: VirtualIpUse[] = [];
  if (snapshot.value.dns?.virtualAddresses?.includes(vip.id)) {
    found.push({ key: 'dns', label: 'DNS service', to: '/network/dns', locksAddress: false });
  }
  if (virtualAddressKind(vip) === 'address') {
    const vipPrefix = parsePrefix(vip.address);
    if (vipPrefix) {
      (snapshot.value.nat?.portForwards ?? []).forEach((pf, index) => {
        const targetIp = pf.target?.address ? parseIp(pf.target.address) : null;
        if (targetIp && prefixContains(vipPrefix, targetIp)) {
          found.push({
            key: `pfw:${pf.id}`,
            label: `Port forward ${index + 1} (${pf.name})`,
            to: '/firewall/nat',
            locksAddress: true,
          });
        }
      });
    }
  }
  return found;
}

export function checkVirtualIp(
  name: string,
  address: string,
  existingVips: Array<SavedObject<VirtualAddress>>,
  editingId?: string,
  options?: {
    kind?: VirtualAddressKind;
    segment?: string;
    segments?: Segment[];
  },
): { errors: Map<string, string>; duplicate: boolean; stale: boolean } {
  const kind: VirtualAddressKind = options?.kind === 'proxyArp' ? 'proxyArp' : 'address';
  const trimmedName = name.trim();
  const trimmedAddress = address.trim();
  const errors = new Map<string, string>();
  let duplicate = false;
  let stale = false;

  if (!trimmedName) {
    errors.set('name', 'A virtual IP needs a name.');
  } else if (existingVips.some((v) => v.value.id !== editingId && v.value.name === trimmedName)) {
    errors.set('name', 'Another virtual IP has this name.');
    duplicate = true;
    stale = true;
  }

  const others = existingVips.filter((v) => v.value.id !== editingId);
  const segments = options?.segments;

  if (kind === 'proxyArp') {
    const segmentId = (options?.segment ?? '').trim();
    const chosenSegment = segments?.find((s) => s.id === segmentId);
    const chosenSubnets = chosenSegment ? staticIpv4Subnets(chosenSegment).filter((sub) => sub.bits < 32) : [];

    if (!segmentId) {
      errors.set('segment', 'Choose a segment with a static IPv4 subnet.');
    } else if (segments && chosenSubnets.length === 0) {
      errors.set('segment', 'Choose a segment with a static IPv4 subnet.');
      stale = true;
    }

    if (!trimmedAddress) {
      errors.set('address', 'A proxy ARP entry needs an IPv4 address or prefix.');
    } else if (trimmedAddress.includes('%')) {
      errors.set('address', 'Enter an address without an interface zone.');
    } else {
      const parsed = parsePrefix(trimmedAddress);
      if (parsed && parsed.family === 6) {
        errors.set('address', 'Proxy NDP is not offered yet (#43).');
      } else if (!parsed || parsed.family !== 4 || !isAllowedVirtualPrefix(parsed)) {
        errors.set('address', 'Enter a unicast IPv4 address or a /24–/32 prefix (not loopback, link-local, multicast, or unspecified).');
      } else if (parsed.bits < 24) {
        errors.set('address', 'Proxy ARP prefixes must be between /24 and /32.');
      } else if (trimmedAddress.includes('/') && parsed.ip.value !== parsed.network) {
        errors.set('address', 'Prefix host bits must be zero.');
      } else {
        if (chosenSubnets.length > 0 && !chosenSubnets.some((sub) => prefixContainsPrefix(sub.prefix, parsed))) {
          errors.set('address', "Expected an address on one of the segment's static IPv4 subnets.");
          stale = true;
        }
        if (!errors.has('address') && segments) {
          const hitsStatic = segments.some((seg) => staticIpv4Subnets(seg).some((sub) => prefixContains(parsed, sub.prefix.ip)));
          if (hitsStatic) {
            errors.set('address', 'Proxy ARP cannot include a static address held by a segment.');
            stale = true;
          }
        }
        if (!errors.has('address') && segments) {
          const hitsGateway = segments.some((seg) => {
            if (seg.addressing.ipv4.mode !== 'static') return false;
            const gw = seg.addressing.ipv4.gateway ? parseIp(seg.addressing.ipv4.gateway) : null;
            return gw !== null && gw.family === 4 && prefixContains(parsed, gw);
          });
          if (hitsGateway) {
            errors.set('address', "This range holds a segment's gateway.");
            stale = true;
          }
        }
        if (!errors.has('address')) {
          for (const other of others) {
            const otherPrefix = parsePrefix(other.value.address);
            if (!otherPrefix || !prefixesOverlap(parsed, otherPrefix)) continue;
            errors.set(
              'address',
              virtualAddressKind(other.value) === 'proxyArp'
                ? 'This address or prefix overlaps another proxy ARP entry.'
                : 'This prefix overlaps an existing held virtual IP.',
            );
            duplicate = true;
            stale = true;
            break;
          }
        }
      }
    }

    return { errors, duplicate, stale };
  }

  if (!trimmedAddress) {
    errors.set('address', 'A virtual IP needs an address or prefix.');
  } else if (trimmedAddress.includes('%')) {
    errors.set('address', 'Enter an address without an interface zone.');
  } else {
    const parsed = parsePrefix(trimmedAddress);
    if (!parsed || !isAllowedVirtualPrefix(parsed)) {
      errors.set('address', 'Enter a unicast IPv4 (/24–/32) or IPv6 (/48–/128) address or prefix (not loopback, link-local, multicast, or unspecified).');
    } else if (parsed.family === 4 && parsed.bits < 24) {
      errors.set('address', 'Held IPv4 prefixes must be between /24 and /32.');
    } else if (parsed.family === 6 && parsed.bits < 48) {
      errors.set('address', 'Held IPv6 prefixes must be between /48 and /128.');
    } else if (trimmedAddress.includes('/') && parsed.ip.value !== parsed.network) {
      errors.set('address', 'Prefix host bits must be zero.');
    } else {
      if (segments && segments.some((seg) => staticSegmentPrefixes(seg).some((sub) => prefixesOverlap(parsed, sub)))) {
        errors.set('address', "This address or prefix overlaps a segment's subnet.");
        stale = true;
      }
      if (!errors.has('address')) {
        const parsedSingle = parsed.bits === (parsed.family === 4 ? 32 : 128);
        for (const other of others) {
          const otherPrefix = parsePrefix(other.value.address);
          if (!otherPrefix || !prefixesOverlap(parsed, otherPrefix)) continue;
          if (virtualAddressKind(other.value) === 'proxyArp') {
            errors.set('address', 'This address or prefix overlaps a proxy ARP virtual IP.');
          } else if (parsedSingle && otherPrefix.bits === (otherPrefix.family === 4 ? 32 : 128)) {
            errors.set('address', 'Another virtual IP already has this address.');
          } else {
            errors.set('address', 'This address or prefix overlaps another virtual IP.');
          }
          duplicate = true;
          stale = true;
          break;
        }
      }
    }
  }

  return { errors, duplicate, stale };
}
