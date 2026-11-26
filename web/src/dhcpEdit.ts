import { fieldErrors } from './api';
import type {
  Dhcp4Entry, Dhcp6Entry, DhcpOption, DhcpOptionType, DhcpRange, Lease4, Lease6, Segment, Snapshot,
} from './types';

let nextRowKey = 1;
export function newRowKey(): number {
  return nextRowKey++;
}

export interface DhcpRangeDraft extends DhcpRange {
  key: number;
}

export interface DhcpOptionDraft {
  key: number;
  code: string;
  type: DhcpOptionType;
  data: string;
  alwaysSend: boolean;
}

export interface Dhcp4StaticDraft {
  key: number;
  mac: string;
  address: string;
  hostname: string;
}

export interface Dhcp6StaticDraft {
  key: number;
  duid: string;
  address: string;
  prefix: string;
  prefixId: string;
  hostname: string;
}

export interface DhcpCommonDraft {
  name: string;
  leaseTime: string;
  ranges: DhcpRangeDraft[];
  dnsServers: string;
  domainSearch: string;
  ntpServers: string;
  options: DhcpOptionDraft[];
}

export interface Dhcp4Draft extends DhcpCommonDraft {
  gateway: string;
  domainName: string;
  nextServer: string;
  bootFileName: string;
  staticLeases: Dhcp4StaticDraft[];
}

