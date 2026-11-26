import { fieldErrors } from './api';
import type {
  Dhcp4Entry, Dhcp6Entry, DhcpOption, DhcpOptionType, DhcpRange, Lease4, Lease6, Segment, Snapshot,
} from './types';

export interface DhcpOptionDraft {
  code: string;
  type: DhcpOptionType;
  data: string;
  alwaysSend: boolean;
}

export interface Dhcp4StaticDraft {
  mac: string;
  address: string;
  hostname: string;
}

export interface Dhcp6StaticDraft {
  duid: string;
  address: string;
  prefix: string;
  prefixId: string;
  hostname: string;
}

export interface Dhcp4Draft {
  name: string;
  leaseTime: string;
  ranges: DhcpRange[];
  gateway: string;
  dnsServers: string;
  domainName: string;
  domainSearch: string;
  ntpServers: string;
  nextServer: string;
  bootFileName: string;
  options: DhcpOptionDraft[];
  staticLeases: Dhcp4StaticDraft[];
}

export interface Dhcp6Draft {
  name: string;
  leaseTime: string;
  ranges: DhcpRange[];
  dnsServers: string;
  domainSearch: string;
  ntpServers: string;
  options: DhcpOptionDraft[];
  staticLeases: Dhcp6StaticDraft[];
  pdEnabled: boolean;
  pdPrefix: string;
  pdSubnetId: string;
  pdLength: string;
  pdDelegatedLength: string;
  pdExcludedPrefix: string;
}

export const dhcpOptionTypes: Array<{ value: DhcpOptionType; label: string }> = [
  { value: 'string', label: 'Text (string)' },
  { value: 'ip', label: 'IP address(es)' },
  { value: 'uint8', label: '8-bit integer' },
  { value: 'uint16', label: '16-bit integer' },
  { value: 'uint32', label: '32-bit integer' },
  { value: 'boolean', label: 'Boolean' },
  { value: 'hex', label: 'Hex bytes' },
];

export function optionPlaceholder(type: DhcpOptionType, family: 'v4' | 'v6'): string {
  switch (type) {
    case 'string': return 'http://wpad.home.arpa/wpad.dat';
    case 'ip': return family === 'v6' ? 'fd00:1::10, fd00:1::11' : '192.168.1.10, 192.168.1.11';
    case 'uint8': return '0 to 255';
    case 'uint16': return '0 to 65535';
    case 'uint32': return '0 to 4294967295';
    case 'boolean': return 'true or false';
    case 'hex': return 'aa:bb:cc or aabbcc';
  }
}

const words = (raw: string) => raw.split(/[\s,]+/).filter(Boolean);

// Parse an IPv4 CIDR such as "192.168.1.1/24" into its host IP, network integer and prefix length.
function parseIpv4Cidr(raw: string): { ip: number; net: number; bits: number; own: string; subnet: string } | null {
  const [addr, bitsRaw] = raw.split('/');
  if (!addr || !bitsRaw) return null;
  const bits = Number(bitsRaw);
  if (!Number.isInteger(bits) || bits < 0 || bits > 32) return null;
  const parts = addr.split('.');
  if (parts.length !== 4) return null;
  let ip = 0;
  for (const part of parts) {
    if (!/^\d+$/.test(part)) return null;
    const n = Number(part);
    if (n < 0 || n > 255) return null;
    ip = (ip * 256) + n;
  }
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
  const net = (ip & mask) >>> 0;
  return { ip: ip >>> 0, net, bits, own: addr, subnet: `${intToIpv4(net)}/${bits}` };
}

function intToIpv4(n: number): string {
  return [(n >>> 24) & 255, (n >>> 16) & 255, (n >>> 8) & 255, n & 255].join('.');
}

// First canonical static IPv4 CIDR on a segment, matching the engine's servedPrefix().
export function servedIpv4(segment: Segment): { own: string; subnet: string; bits: number; ip: number; net: number } | null {
  if (segment.addressing.ipv4.mode !== 'static') return null;
  const sorted = [...(segment.addressing.ipv4.addresses ?? [])].sort();
  for (const raw of sorted) {
    const parsed = parseIpv4Cidr(raw);
    if (parsed) return parsed;
  }
  return null;
}

