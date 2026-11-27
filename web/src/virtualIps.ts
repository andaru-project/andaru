import { parseIp, parsePrefix, prefixContains, type ParsedPrefix } from './trafficFlow';
import type { SavedObject, Segment, Snapshot, VirtualAddress, VirtualAddressKind } from './types';

export interface VirtualIpUse {
  key: string;
  label: string;
  to: string;
  locksAddress: boolean;
}

export interface StaticSubnet {
  cidr: string;
  prefix: ParsedPrefix;
}

export const maxVirtualAddresses = 64;

export const virtualAddressKindLabel: Record<VirtualAddressKind, string> = {
  address: 'Held address',
  proxyArp: 'Proxy ARP',
  proxyNdp: 'Proxy NDP',
};

export function virtualAddressKind(vip: Pick<VirtualAddress, 'kind'>): VirtualAddressKind {
  if (vip.kind === 'proxyArp') return 'proxyArp';
  if (vip.kind === 'proxyNdp') return 'proxyNdp';
  return 'address';
}

function prefixesOverlap(a: ParsedPrefix, b: ParsedPrefix): boolean {
  if (a.family !== b.family) return false;
  const mask = a.bits <= b.bits ? a.mask : b.mask;
  return (a.network & mask) === (b.network & mask);
}

function prefixContainsPrefix(outer: ParsedPrefix, inner: ParsedPrefix): boolean {
  return outer.family === inner.family && outer.bits <= inner.bits && (inner.network & outer.mask) === outer.network;
}

function isSinglePrefix(prefix: ParsedPrefix): boolean {
  return prefix.bits === (prefix.family === 4 ? 32 : 128);
}

export function virtualAddressFamily(vip: Pick<VirtualAddress, 'address' | 'kind'>): 'IPv4' | 'IPv6' | undefined {
  const kind = virtualAddressKind(vip);
  if (kind === 'proxyNdp') {
    return parseIp(vip.address)?.family === 6 ? 'IPv6' : undefined;
  }
  const parsed = parsePrefix(vip.address);
  if (!parsed) return undefined;
  if (kind === 'proxyArp') {
    return parsed.family === 4 ? 'IPv4' : undefined;
  }
  return parsed.family === 4 ? 'IPv4' : 'IPv6';
}

// Single held address (/32 IPv4 or /128 IPv6) eligible as a DNS listener.
export function isSingleVirtualAddress(vip: Pick<VirtualAddress, 'address' | 'kind'>): boolean {
  if (virtualAddressKind(vip) !== 'address') return false;
  const parsed = parsePrefix(vip.address);
  return parsed !== null && isSinglePrefix(parsed);
}

function parseSubnets(addresses: string[] | undefined, family: 4 | 6): StaticSubnet[] {
  const out: StaticSubnet[] = [];
  for (const raw of addresses ?? []) {
    if (!raw.includes('/')) continue;
    const prefix = parsePrefix(raw);
    if (prefix && prefix.family === family) out.push({ cidr: raw, prefix });
  }
  return out;
}

export function staticIpv4Subnets(segment: Segment): StaticSubnet[] {
  return segment.addressing.ipv4.mode === 'static' ? parseSubnets(segment.addressing.ipv4.addresses, 4) : [];
}

export function staticIpv6Subnets(segment: Segment): StaticSubnet[] {
  return segment.addressing.ipv6.mode === 'static' ? parseSubnets(segment.addressing.ipv6.addresses, 6) : [];
}

function staticSegmentPrefixes(segment: Segment): ParsedPrefix[] {
  return [
    ...staticIpv4Subnets(segment),
    ...staticIpv6Subnets(segment),
    ...(segment.addressing.ipv6.mode === 'delegated' ? parseSubnets(segment.addressing.ipv6.addresses, 6) : []),
  ].map((sub) => sub.prefix);
}

// A /32 static address leaves no room for another address on the subnet (the only IP is the segment's own).
export function isProxyArpEligibleSegment(segment: Segment): boolean {
  return staticIpv4Subnets(segment).some((sub) => sub.prefix.bits < 32);
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
  if ((val >> 32n) === 0xffffn) return false; // ::ffff:0:0/96 IPv4-mapped
  if (((val >> 118n) & 0x3ffn) === 0x3fan) return false; // fe80::/10 link-local unicast
  if (((val >> 120n) & 0xffn) === 0xffn) return false; // ff00::/8 multicast
  return true;
}

