import { parseIpv6Groups } from './dhcpEdit';
import type { SavedObject, Snapshot, VirtualAddress } from './types';

export interface VirtualIpUse {
  key: string;
  label: string;
  to: string;
  locksAddress: boolean;
}

export const maxVirtualAddresses = 64;

// Parse a bare dotted-quad IPv4 address (no prefix, zone, or leading zeros) into canonical form.
function parseIpv4(raw: string): string | null {
  const s = raw.trim();
  if (!s || s.includes('/') || s.includes('%')) return null;
  const parts = s.split('.');
  if (parts.length !== 4) return null;
  for (const part of parts) {
    if (!/^(?:0|[1-9]\d{0,2})$/.test(part) || Number(part) > 255) return null;
  }
  return parts.join('.');
}

// Parse a bare IPv6 address (no prefix or zone) into its 8 lowercase canonical hex groups.
function parseBareIpv6(raw: string): string | null {
  const s = raw.trim();
  if (!s || s.includes('/') || s.includes('%')) return null;
  const groups = parseIpv6Groups(s);
  return groups ? groups.join(':') : null;
}

export function ipFamily(address: string): 'IPv4' | 'IPv6' | undefined {
  if (parseIpv4(address) !== null) return 'IPv4';
  if (parseBareIpv6(address) !== null) return 'IPv6';
  return undefined;
}

// Compare two bare IP addresses regardless of IPv6 casing or zero compression.
export function sameIpAddress(a: string, b: string): boolean {
  const a4 = parseIpv4(a);
  if (a4 !== null) return a4 === parseIpv4(b);
  const a6 = parseBareIpv6(a);
  if (a6 !== null) return a6 === parseBareIpv6(b);
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
  } else if (!ipFamily(trimmedAddress)) {
    errors.set('address', 'Enter a single unicast IPv4 or IPv6 address.');
  } else if (existingVips.some((v) => v.value.id !== editingId && sameIpAddress(v.value.address, trimmedAddress))) {
    errors.set('address', 'Another virtual IP already has this address.');
    duplicate = true;
  }

  return { errors, duplicate };
}
