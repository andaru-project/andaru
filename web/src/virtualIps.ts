import { parseIpv6Groups } from './dhcpEdit';
import type { SavedObject, Segment, Snapshot, VirtualAddress, VirtualAddressKind } from './types';

export interface VirtualIpUse {
  key: string;
  label: string;
  to: string;
  locksAddress: boolean;
}

export interface StaticIpv4Subnet {
  cidr: string;
  network: number;
  bits: number;
  hostIp: number;
}

export const maxVirtualAddresses = 64;

export const virtualAddressKindLabel: Record<VirtualAddressKind, string> = {
  address: 'Held address',
  proxyArp: 'Proxy ARP',
};

export function virtualAddressKind(vip: Pick<VirtualAddress, 'kind'>): VirtualAddressKind {
  return vip.kind === 'proxyArp' ? 'proxyArp' : 'address';
}

// Parse a bare dotted-quad IPv4 address (no prefix, zone, or leading zeros) into its 4 octets.
function parseIpv4Octets(raw: string): [number, number, number, number] | null {
  const s = raw.trim();
  const parts = s.split('.');
  if (parts.length !== 4) return null;
  const octets: number[] = [];
  for (const part of parts) {
    if (!/^(?:0|[1-9]\d{0,2})$/.test(part)) return null;
    const n = Number(part);
    if (n > 255) return null;
    octets.push(n);
  }
  return octets as [number, number, number, number];
}

function ipv4Number(octets: [number, number, number, number]): number {
  return (((octets[0] << 24) | (octets[1] << 16) | (octets[2] << 8) | octets[3]) >>> 0);
}

function ipv4Mask(bits: number): number {
  return bits === 0 ? 0 : ((~0 << (32 - bits)) >>> 0);
}

interface ParsedIpv4Cidr {
  ip: number;
  network: number;
  bits: number;
  hasSlash: boolean;
}

function parseIpv4Cidr(raw: string): ParsedIpv4Cidr | null {
  const s = raw.trim();
  if (!s || s.includes('%')) return null;
  const slash = s.indexOf('/');
  if (slash < 0) {
    const octets = parseIpv4Octets(s);
    if (!octets) return null;
    const ip = ipv4Number(octets);
    return { ip, network: ip, bits: 32, hasSlash: false };
  }
  const addrPart = s.slice(0, slash);
  const bitsPart = s.slice(slash + 1);
  if (!/^(?:0|[1-9]\d?)$/.test(bitsPart)) return null;
  const bits = Number(bitsPart);
  if (bits > 32) return null;
  const octets = parseIpv4Octets(addrPart);
  if (!octets) return null;
  const ip = ipv4Number(octets);
  return { ip, network: (ip & ipv4Mask(bits)) >>> 0, bits, hasSlash: true };
}

function prefixContainsIpv4(prefix: { network: number; bits: number }, ip: number): boolean {
  return ((ip & ipv4Mask(prefix.bits)) >>> 0) === prefix.network;
}

function prefixContainsPrefixIpv4(outer: { network: number; bits: number }, inner: { network: number; bits: number }): boolean {
  return outer.bits <= inner.bits && prefixContainsIpv4(outer, inner.network);
}

function prefixesOverlapIpv4(a: { network: number; bits: number }, b: { network: number; bits: number }): boolean {
  const mask = ipv4Mask(Math.min(a.bits, b.bits));
  return ((a.network & mask) >>> 0) === ((b.network & mask) >>> 0);
}

// Parse a bare IPv6 address (no prefix, zone, or IPv4-mapped form) into its 8 lowercase canonical hex groups.
function parseBareIpv6Groups(raw: string): string[] | null {
  const s = raw.trim();
  if (!s || s.includes('/') || s.includes('%')) return null;
  const groups = parseIpv6Groups(s);
  if (!groups) return null;
  // Reject pure-hex IPv4-mapped IPv6 (::ffff:0:0/96), matching netip.Addr.Is4In6().
  if (groups.slice(0, 5).every((g) => g === '0') && groups[5] === 'ffff') return null;
  return groups;
}

// Detect any IPv6 address or prefix (including IPv4-mapped forms) for proxy ARP's IPv6 refusal.
function isIpv6Input(raw: string): boolean {
  const s = raw.trim();
  if (!s || s.includes('%') || !s.includes(':')) return false;
  const slash = s.indexOf('/');
  const addrPart = slash >= 0 ? s.slice(0, slash) : s;
  if (slash >= 0) {
    const bitsPart = s.slice(slash + 1);
    if (!/^(?:0|[1-9]\d{0,2})$/.test(bitsPart)) return false;
    if (Number(bitsPart) > 128) return false;
  }
  if (parseIpv6Groups(addrPart) !== null) return true;
  // Handle dotted-quad IPv4-embedded IPv6 such as ::ffff:203.0.113.50.
  const lastColon = addrPart.lastIndexOf(':');
  if (lastColon >= 0) {
    const tail = parseIpv4Octets(addrPart.slice(lastColon + 1));
    if (tail) {
      const hi = ((tail[0] << 8) | tail[1]).toString(16);
      const lo = ((tail[2] << 8) | tail[3]).toString(16);
      return parseIpv6Groups(`${addrPart.slice(0, lastColon + 1)}${hi}:${lo}`) !== null;
    }
  }
  return false;
}