// Global unicast IPv6: matches Go netip.Addr.IsGlobalUnicast() && !IsPrivate() && !Is4In6() (rejects fc00::/7 ULA too).
function isGlobalUnicastIpv6(val: bigint): boolean {
  return isAllowedIpv6Value(val) && ((val >> 121n) & 0x7fn) !== 0x7en;
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
    hostSegment?: string;
    segments?: Segment[];
  },
): { errors: Map<string, string>; duplicate: boolean; stale: boolean } {
  const kind: VirtualAddressKind = virtualAddressKind({ kind: options?.kind });
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

  if (kind === 'proxyNdp') {
    const segmentId = (options?.segment ?? '').trim();
    const hostSegmentId = (options?.hostSegment ?? '').trim();
    const chosenSegment = segments?.find((s) => s.id === segmentId);
    const chosenHostSegment = segments?.find((s) => s.id === hostSegmentId);

    if (!segmentId) {
      errors.set('segment', 'Choose the uplink segment that answers neighbour discovery.');
    } else if (segments && !chosenSegment) {
      errors.set('segment', 'Choose an existing uplink segment.');
      stale = true;
    }

    if (!hostSegmentId) {
      errors.set('hostSegment', 'Choose the segment where the host lives.');
    } else if (segments && !chosenHostSegment) {
      errors.set('hostSegment', 'Choose an existing host segment.');
      stale = true;
    } else if (segmentId && hostSegmentId === segmentId) {
      errors.set('hostSegment', 'Host segment must differ from the uplink segment.');
    }

    if (!trimmedAddress) {
      errors.set('address', 'A proxy NDP entry needs a global unicast IPv6 address.');
    } else if (trimmedAddress.includes('%')) {
      errors.set('address', 'Enter an address without an interface zone.');
    } else {
      const parsed = parseIp(trimmedAddress);
      if (!parsed || parsed.family !== 6 || !isGlobalUnicastIpv6(parsed.value)) {
        errors.set('address', 'Expected one global unicast IPv6 address, without a length.');
      } else {
        const uplinkSubnets = chosenSegment ? staticIpv6Subnets(chosenSegment) : [];
        if (uplinkSubnets.length > 0 && !uplinkSubnets.some((sub) => prefixContains(sub.prefix, parsed))) {
          errors.set('address', "Expected an address on one of the segment's static IPv6 subnets.");
          stale = true;
        }
        if (!errors.has('address') && segments) {
          const hitsStatic = segments.some((seg) => staticSegmentPrefixes(seg).some((sub) => sub.family === 6 && sub.ip.value === parsed.value));
          if (hitsStatic) {
            errors.set('address', 'Proxy NDP cannot use a static address held by a segment.');
            stale = true;
          }
        }
        if (!errors.has('address') && segments) {
          const hitsGateway = segments.some((seg) => {
            if (seg.addressing.ipv6.mode !== 'static') return false;
            const gw = seg.addressing.ipv6.gateway ? parseIp(seg.addressing.ipv6.gateway) : null;
            return gw !== null && gw.family === 6 && gw.value === parsed.value;
          });
          if (hitsGateway) {
            errors.set('address', "This address is a segment's gateway.");
            stale = true;
          }
        }
        if (!errors.has('address') && chosenHostSegment) {
          const hostSubnets = staticSegmentPrefixes(chosenHostSegment).filter((sub) => sub.family === 6);
          if (hostSubnets.some((sub) => prefixContains(sub, parsed))) {
            errors.set('address', "This address is already on the host segment's subnet.");
            stale = true;
          }
        }
        if (!errors.has('address')) {
          for (const other of others) {
            const otherPrefix = parsePrefix(other.value.address);
            if (!otherPrefix || !prefixContains(otherPrefix, parsed)) continue;
            const otherKind = virtualAddressKind(other.value);
            if (otherKind === 'proxyNdp') {
              errors.set('address', 'Another proxy NDP entry already has this address.');
            } else {
              errors.set('address', 'This address overlaps an existing virtual IP.');
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

  if (kind === 'proxyArp') {
    const segmentId = (options?.segment ?? '').trim();
    const chosenSegment = segments?.find((s) => s.id === segmentId);
    const chosenSubnets = chosenSegment ? staticIpv4Subnets(chosenSegment).filter((sub) => sub.prefix.bits < 32) : [];

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
        errors.set('address', 'Proxy ARP is IPv4 only; use Proxy NDP for IPv6.');
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
                : 'This address or prefix overlaps an existing held virtual IP.',
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
        const parsedSingle = isSinglePrefix(parsed);
        for (const other of others) {
          const otherPrefix = parsePrefix(other.value.address);
          if (!otherPrefix || !prefixesOverlap(parsed, otherPrefix)) continue;
          const otherKind = virtualAddressKind(other.value);
          if (otherKind === 'proxyArp') {
            errors.set('address', 'This address or prefix overlaps a proxy ARP virtual IP.');
          } else if (otherKind === 'proxyNdp') {
            errors.set('address', 'This address or prefix overlaps a proxy NDP virtual IP.');
          } else if (parsedSingle && isSinglePrefix(otherPrefix)) {
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