// Upper-half default range for a static IPv4 segment, matching the takeover assistant's convention.
export function suggestedIpv4Range(segment: Segment): DhcpRange {
  const info = servedIpv4(segment);
  if (!info || info.bits > 30) return { first: '', last: '' };
  const size = 2 ** (32 - info.bits);
  const bcast = info.net + size - 1;
  let first = info.net + Math.floor(size / 2);
  let last = bcast - 1;
  if (info.ip >= first && info.ip <= last) {
    if (info.ip === first && first < last) first += 1;
    else if (info.ip === last && first < last) last -= 1;
    else {
      // Own address sits inside the upper half; fall back to the lower half.
      first = info.net + 1;
      last = info.net + Math.floor(size / 2) - 1;
    }
  }
  return first <= last ? { first: intToIpv4(first), last: intToIpv4(last) } : { first: '', last: '' };
}

// First non-link-local static IPv6 /64 on a segment, matching the engine's servedV6Static().
export function servedIpv6Static(segment: Segment): { own: string; prefix: string } | null {
  if (segment.addressing.ipv6.mode !== 'static') return null;
  const sorted = [...(segment.addressing.ipv6.addresses ?? [])].sort();
  for (const raw of sorted) {
    const [addr, bitsRaw] = raw.split('/');
    if (!addr || bitsRaw !== '64' || !addr.includes(':')) continue;
    if (/^fe[89ab][0-9a-f]:/i.test(addr)) continue;
    return { own: addr, prefix: raw };
  }
  return null;
}

export function isDelegatedIpv6(segment: Segment): boolean {
  return segment.addressing.ipv6.mode === 'delegated' && Boolean(segment.addressing.ipv6.from);
}

export function raModeOf(segment: Segment): string {
  const ra = segment.addressing.ipv6.ra;
  if (!ra) return 'disabled';
  if (ra.mode) return ra.mode;
  if (!ra.enabled) return 'disabled';
  if (ra.managed) return 'managed';
  if (ra.otherInfo) return 'stateless';
  return 'unmanaged';
}

export function formatLeaseTime(seconds?: number): string {
  const value = seconds || 7200;
  if (value % 86400 === 0) return `${value / 86400}d`;
  if (value % 3600 === 0) return `${value / 3600}h`;
  if (value % 60 === 0) return `${value / 60}m`;
  return `${value}s`;
}

export function draft4Of(entry: Dhcp4Entry | undefined, segment: Segment): Dhcp4Draft {
  return {
    name: entry?.name ?? segment.name,
    leaseTime: String(entry?.leaseTime ?? 7200),
    ranges: entry?.ranges.length ? entry.ranges.map((r) => ({ ...r })) : [suggestedIpv4Range(segment)],
    gateway: entry?.gateway ?? '',
    dnsServers: (entry?.dnsServers ?? []).join(', '),
    domainName: entry?.domainName ?? '',
    domainSearch: (entry?.domainSearch ?? []).join(', '),
    ntpServers: (entry?.ntpServers ?? []).join(', '),
    nextServer: entry?.nextServer ?? '',
    bootFileName: entry?.bootFileName ?? '',
    options: (entry?.options ?? []).map((o) => ({ code: String(o.code), type: o.type, data: o.data, alwaysSend: Boolean(o.alwaysSend) })),
    staticLeases: (entry?.staticLeases ?? []).map((l) => ({ mac: l.mac, address: l.address, hostname: l.hostname ?? '' })),
  };
}