export interface Dhcp6Draft extends DhcpCommonDraft {
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

export function parseDecimalInt(raw: string): number {
  const s = raw.trim();
  const n = /^\d+$/.test(s) ? Number(s) : Number.NaN;
  return Number.isSafeInteger(n) ? n : Number.NaN;
}

// Parse an IPv4 CIDR such as "192.168.1.1/24" into its host IP, network integer and prefix length.
function parseIpv4Cidr(raw: string): { ip: number; net: number; bits: number; own: string; subnet: string } | null {
  const [addr, bitsRaw] = raw.split('/');
  if (!addr || !bitsRaw) return null;
  const bits = parseDecimalInt(bitsRaw);
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

function freeIntervals(lo: number, hi: number, blocked: number[]): Array<{ first: number; last: number }> {
  if (lo > hi) return [];
  const out: Array<{ first: number; last: number }> = [];
  let cur = lo;
  for (const ip of blocked) {
    if (ip < cur) continue;
    if (ip > hi) break;
    if (ip > cur) out.push({ first: cur, last: ip - 1 });
    cur = ip + 1;
  }
  if (cur <= hi) out.push({ first: cur, last: hi });
  return out;
}

// Upper-half default range for a static IPv4 segment, excluding every own IP on the subnet.
export function suggestedIpv4Range(segment: Segment): DhcpRange {
  const info = servedIpv4(segment);
  if (!info || info.bits > 30) return { first: '', last: '' };
  const size = 2 ** (32 - info.bits);
  const bcast = info.net + size - 1;
  const mid = info.net + Math.floor(size / 2);
  const ownIps = (segment.addressing.ipv4.addresses ?? [])
    .map(parseIpv4Cidr)
    .filter((p): p is NonNullable<typeof p> => p !== null && p.ip >= info.net && p.ip <= bcast)
    .map((p) => p.ip)
    .sort((a, b) => a - b);

  const upper = freeIntervals(mid, bcast - 1, ownIps);
  if (upper.length === 1 && upper[0].first === mid && upper[0].last === bcast - 1) {
    return { first: intToIpv4(upper[0].first), last: intToIpv4(upper[0].last) };
  }
  const candidates = [...upper, ...freeIntervals(info.net + 1, mid - 1, ownIps)];
  let best: { first: number; last: number } | undefined;
  for (const candidate of candidates) {
    if (!best || (candidate.last - candidate.first) > (best.last - best.first)) best = candidate;
  }
  return best ? { first: intToIpv4(best.first), last: intToIpv4(best.last) } : { first: '', last: '' };
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
    ranges: entry?.ranges.length
      ? entry.ranges.map((r) => ({ key: newRowKey(), ...r }))
      : [{ key: newRowKey(), ...suggestedIpv4Range(segment) }],
    gateway: entry?.gateway ?? '',
    dnsServers: (entry?.dnsServers ?? []).join(', '),
    domainName: entry?.domainName ?? '',
    domainSearch: (entry?.domainSearch ?? []).join(', '),
    ntpServers: (entry?.ntpServers ?? []).join(', '),
    nextServer: entry?.nextServer ?? '',
    bootFileName: entry?.bootFileName ?? '',
    options: (entry?.options ?? []).map((o) => ({
      key: newRowKey(),
      code: String(o.code),
      type: o.type,
      data: o.data,
      alwaysSend: Boolean(o.alwaysSend),
    })),
    staticLeases: (entry?.staticLeases ?? []).map((l) => ({
      key: newRowKey(),
      mac: l.mac,
      address: l.address,
      hostname: l.hostname ?? '',
    })),
  };
}

export function draft6Of(entry: Dhcp6Entry | undefined, segment: Segment): Dhcp6Draft {
  const delegated = isDelegatedIpv6(segment);
  const defaultRange = delegated ? { first: '::1000', last: '::1fff' } : { first: '', last: '' };
  return {
    name: entry?.name ?? `${segment.name}6`,
    leaseTime: String(entry?.leaseTime ?? 7200),
    ranges: entry
      ? (entry.ranges ?? []).map((r) => ({ key: newRowKey(), ...r }))
      : (defaultRange.first ? [{ key: newRowKey(), ...defaultRange }] : []),
    dnsServers: (entry?.dnsServers ?? []).join(', '),
    domainSearch: (entry?.domainSearch ?? []).join(', '),
    ntpServers: (entry?.ntpServers ?? []).join(', '),
    options: (entry?.options ?? []).map((o) => ({
      key: newRowKey(),
      code: String(o.code),
      type: o.type,
      data: o.data,
      alwaysSend: Boolean(o.alwaysSend),
    })),
    staticLeases: (entry?.staticLeases ?? []).map((l) => ({
      key: newRowKey(),
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

const isBlankRange = (r: DhcpRange) => !r.first.trim() && !r.last.trim();
const isBlankOption = (o: DhcpOptionDraft) => !o.code.trim() && (o.type === 'string' ? o.data.length === 0 : !o.data.trim());
const isBlankStatic4 = (l: Dhcp4StaticDraft) => !l.mac.trim() && !l.address.trim() && !l.hostname.trim();
const isBlankStatic6 = (l: Dhcp6StaticDraft) => !l.duid.trim() && !l.address.trim() && !l.prefix.trim() && !l.prefixId.trim() && !l.hostname.trim();

// Drop completely blank rows before validation and submission so draft indices match server JSON pointer indices 1:1.
export function pruneDhcp4Draft(draft: Dhcp4Draft): Dhcp4Draft {
  const nonBlankRanges = draft.ranges.filter((r) => !isBlankRange(r));
  return {
    ...draft,
    ranges: nonBlankRanges.length > 0 ? nonBlankRanges : draft.ranges.slice(0, 1),
    options: draft.options.filter((o) => !isBlankOption(o)),
    staticLeases: draft.staticLeases.filter((l) => !isBlankStatic4(l)),
  };
}

export function pruneDhcp6Draft(draft: Dhcp6Draft): Dhcp6Draft {
  return {
    ...draft,
    ranges: draft.ranges.filter((r) => !isBlankRange(r)),
    options: draft.options.filter((o) => !isBlankOption(o)),
    staticLeases: draft.staticLeases.filter((l) => !isBlankStatic6(l)),
  };
}

function buildOptions(drafts: DhcpOptionDraft[]): DhcpOption[] {
  return drafts
    .filter((o) => !isBlankOption(o))
    .map((o) => {
      const item: DhcpOption = {
        code: parseDecimalInt(o.code),
        type: o.type,
        data: o.type === 'string' ? o.data : o.data.trim(),
      };
      if (o.alwaysSend) item.alwaysSend = true;
      return item;
    });
}

function checkCommonDhcp(draft: DhcpCommonDraft, maxOptionCode: number, noun: string): Map<string, string> {
  const found = new Map<string, string>();
  if (!draft.name.trim()) found.set('name', `A ${noun} server needs a name.`);
  const lease = parseDecimalInt(draft.leaseTime);
  if (!Number.isInteger(lease) || lease < 300 || lease > 604800) found.set('leaseTime', 'Expected 300 to 604800 seconds.');
  draft.options.forEach((o, i) => {
    if (isBlankOption(o)) return;
    const code = parseDecimalInt(o.code);
    if (!Number.isInteger(code) || code < 1 || code > maxOptionCode) found.set(`options/${i}/code`, `Code must be 1 to ${maxOptionCode}.`);
    const hasData = o.type === 'string' ? o.data.length > 0 : Boolean(o.data.trim());
    if (!hasData) found.set(`options/${i}/data`, 'Enter a value.');
  });
  return found;
}

export function checkDhcp4(draft: Dhcp4Draft): Map<string, string> {
  const found = checkCommonDhcp(draft, 254, 'DHCP');
  if (draft.ranges.length === 0) found.set('ranges', 'Add at least one address range.');
  draft.ranges.forEach((r, i) => {
    if (!r.first.trim()) found.set(`ranges/${i}/first`, 'Enter the first address.');
    if (!r.last.trim()) found.set(`ranges/${i}/last`, 'Enter the last address.');
  });
  draft.staticLeases.forEach((l, i) => {
    if (isBlankStatic4(l)) return;
    if (!l.mac.trim()) found.set(`staticLeases/${i}/mac`, 'Enter a MAC address.');
    if (!l.address.trim()) found.set(`staticLeases/${i}/address`, 'Enter an IPv4 address.');
  });
  return found;
}

export function checkDhcp6(draft: Dhcp6Draft, delegated: boolean): Map<string, string> {
  const found = checkCommonDhcp(draft, 65535, 'DHCPv6');
  draft.ranges.forEach((r, i) => {
    if (isBlankRange(r)) return;
    if (!r.first.trim()) found.set(`ranges/${i}/first`, 'Enter the first address.');
    if (!r.last.trim()) found.set(`ranges/${i}/last`, 'Enter the last address.');
  });
  if (draft.pdEnabled) {
    const dlen = parseDecimalInt(draft.pdDelegatedLength);
    if (delegated) {
      if (!draft.pdSubnetId.trim()) found.set('prefixDelegation/subnetId', 'Enter a hex subnet ID.');
      const len = parseDecimalInt(draft.pdLength);
      const validLen = Number.isInteger(len) && len >= 1 && len <= 64;
      if (!validLen) found.set('prefixDelegation/length', 'Enter the pool prefix length.');
      const minDlen = validLen ? len : 1;
      if (!Number.isInteger(dlen) || dlen < minDlen || dlen > 64) {
        found.set('prefixDelegation/delegatedLength', `Expected ${minDlen} to 64.`);
      }
    } else {
      if (!draft.pdPrefix.trim()) {
        found.set('prefixDelegation/prefix', 'Enter an IPv6 pool prefix, such as fd00:beef:1::/48.');
      }
      if (!Number.isInteger(dlen) || dlen < 48 || dlen > 64) {
        found.set('prefixDelegation/delegatedLength', 'Expected 48 to 64.');
      }
    }
  }
  draft.staticLeases.forEach((l, i) => {
    if (isBlankStatic6(l)) return;
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
    leaseTime: parseDecimalInt(draft.leaseTime),
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
    .filter((l) => !isBlankStatic4(l))
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
    leaseTime: parseDecimalInt(draft.leaseTime),
  };
  const ranges = draft.ranges
    .filter((r) => !isBlankRange(r))
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
          length: parseDecimalInt(draft.pdLength),
          delegatedLength: parseDecimalInt(draft.pdDelegatedLength),
        }
      : {
          prefix: draft.pdPrefix.trim(),
          delegatedLength: parseDecimalInt(draft.pdDelegatedLength),
          ...(draft.pdExcludedPrefix.trim() ? { excludedPrefix: draft.pdExcludedPrefix.trim() } : {}),
        };
  }
  const statics = draft.staticLeases
    .filter((l) => !isBlankStatic6(l))
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

function summarizeCommonDhcpExtras(entry: {
  dnsServers?: string[];
  domainSearch?: string[];
  ntpServers?: string[];
  options?: DhcpOption[];
  staticLeases?: unknown[];
}): string[] {
  const parts: string[] = [];
  if (entry.dnsServers?.length) parts.push(`DNS: ${entry.dnsServers.join(', ')}`);
  if (entry.domainSearch?.length) parts.push(`Search: ${entry.domainSearch.join(', ')}`);
  if (entry.ntpServers?.length) parts.push(`NTP: ${entry.ntpServers.join(', ')}`);
  if (entry.options?.length) parts.push(`${entry.options.length} custom ${entry.options.length === 1 ? 'option' : 'options'}`);
  if (entry.staticLeases?.length) parts.push(`${entry.staticLeases.length} static`);
  return parts;
}

export function summarizeDhcp4Extras(entry: Dhcp4Entry): string[] {
  const parts: string[] = [];
  if (entry.gateway) parts.push(`Gateway ${entry.gateway}`);
  if (entry.domainName) parts.push(`Domain ${entry.domainName}`);
  if (entry.nextServer || entry.bootFileName) parts.push('PXE boot');
  return [...parts, ...summarizeCommonDhcpExtras(entry)];
}

export function summarizeDhcp6Extras(entry: Dhcp6Entry): string[] {
  return summarizeCommonDhcpExtras(entry);
}

export function leaseMatchesStatic4(lease: Lease4, snapshot?: Snapshot): boolean {
  const entry = snapshot?.value.dhcp?.find((d) => d.segment === lease.segment);
  if (!entry?.staticLeases) return false;
  const mac = lease.mac.toLowerCase();
  return entry.staticLeases.some((s) => s.mac.toLowerCase() === mac);
}

export function leaseMatchesStatic6(lease: Lease6, snapshot?: Snapshot): boolean {
  const entry = snapshot?.value.dhcp6?.find((d) => d.segment === lease.segment);
  if (!entry?.staticLeases) return false;
  const duid = lease.duid.toLowerCase();
  return entry.staticLeases.some((s) => s.duid.toLowerCase() === duid
    && (lease.type === 'prefix' ? Boolean(s.prefix || s.prefixId) : Boolean(s.address)));
}

// Extract the low 64-bit interface identifier suffix (e.g. "::1234:5678") from a full IPv6 address.
export function ipv6SuffixOf(address: string): string {
  const host = address.split('/')[0].split('%')[0].trim();
  if (!host.includes(':') || host.includes('.')) return '';
  const halves = host.split('::');
  if (halves.length > 2) return '';
  const left = halves[0] ? halves[0].split(':') : [];
  const right = halves.length === 2 && halves[1] ? halves[1].split(':') : [];
  const validGroup = (g: string) => /^[0-9a-fA-F]{1,4}$/.test(g);
  if (!left.every(validGroup) || !right.every(validGroup)) return '';
  const missing = halves.length === 2 ? 8 - left.length - right.length : 0;
  if (halves.length === 2 ? missing < 1 : left.length !== 8) return '';
  const full = [...left, ...Array<string>(missing).fill('0'), ...right].map((g) => parseInt(g, 16).toString(16));
  if (full.length !== 8) return '';
  const tail = full.slice(4);
  if (tail.every((g) => g === '0')) return '::';
  let i = 0;
  while (i < tail.length - 1 && tail[i] === '0') i++;
  return `::${tail.slice(i).join(':')}`;
}