export function ipFamily(address: string): 'IPv4' | 'IPv6' | undefined {
  if (parseIpv4Octets(address) !== null) return 'IPv4';
  if (parseBareIpv6Groups(address) !== null) return 'IPv6';
  return undefined;
}

export function virtualAddressFamily(vip: Pick<VirtualAddress, 'address' | 'kind'>): 'IPv4' | 'IPv6' | undefined {
  if (virtualAddressKind(vip) === 'proxyArp') {
    return parseIpv4Cidr(vip.address) !== null ? 'IPv4' : undefined;
  }
  return ipFamily(vip.address);
}

export function staticIpv4Subnets(segment: Segment): StaticIpv4Subnet[] {
  if (segment.addressing.ipv4.mode !== 'static') return [];
  const out: StaticIpv4Subnet[] = [];
  for (const raw of segment.addressing.ipv4.addresses ?? []) {
    const parsed = parseIpv4Cidr(raw);
    if (!parsed || !parsed.hasSlash) continue;
    out.push({ cidr: raw, network: parsed.network, bits: parsed.bits, hostIp: parsed.ip });
  }
  return out;
}

export function isProxyArpEligibleSegment(segment: Segment): boolean {
  return staticIpv4Subnets(segment).length > 0;
}

// Matches internal/config/virtual.go: validUnicast(a) && !a.IsLinkLocalUnicast() && !a.Is4In6().
function isAllowedVirtualAddress(address: string): boolean {
  const v4 = parseIpv4Octets(address);
  if (v4) {
    const [o0, o1, o2, o3] = v4;
    if (o0 === 0 && o1 === 0 && o2 === 0 && o3 === 0) return false; // unspecified
    if (o0 === 127) return false; // loopback
    if (o0 === 169 && o1 === 254) return false; // link-local unicast
    if ((o0 & 0xf0) === 0xe0) return false; // multicast
    if (o0 === 255 && o1 === 255 && o2 === 255 && o3 === 255) return false; // limited broadcast
    return true;
  }
  const v6 = parseBareIpv6Groups(address);
  if (v6) {
    if (v6.every((g) => g === '0')) return false; // unspecified (::)
    if (v6.slice(0, 7).every((g) => g === '0') && v6[7] === '1') return false; // loopback (::1)
    const first = parseInt(v6[0], 16);
    if ((first & 0xffc0) === 0xfe80) return false; // link-local unicast (fe80::/10)
    if ((first & 0xff00) === 0xff00) return false; // multicast (ff00::/8)
    return true;
  }
  return false;
}

// Compare two bare IP addresses regardless of IPv6 casing or zero compression.
export function sameIpAddress(a: string, b: string): boolean {
  const a4 = parseIpv4Octets(a);
  if (a4 !== null) {
    const b4 = parseIpv4Octets(b);
    return b4 !== null && a4[0] === b4[0] && a4[1] === b4[1] && a4[2] === b4[2] && a4[3] === b4[3];
  }
  const a6 = parseBareIpv6Groups(a);
  if (a6 !== null) {
    const b6 = parseBareIpv6Groups(b);
    return b6 !== null && a6.join(':') === b6.join(':');
  }
  return false;
}

