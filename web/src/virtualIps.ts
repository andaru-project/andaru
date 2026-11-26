import { parseIpv6Groups } from './dhcpEdit';
import type { SavedObject, Snapshot, VirtualAddress } from './types';

export interface VirtualIpUse {
  key: string;
  label: string;
  to: string;
  locksAddress: boolean;
}

export const maxVirtualAddresses = 64;

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

export function ipFamily(address: string): 'IPv4' | 'IPv6' | undefined {
  if (parseIpv4Octets(address) !== null) return 'IPv4';
  if (parseBareIpv6Groups(address) !== null) return 'IPv6';
  return undefined;
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
  return found;
}

export function checkVirtualIp(
  name: string,
  address: string,
  existingVips: Array<SavedObject<VirtualAddress>>,
  editingId?: string,
): { errors: Map<string, string>; duplicate: boolean } {
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

  if (!trimmedAddress) {
    errors.set('address', 'A virtual IP needs an address.');
  } else if (trimmedAddress.includes('/')) {
    errors.set('address', 'Enter a single address without a prefix length.');
  } else if (trimmedAddress.includes('%')) {
    errors.set('address', 'Enter an address without an interface zone.');
  } else if (!isAllowedVirtualAddress(trimmedAddress)) {
    errors.set('address', 'Enter one unicast IPv4 or IPv6 address (not loopback, link-local, multicast, or unspecified).');
  } else if (existingVips.some((v) => v.value.id !== editingId && sameIpAddress(v.value.address, trimmedAddress))) {
    errors.set('address', 'Another virtual IP already has this address.');
    duplicate = true;
  }

  return { errors, duplicate };
}