export function draft6Of(entry: Dhcp6Entry | undefined, segment: Segment): Dhcp6Draft {
  const delegated = isDelegatedIpv6(segment);
  const defaultRange = delegated ? { first: '::1000', last: '::1fff' } : { first: '', last: '' };
  return {
    name: entry?.name ?? `${segment.name}6`,
    leaseTime: String(entry?.leaseTime ?? 7200),
    ranges: entry ? (entry.ranges ?? []).map((r) => ({ ...r })) : (defaultRange.first ? [defaultRange] : []),
    dnsServers: (entry?.dnsServers ?? []).join(', '),
    domainSearch: (entry?.domainSearch ?? []).join(', '),
    ntpServers: (entry?.ntpServers ?? []).join(', '),
    options: (entry?.options ?? []).map((o) => ({ code: String(o.code), type: o.type, data: o.data, alwaysSend: Boolean(o.alwaysSend) })),
    staticLeases: (entry?.staticLeases ?? []).map((l) => ({
      duid: l.duid,
      address: l.address ?? '',
      prefix: l.prefix ?? '',
      prefixId: l.prefixId ?? '',
      hostname: l.hostname ?? '',
    })),
    pdEnabled: Boolean(entry?.prefixDelegation),
    pdPrefix: entry?.prefixDelegation?.prefix ?? '',
    pdSubnetId: entry?.prefixDelegation?.subnetId ?? '',
    pdLength: entry?.prefixDelegation?.length ? String(entry.prefixDelegation.length) : '',
    pdDelegatedLength: entry?.prefixDelegation?.delegatedLength ? String(entry.prefixDelegation.delegatedLength) : (delegated ? '64' : '56'),
    pdExcludedPrefix: entry?.prefixDelegation?.excludedPrefix ?? '',
  };
}

function buildOptions(drafts: DhcpOptionDraft[]): DhcpOption[] {
  return drafts
    .filter((o) => o.code.trim() || o.data.trim())
    .map((o) => {
      const item: DhcpOption = { code: Number(o.code.trim()), type: o.type, data: o.data.trim() };
      if (o.alwaysSend) item.alwaysSend = true;
      return item;
    });
}

export function checkDhcp4(draft: Dhcp4Draft): Map<string, string> {
  const found = new Map<string, string>();
  if (!draft.name.trim()) found.set('name', 'A DHCP server needs a name.');
  const lease = Number(draft.leaseTime.trim());
  if (!Number.isInteger(lease) || lease < 300 || lease > 604800) found.set('leaseTime', 'Expected 300 to 604800 seconds.');
  if (draft.ranges.length === 0) found.set('ranges', 'Add at least one address range.');
  draft.ranges.forEach((r, i) => {
    if (!r.first.trim()) found.set(`ranges/${i}/first`, 'Enter the first address.');
    if (!r.last.trim()) found.set(`ranges/${i}/last`, 'Enter the last address.');
  });
  draft.options.forEach((o, i) => {
    if (!o.code.trim() && !o.data.trim()) return;
    const code = Number(o.code.trim());
    if (!Number.isInteger(code) || code < 1 || code > 254) found.set(`options/${i}/code`, 'Code must be 1 to 254.');
    if (!o.data.trim()) found.set(`options/${i}/data`, 'Enter a value.');
  });
  draft.staticLeases.forEach((l, i) => {
    if (!l.mac.trim() && !l.address.trim() && !l.hostname.trim()) return;
    if (!l.mac.trim()) found.set(`staticLeases/${i}/mac`, 'Enter a MAC address.');
    if (!l.address.trim()) found.set(`staticLeases/${i}/address`, 'Enter an IPv4 address.');
  });
  return found;
}