// Where a saved virtual IP is referenced by DNS or targeted by NAT port forwards.
export function virtualAddressUses(vip: VirtualAddress, snapshot: Snapshot): VirtualIpUse[] {
  const found: VirtualIpUse[] = [];
  if (snapshot.value.dns?.virtualAddresses?.includes(vip.id)) {
    found.push({ key: 'dns', label: 'DNS service', to: '/network/dns', locksAddress: false });
  }
  if (virtualAddressKind(vip) === 'address') {
    (snapshot.value.nat?.portForwards ?? []).forEach((pf, index) => {
      if (pf.target?.address && sameIpAddress(pf.target.address, vip.address)) {
        found.push({
          key: `pfw:${pf.id}`,
          label: `Port forward ${index + 1} (${pf.name})`,
          to: '/firewall/nat',
          locksAddress: true,
        });
      }
    });
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
): { errors: Map<string, string>; duplicate: boolean } {
  const kind: VirtualAddressKind = options?.kind === 'proxyArp' ? 'proxyArp' : 'address';
  const trimmedName = name.trim();
  const trimmedAddress = address.trim();
  const errors = new Map<string, string>();
  let duplicate = false;

  if (!trimmedName) {
    errors.set('name', 'A virtual IP needs a name.');
  } else if (existingVips.some((v) => v.value.id !== editingId && v.value.name === trimmedName)) {
    errors.set('name', 'Another virtual IP has this name.');
    duplicate = true;
  }

  const others = existingVips.filter((v) => v.value.id !== editingId);

  if (kind === 'proxyArp') {
    const segmentId = (options?.segment ?? '').trim();
    const segments = options?.segments;
    const chosenSegment = segments?.find((s) => s.id === segmentId);

    if (!segmentId) {
      errors.set('segment', 'Choose a segment with a static IPv4 subnet.');
    } else if (segments && (!chosenSegment || !isProxyArpEligibleSegment(chosenSegment))) {
      errors.set('segment', 'Choose a segment with a static IPv4 subnet.');
    }

    if (!trimmedAddress) {
      errors.set('address', 'A proxy ARP entry needs an IPv4 address or prefix.');
    } else if (trimmedAddress.includes('%')) {
      errors.set('address', 'Enter an address without an interface zone.');
    } else if (isIpv6Input(trimmedAddress)) {
      errors.set('address', 'Proxy NDP is not offered yet (#43).');
    } else {
      const parsed = parseIpv4Cidr(trimmedAddress);
      if (!parsed) {
        errors.set('address', 'Enter an IPv4 address or a /24–/32 prefix (for example 203.0.113.50 or 203.0.113.56/29).');
      } else if (parsed.bits < 24 || parsed.bits > 32) {
        errors.set('address', 'Proxy ARP prefixes must be between /24 and /32.');
      } else if (parsed.hasSlash && parsed.ip !== parsed.network) {
        errors.set('address', 'Prefix host bits must be zero.');
      } else {
        if (chosenSegment && isProxyArpEligibleSegment(chosenSegment)) {
          const chosenSubnets = staticIpv4Subnets(chosenSegment);
          if (!chosenSubnets.some((sub) => prefixContainsPrefixIpv4(sub, parsed))) {
            errors.set('address', "Expected an address on one of the segment's static IPv4 subnets.");
          }
        }
        if (!errors.has('address') && segments) {
          const hitsStatic = segments.some((seg) => staticIpv4Subnets(seg).some((sub) => prefixContainsIpv4(parsed, sub.hostIp)));
          if (hitsStatic) {
            errors.set('address', 'Proxy ARP cannot include a static address held by a segment.');
          }
        }
        if (!errors.has('address')) {
          for (const other of others) {
            if (virtualAddressKind(other.value) === 'proxyArp') {
              const otherPrefix = parseIpv4Cidr(other.value.address);
              if (otherPrefix && prefixesOverlapIpv4(parsed, otherPrefix)) {
                errors.set('address', 'This address or prefix overlaps another proxy ARP entry.');
                duplicate = true;
                break;
              }
            } else {
              const otherOctets = parseIpv4Octets(other.value.address);
              if (otherOctets && prefixContainsIpv4(parsed, ipv4Number(otherOctets))) {
                errors.set('address', 'This prefix includes an existing held virtual IP.');
                duplicate = true;
                break;
              }
            }
          }
        }
      }
    }

    return { errors, duplicate };
  }

  if (!trimmedAddress) {
    errors.set('address', 'A virtual IP needs an address.');
  } else if (trimmedAddress.includes('/')) {
    errors.set('address', 'Enter a single address without a prefix length.');
  } else if (trimmedAddress.includes('%')) {
    errors.set('address', 'Enter an address without an interface zone.');
  } else if (!isAllowedVirtualAddress(trimmedAddress)) {
    errors.set('address', 'Enter one unicast IPv4 or IPv6 address (not loopback, link-local, multicast, or unspecified).');
  } else if (others.some((v) => virtualAddressKind(v.value) === 'address' && sameIpAddress(v.value.address, trimmedAddress))) {
    errors.set('address', 'Another virtual IP already has this address.');
    duplicate = true;
  } else {
    const octets = parseIpv4Octets(trimmedAddress);
    if (octets) {
      const addrNum = ipv4Number(octets);
      const hitsProxy = others.some((v) => {
        if (virtualAddressKind(v.value) !== 'proxyArp') return false;
        const parsed = parseIpv4Cidr(v.value.address);
        return parsed !== null && prefixContainsIpv4(parsed, addrNum);
      });
      if (hitsProxy) {
        errors.set('address', 'This address falls inside a proxy ARP virtual IP.');
        duplicate = true;
      }
    }
  }

  return { errors, duplicate };
}
