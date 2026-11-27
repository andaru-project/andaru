import { formatAddressRef, formatPortRef } from './aliasLiteral';
import type { AddressAlias, PortAlias } from './types';

// scaled writes a quantity in the largest unit that keeps it readable: one decimal below ten, none from ten up.
function scaled(value: number, base: number, units: string[]) {
  let unit = 0;
  // The number as it will be shown decides the unit, so 1023.9 MiB reads 1.0 GiB and not 1024 MiB.
  const shown = () => (unit === 0 || value >= 9.95 ? Math.round(value) : Math.round(value * 10) / 10);
  while (shown() >= base && unit < units.length - 1) {
    value /= base;
    unit++;
  }
  return `${unit > 0 && shown() < 10 ? shown().toFixed(1) : shown()} ${units[unit]}`;
}

export const bytes = (value: number) => scaled(value, 1024, ['B', 'KiB', 'MiB', 'GiB', 'TiB']);

// bitrate writes bits a second as networks are sold, in thousands: "850 kbps", "1.2 Mbps", "10 Gbps".
export const bitrate = (bitsPerSecond: number) => scaled(bitsPerSecond, 1000, ['bps', 'kbps', 'Mbps', 'Gbps']);

// duration writes a length of time in its two largest units: "3 days 4 hours", "5 hours 12 minutes", "40 minutes".
export function duration(seconds: number) {
  const parts: Array<[number, string]> = [[Math.floor(seconds / 86400), 'day'], [Math.floor(seconds / 3600) % 24, 'hour'], [Math.floor(seconds / 60) % 60, 'minute']];
  const first = parts.findIndex(([count]) => count > 0);
  if (first < 0) return 'less than a minute';
  return parts.slice(first, first + 2).filter(([count]) => count > 0).map(([count, unit]) => plural(count, unit)).join(' ');
}

// Firmware writes its date as MM/DD/YYYY (SMBIOS); dates are shown ISO. Anything else is shown as it is.
export const firmwareDate = (value: string) => value.replace(/^(0[1-9]|1[0-2])\/(0[1-9]|[12]\d|3[01])\/(\d{4})$/, '$3-$1-$2');

export const plural = (count: number, unit: string) => `${count} ${unit}${count === 1 ? '' : 's'}`;

const protocolLabels: Record<PortAlias['protocol'], string> = { tcp: 'TCP', udp: 'UDP', icmp: 'ICMP', icmpv6: 'ICMPv6', group: 'group' };

export function formatAddressAlias(alias: Partial<AddressAlias>, names: Map<string, string>): string | undefined {
  if (alias.kind === 'host') return alias.address;
  if (alias.kind === 'network') return alias.prefix;
  if (alias.kind === 'range' && alias.first && alias.last) return `${alias.first} to ${alias.last}`;
  if (alias.kind === 'group') return alias.members?.length ? alias.members.map((id) => formatAddressRef(id, names)).join(', ') : '';
  if (alias.kind === 'url') return alias.url;
  if (alias.kind === 'hostnames') return alias.names?.join(', ') ?? '';
  return undefined;
}

export function formatPortAlias(alias: Partial<PortAlias>, names: Map<string, string>): string | undefined {
  if (!alias.protocol) return undefined;
  if (alias.protocol === 'group') return alias.members?.length ? alias.members.map((id) => formatPortRef(id, names)).join(', ') : '';
  const items = alias.ports ?? alias.types ?? [];
  return `${protocolLabels[alias.protocol]} ${items.length > 0 ? items.join(', ') : 'any type'}`;
}