export function checkDhcp6(draft: Dhcp6Draft, delegated: boolean): Map<string, string> {
  const found = new Map<string, string>();
  if (!draft.name.trim()) found.set('name', 'A DHCPv6 server needs a name.');
  const lease = Number(draft.leaseTime.trim());
  if (!Number.isInteger(lease) || lease < 300 || lease > 604800) found.set('leaseTime', 'Expected 300 to 604800 seconds.');
  draft.ranges.forEach((r, i) => {
    if (!r.first.trim() && !r.last.trim()) return;
    if (!r.first.trim()) found.set(`ranges/${i}/first`, 'Enter the first address.');
    if (!r.last.trim()) found.set(`ranges/${i}/last`, 'Enter the last address.');
  });
  if (draft.pdEnabled) {
    const dlen = Number(draft.pdDelegatedLength.trim());
    if (!Number.isInteger(dlen) || dlen < 48 || dlen > 64) found.set('prefixDelegation/delegatedLength', 'Expected 48 to 64.');
    if (delegated) {
      if (!draft.pdSubnetId.trim()) found.set('prefixDelegation/subnetId', 'Enter a hex subnet ID.');
      const len = Number(draft.pdLength.trim());
      if (!Number.isInteger(len) || len < 1 || len > 64) found.set('prefixDelegation/length', 'Enter the pool prefix length.');
    } else if (!draft.pdPrefix.trim()) {
      found.set('prefixDelegation/prefix', 'Enter an IPv6 pool prefix, such as fd00:beef:1::/48.');
    }
  }
  draft.options.forEach((o, i) => {
    if (!o.code.trim() && !o.data.trim()) return;
    const code = Number(o.code.trim());
    if (!Number.isInteger(code) || code < 1 || code > 65535) found.set(`options/${i}/code`, 'Code must be 1 to 65535.');
    if (!o.data.trim()) found.set(`options/${i}/data`, 'Enter a value.');
  });
  draft.staticLeases.forEach((l, i) => {
    if (!l.duid.trim() && !l.address.trim() && !l.prefix.trim() && !l.prefixId.trim() && !l.hostname.trim()) return;
    if (!l.duid.trim()) found.set(`staticLeases/${i}/duid`, 'Enter a client DUID.');
    const hasBinding = delegated ? Boolean(l.address.trim() || l.prefixId.trim()) : Boolean(l.address.trim() || l.prefix.trim());
    if (!hasBinding) found.set(`staticLeases/${i}`, delegated ? 'Enter a suffix address, a prefix ID, or both.' : 'Enter an address, a prefix, or both.');
  });
  return found;
}

export function dhcp4Payload(draft: Dhcp4Draft, segmentId: string, existing?: Dhcp4Entry): Omit<Dhcp4Entry, 'id'> {
  const value: Omit<Dhcp4Entry, 'id'> = {
    name: draft.name.trim(),
    ...(existing?.description ? { description: existing.description } : {}),
    segment: segmentId,
    ranges: draft.ranges.map((r) => ({ first: r.first.trim(), last: r.last.trim() })),
    leaseTime: Number(draft.leaseTime.trim()),
  };
  if (draft.gateway.trim()) value.gateway = draft.gateway.trim();
  const dns = words(draft.dnsServers);
  if (dns.length > 0) value.dnsServers = dns;
  if (draft.domainName.trim()) value.domainName = draft.domainName.trim();
  const search = words(draft.domainSearch);
  if (search.length > 0) value.domainSearch = search;
  const ntp = words(draft.ntpServers);
  if (ntp.length > 0) value.ntpServers = ntp;
  if (draft.nextServer.trim()) value.nextServer = draft.nextServer.trim();
  if (draft.bootFileName.trim()) value.bootFileName = draft.bootFileName.trim();
  const options = buildOptions(draft.options);
  if (options.length > 0) value.options = options;
  const statics = draft.staticLeases
    .filter((l) => l.mac.trim() || l.address.trim())
    .map((l) => ({
      mac: l.mac.trim(),
      address: l.address.trim(),
      ...(l.hostname.trim() ? { hostname: l.hostname.trim() } : {}),
    }));
  if (statics.length > 0) value.staticLeases = statics;
  return value;
}

