import { parseIpv6Groups } from './dhcpEdit';
import { parsePortSpan } from './natEdit';
import type { AddressAlias, AliasKinds, PortAlias } from './types';

export interface LiteralAliasCandidate<K extends keyof AliasKinds = keyof AliasKinds> {
  key: string;
  label: string;
  hint: string;
  expectedKind: string;
  expectedSummary: string;
  payload: Omit<AliasKinds[K], 'id'>;
}

// The types an ICMP or ICMPv6 alias may name, matching internal/config/policy.go.
export const icmpTypes: Record<'icmp' | 'icmpv6', readonly string[]> = {
  icmp: ['echo-request', 'echo-reply', 'destination-unreachable', 'time-exceeded', 'parameter-problem', 'timestamp-request', 'timestamp-reply'],
  icmpv6: ['echo-request', 'echo-reply', 'destination-unreachable', 'packet-too-big', 'time-exceeded', 'parameter-problem'],
};

interface ParsedAddress {
  family: 'ipv4' | 'ipv6';
  value: bigint;
  canonical: string;
}

function formatIpv6Groups(groups: string[]): string {
  const nums = groups.map((g) => Number.parseInt(g, 16));
  let bestStart = -1;
  let bestLen = 0;
  let runStart = -1;
  for (let i = 0; i < 8; i++) {
    if (nums[i] === 0) {
      if (runStart === -1) runStart = i;
      const len = i - runStart + 1;
      if (len > bestLen) {
        bestStart = runStart;
        bestLen = len;
      }
    } else {
      runStart = -1;
    }
  }
  if (bestLen < 2) return nums.map((n) => n.toString(16)).join(':');
  const left = nums.slice(0, bestStart).map((n) => n.toString(16)).join(':');
  const right = nums.slice(bestStart + bestLen).map((n) => n.toString(16)).join(':');
  return `${left}::${right}`;
}

function parseBareAddress(raw: string): ParsedAddress | null {
  const text = raw.trim();
  if (!text || text.includes('%') || text.includes('/')) return null;

  if (text.includes('.')) {
    const parts = text.split('.');
    if (parts.length !== 4) return null;
    let value = 0n;
    for (const part of parts) {
      if (!/^(0|[1-9]\d{0,2})$/.test(part)) return null;
      const n = Number(part);
      if (n < 0 || n > 255) return null;
      value = (value << 8n) | BigInt(n);
    }
    return { family: 'ipv4', value, canonical: text };
  }

  if (text.includes(':')) {
    const groups = parseIpv6Groups(text);
    if (!groups) return null;
    let value = 0n;
    for (const g of groups) value = (value << 16n) | BigInt(`0x${g}`);
    if ((value >> 32n) === 0xffffn) return null;
    return { family: 'ipv6', value, canonical: formatIpv6Groups(groups) };
  }

  return null;
}

function parseNetworkLiteral(raw: string): { family: 'ipv4' | 'ipv6'; canonical: string } | null {
  const slash = raw.indexOf('/');
  if (slash === -1 || raw.indexOf('/', slash + 1) !== -1) return null;
  const addrPart = raw.slice(0, slash).trim();
  const bitsPart = raw.slice(slash + 1).trim();
  if (!/^(0|[1-9]\d{0,2})$/.test(bitsPart)) return null;
  const parsed = parseBareAddress(addrPart);
  if (!parsed) return null;
  const bits = Number(bitsPart);
  const maxBits = parsed.family === 'ipv4' ? 32 : 128;
  if (bits < 0 || bits > maxBits) return null;
  // Mirrors internal/config/policy.go:90 — network prefixes must have zero host bits.
  const hostBits = maxBits - bits;
  if (hostBits > 0 && (parsed.value & ((1n << BigInt(hostBits)) - 1n)) !== 0n) return null;
  return { family: parsed.family, canonical: `${parsed.canonical}/${bits}` };
}

function parsePortList(raw: string): string[] | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;
  const tokens = trimmed.replace(/\s*-\s*/g, '-').split(/[\s,]+/).filter(Boolean);
  if (tokens.length === 0 || tokens.length > 64) return null;
  const out: string[] = [];
  for (const token of tokens) {
    const span = parsePortSpan(token);
    if (!span) return null;
    const canonical = span.first === span.last ? String(span.first) : `${span.first}-${span.last}`;
    if (!out.includes(canonical)) out.push(canonical);
  }
  return out;
}

function buildPortCandidate(
  protocol: 'tcp' | 'udp',
  ports: string[],
): LiteralAliasCandidate<'ports'> | null {
  const joined = ports.join(',');
  const name = `${protocol}/${joined}`;
  if (name.length > 128) return null;
  const upper = protocol.toUpperCase();
  const payload: Omit<PortAlias, 'id'> = { name, protocol, ports };
  return {
    key: `${protocol}:${joined}`,
    label: `Use ${upper} ${joined}`,
    hint: `Creates ${upper} port alias "${name}"`,
    expectedKind: protocol,
    expectedSummary: `${upper} ${ports.join(', ')}`,
    payload,
  };
}

export function parseAddressLiteral(
  input: string,
  options: { family?: 'ipv4' | 'ipv6'; kind?: string } = {},
): Array<LiteralAliasCandidate<'addresses'>> {
  const trimmed = input.trim();
  if (!trimmed || trimmed.length > 128) return [];

  if (trimmed.includes('/')) {
    if (options.kind && options.kind !== 'network') return [];
    const net = parseNetworkLiteral(trimmed);
    if (!net || (options.family && net.family !== options.family)) return [];
    const payload: Omit<AddressAlias, 'id'> = { name: net.canonical, kind: 'network', prefix: net.canonical };
    return [{
      key: `network:${net.canonical}`,
      label: `Use ${net.canonical}`,
      hint: `Creates network alias "${net.canonical}"`,
      expectedKind: 'network',
      expectedSummary: net.canonical,
      payload,
    }];
  }

  const dash = trimmed.indexOf('-');
  if (dash !== -1 && trimmed.indexOf('-', dash + 1) === -1) {
    if (options.kind && options.kind !== 'range') return [];
    const first = parseBareAddress(trimmed.slice(0, dash));
    const last = parseBareAddress(trimmed.slice(dash + 1));
    if (!first || !last || first.family !== last.family || last.value < first.value) return [];
    if (options.family && first.family !== options.family) return [];
    const name = `${first.canonical}-${last.canonical}`;
    if (name.length > 128) return [];
    const payload: Omit<AddressAlias, 'id'> = { name, kind: 'range', first: first.canonical, last: last.canonical };
    return [{
      key: `range:${name}`,
      label: `Use ${name}`,
      hint: `Creates address range alias "${name}"`,
      expectedKind: 'range',
      expectedSummary: `${first.canonical}–${last.canonical}`,
      payload,
    }];
  }

  if (options.kind && options.kind !== 'host') return [];
  const host = parseBareAddress(trimmed);
  if (!host || (options.family && host.family !== options.family)) return [];
  const payload: Omit<AddressAlias, 'id'> = { name: host.canonical, kind: 'host', address: host.canonical };
  return [{
    key: `host:${host.canonical}`,
    label: `Use ${host.canonical}`,
    hint: `Creates host address alias "${host.canonical}"`,
    expectedKind: 'host',
    expectedSummary: host.canonical,
    payload,
  }];
}

export function parsePortLiteral(
  input: string,
  options: { kind?: string } = {},
): Array<LiteralAliasCandidate<'ports'>> {
  const trimmed = input.trim();
  if (!trimmed || trimmed.length > 128) return [];

  const protoMatch = /^(tcp|udp|icmp|icmpv6)(?:[/:]|\s+)(.+)$/i.exec(trimmed);
  if (protoMatch) {
    const protocol = protoMatch[1].toLowerCase() as 'tcp' | 'udp' | 'icmp' | 'icmpv6';
    const rest = protoMatch[2].trim();
    if (options.kind && options.kind !== protocol) return [];

    if (protocol === 'tcp' || protocol === 'udp') {
      const ports = parsePortList(rest);
      if (!ports) return [];
      const candidate = buildPortCandidate(protocol, ports);
      return candidate ? [candidate] : [];
    }

    const typeTokens = rest.toLowerCase().split(/[\s,]+/).filter(Boolean);
    if (typeTokens.length === 0) return [];
    const allowed = icmpTypes[protocol];
    const uniqueTypes: string[] = [];
    for (const token of typeTokens) {
      if (!allowed.includes(token)) return [];
      if (!uniqueTypes.includes(token)) uniqueTypes.push(token);
    }
    const joined = uniqueTypes.join(',');
    const name = `${protocol}/${joined}`;
    if (name.length > 128) return [];
    const summaryPrefix = protocol === 'icmp' ? 'ICMP' : 'ICMPv6';
    const payload: Omit<PortAlias, 'id'> = { name, protocol, types: uniqueTypes };
    return [{
      key: `${protocol}:${joined}`,
      label: `Use ${summaryPrefix} ${joined}`,
      hint: `Creates ${summaryPrefix} alias "${name}"`,
      expectedKind: protocol,
      expectedSummary: `${summaryPrefix} ${uniqueTypes.join(', ')}`,
      payload,
    }];
  }

  const ports = parsePortList(trimmed);
  if (!ports) return [];
  const protocols: Array<'tcp' | 'udp'> = options.kind === 'tcp'
    ? ['tcp']
    : options.kind === 'udp'
      ? ['udp']
      : options.kind
        ? []
        : ['tcp', 'udp'];
  return protocols.flatMap((protocol) => {
    const candidate = buildPortCandidate(protocol, ports);
    return candidate ? [candidate] : [];
  });
}

export function parseAliasLiterals<K extends keyof AliasKinds>(
  aliasType: K,
  input: string,
  options: { family?: 'ipv4' | 'ipv6'; kind?: string } = {},
): Array<LiteralAliasCandidate<K>> {
  if (aliasType === 'addresses') {
    return parseAddressLiteral(input, options) as Array<LiteralAliasCandidate<K>>;
  }
  return parsePortLiteral(input, options) as Array<LiteralAliasCandidate<K>>;
}