export function dhcp6Payload(draft: Dhcp6Draft, segmentId: string, delegated: boolean, existing?: Dhcp6Entry): Omit<Dhcp6Entry, 'id'> {
  const value: Omit<Dhcp6Entry, 'id'> = {
    name: draft.name.trim(),
    ...(existing?.description ? { description: existing.description } : {}),
    segment: segmentId,
    leaseTime: Number(draft.leaseTime.trim()),
  };
  const ranges = draft.ranges
    .filter((r) => r.first.trim() || r.last.trim())
    .map((r) => ({ first: r.first.trim(), last: r.last.trim() }));
  if (ranges.length > 0) value.ranges = ranges;
  const dns = words(draft.dnsServers);
  if (dns.length > 0) value.dnsServers = dns;
  const search = words(draft.domainSearch);
  if (search.length > 0) value.domainSearch = search;
  const ntp = words(draft.ntpServers);
  if (ntp.length > 0) value.ntpServers = ntp;
  const options = buildOptions(draft.options);
  if (options.length > 0) value.options = options;
  if (draft.pdEnabled) {
    value.prefixDelegation = delegated
      ? {
          subnetId: draft.pdSubnetId.trim(),
          length: Number(draft.pdLength.trim()),
          delegatedLength: Number(draft.pdDelegatedLength.trim()),
        }
      : {
          prefix: draft.pdPrefix.trim(),
          delegatedLength: Number(draft.pdDelegatedLength.trim()),
          ...(draft.pdExcludedPrefix.trim() ? { excludedPrefix: draft.pdExcludedPrefix.trim() } : {}),
        };
  }
  const statics = draft.staticLeases
    .filter((l) => l.duid.trim() || l.address.trim() || l.prefix.trim() || l.prefixId.trim())
    .map((l) => ({
      duid: l.duid.trim(),
      ...(l.address.trim() ? { address: l.address.trim() } : {}),
      ...(!delegated && l.prefix.trim() ? { prefix: l.prefix.trim() } : {}),
      ...(delegated && l.prefixId.trim() ? { prefixId: l.prefixId.trim() } : {}),
      ...(l.hostname.trim() ? { hostname: l.hostname.trim() } : {}),
    }));
  if (statics.length > 0) value.staticLeases = statics;
  return value;
}

// Map engine JSON pointer error paths (e.g. "/dhcp/0/gateway" or "/gateway" or "/options/1/code") to drawer field keys.
export function dhcpErrors(error: unknown): Map<string, string> {
  return fieldErrors(error, (path) => {
    const clean = path.replace(/^\/(dhcp6?|dhcp)\/\d+\//, '').replace(/^\//, '');
    if (/^(dnsServers|domainSearch|ntpServers)\/\d+$/.test(clean)) return clean.split('/')[0];
    return clean;
  });
}

export function summarizeDhcp4Extras(entry: Dhcp4Entry): string[] {
  const parts: string[] = [];
  if (entry.gateway) parts.push(`Gateway ${entry.gateway}`);
  if (entry.dnsServers?.length) parts.push(`DNS: ${entry.dnsServers.join(', ')}`);
  if (entry.domainName) parts.push(`Domain ${entry.domainName}`);
  if (entry.domainSearch?.length) parts.push(`Search: ${entry.domainSearch.join(', ')}`);
  if (entry.ntpServers?.length) parts.push(`NTP: ${entry.ntpServers.join(', ')}`);
  if (entry.nextServer || entry.bootFileName) parts.push('PXE boot');
  if (entry.options?.length) parts.push(`${entry.options.length} custom ${entry.options.length === 1 ? 'option' : 'options'}`);
  if (entry.staticLeases?.length) parts.push(`${entry.staticLeases.length} static`);
  return parts;
}

export function summarizeDhcp6Extras(entry: Dhcp6Entry): string[] {
  const parts: string[] = [];
  if (entry.dnsServers?.length) parts.push(`DNS: ${entry.dnsServers.join(', ')}`);
  if (entry.domainSearch?.length) parts.push(`Search: ${entry.domainSearch.join(', ')}`);
  if (entry.ntpServers?.length) parts.push(`NTP: ${entry.ntpServers.join(', ')}`);
  if (entry.options?.length) parts.push(`${entry.options.length} custom ${entry.options.length === 1 ? 'option' : 'options'}`);
  if (entry.staticLeases?.length) parts.push(`${entry.staticLeases.length} static`);
  return parts;
}

export function leaseMatchesStatic4(lease: Lease4, snapshot?: Snapshot): boolean {
  const entry = snapshot?.value.dhcp?.find((d) => d.segment === lease.segment);
  if (!entry?.staticLeases) return false;
  const mac = lease.mac.toLowerCase();
  return entry.staticLeases.some((s) => s.mac.toLowerCase() === mac || s.address === lease.address);
}

export function leaseMatchesStatic6(lease: Lease6, snapshot?: Snapshot): boolean {
  const entry = snapshot?.value.dhcp6?.find((d) => d.segment === lease.segment);
  if (!entry?.staticLeases) return false;
  const duid = lease.duid.toLowerCase();
  return entry.staticLeases.some((s) => s.duid.toLowerCase() === duid);
}
