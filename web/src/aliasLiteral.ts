import { parseIpv6Groups } from './dhcpEdit';
import type { AddressAlias, AliasKinds, PortAlias } from './types';

export interface LiteralAliasCandidate<K extends keyof AliasKinds = keyof AliasKinds> {
  key: string;
  label: string;
  hint: string;
  payload: Omit<AliasKinds[K], 'id'>;
}

const ipv4Max = (1n << 32n) - 1n;
const ipv6Max = (1n << 128n) - 1n;
const ipv4MappedPrefix = 0xffffn << 32n;

const knownIcmpTypes: Record<'icmp' | 'icmpv6', ReadonlySet<string>> = {
  icmp: new Set([
    'echo-request',
    'echo-reply',
    'destination-unreachable',
    'time-exceeded',
    'parameter-problem',
    'timestamp-request',
    'timestamp-reply',
  ]),
  icmpv6: new Set([
    'echo-request',
    'echo-reply',
    'destination-unreachable',
    'packet-too-big',
    'time-exceeded',
    'parameter-problem',
  ]),
};

interface ParsedAddress {
  family: 'ipv4' | 'ipv6';
  value: bigint;
  canonical: string;
}

function formatIpv4(value: bigint): string {
  return [
    Number((value >> 24n) & 255n),
    Number((value >> 16n) & 255n),
    Number((value >> 8n) & 255n),
    Number(value & 255n),
  ].join('.');
}

function formatIpv6(value: bigint): string {
  const groups: number[] = [];
  for (let i = 7; i >= 0; i--) {
    groups.push(Number((value >> BigInt(i * 16)) & 0xffffn));
  }
  let bestStart = -1;
  let bestLen = 0;
  let runStart = -1;
  for (let i = 0; i < 8; i++) {
    if (groups[i] === 0) {
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
  if (bestLen < 2) return groups.map((g) => g.toString(16)).join(':');
  const left = groups.slice(0, bestStart).map((g) => g.toString(16)).join(':');
  const right = groups.slice(bestStart + bestLen).map((g) => g.toString(16)).join(':');
  return `${left}::${right}`;
}

function isIpv4MappedIpv6(value: bigint): boolean {
  return (value >> 32n) === (ipv4MappedPrefix >> 32n);
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
    return { family: 'ipv4', value, canonical: formatIpv4(value) };
  }

  if (text.includes(':')) {
    const groups = parseIpv6Groups(text);
    if (!groups) return null;
    let value = 0n;
    for (const g of groups) value = (value << 16n) | BigInt(`0x${g}`);
    if (isIpv4MappedIpv6(value)) return null;
    return { family: 'ipv6', value, canonical: formatIpv6(value) };
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
  const allOnes = parsed.family === 'ipv4' ? ipv4Max : ipv6Max;
  const mask = bits === 0 ? 0n : (allOnes << BigInt(maxBits - bits)) & allOnes;
  // Mirrors internal/config/policy.go:90 — network prefixes must have zero host bits.
  if ((parsed.value & mask) !== parsed.value) return null;
  return { family: parsed.family, canonical: `${parsed.canonical}/${bits}` };
}

function parsePortList(raw: string): string[] | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;
  const tokens = trimmed.replace(/\s*-\s*/g, '-').split(/[\s,]+/).filter(Boolean);
  if (tokens.length === 0 || tokens.length > 64) return null;
  const out: string[] = [];
  for (const token of tokens) {
    const match = /^([1-9]\d{0,4})(?:-([1-9]\d{0,4}))?$/.exec(token);
    if (!match) return null;
    const first = Number(match[1]);
    const last = match[2] !== undefined ? Number(match[2]) : first;
    if (first < 1 || first > 65535 || last < 1 || last > 65535 || last < first) return null;
    out.push(first === last ? String(first) : `${first}-${last}`);
  }
  return out;
}

export function parseAddressLiteral(
  input: string,
  options: { family?: 'ipv4' | 'ipv6'; kind?: string } = {},
): Array<LiteralAliasCandidate<'addresses'>> {
  const trimmed = input.trim();
  if (!trimmed || trimmed.length > 128) return [];

  // 1. Network CIDR (e.g. 192.168.10.0/24 or 2001:db8::/48)
  if (trimmed.includes('/')) {
    if (options.kind && options.kind !== 'network') return [];
    const net = parseNetworkLiteral(trimmed);
    if (!net) return [];
    if (options.family && net.family !== options.family) return [];
    const payload: Omit<AddressAlias, 'id'> = {
      name: net.canonical,
      kind: 'network',
      prefix: net.canonical,
    };
    return [{
      key: `network:${net.canonical}`,
      label: `Use ${net.canonical}`,
      hint: `Creates network alias "${net.canonical}"`,
      payload,
    }];
  }

  // 2. Range (e.g. 10.20.0.10-10.20.0.20 or 2001:db8::10-2001:db8::20)
  const dash = trimmed.indexOf('-');
  if (dash !== -1 && trimmed.indexOf('-', dash + 1) === -1) {
    if (options.kind && options.kind !== 'range') return [];
    const first = parseBareAddress(trimmed.slice(0, dash));
    const last = parseBareAddress(trimmed.slice(dash + 1));
    if (!first || !last || first.family !== last.family || last.value < first.value) return [];
    if (options.family && first.family !== options.family) return [];
    const name = `${first.canonical}-${last.canonical}`;
    if (name.length > 128) return [];
    const payload: Omit<AddressAlias, 'id'> = {
      name,
      kind: 'range',
      first: first.canonical,
      last: last.canonical,
    };
    return [{
      key: `range:${name}`,
      label: `Use ${name}`,
      hint: `Creates address range alias "${name}"`,
      payload,
    }];
  }

  // 3. Single host address (e.g. 192.168.1.50 or 2001:db8::50)
  if (options.kind && options.kind !== 'host') return [];
  const host = parseBareAddress(trimmed);
  if (!host) return [];
  if (options.family && host.family !== options.family) return [];
  const payload: Omit<AddressAlias, 'id'> = {
    name: host.canonical,
    kind: 'host',
    address: host.canonical,
  };
  return [{
    key: `host:${host.canonical}`,
    label: `Use ${host.canonical}`,
    hint: `Creates host address alias "${host.canonical}"`,
    payload,
  }];
}

export function parsePortLiteral(
  input: string,
  options: { kind?: string } = {},
): Array<LiteralAliasCandidate<'ports'>> {
  const trimmed = input.trim();
  if (!trimmed || trimmed.length > 128) return [];

  // 1. Explicit protocol prefix: tcp/443, udp:53, tcp 8000-8080, icmp/echo-request, icmpv6/echo-request
  const protoMatch = /^(tcp|udp|icmp|icmpv6)(?:[/:]|\s+)(.+)$/i.exec(trimmed);
  if (protoMatch) {
    const protocol = protoMatch[1].toLowerCase() as 'tcp' | 'udp' | 'icmp' | 'icmpv6';
    const rest = protoMatch[2].trim();
    if (options.kind && options.kind !== protocol) return [];

    if (protocol === 'tcp' || protocol === 'udp') {
      const ports = parsePortList(rest);
      if (!ports) return [];
      const joined = ports.join(',');
      const name = `${protocol}/${joined}`;
      if (name.length > 128) return [];
      const payload: Omit<PortAlias, 'id'> = { name, protocol, ports };
      return [{
        key: `${protocol}:${joined}`,
        label: `Use ${protocol.toUpperCase()} ${joined}`,
        hint: `Creates ${protocol.toUpperCase()} port alias "${name}"`,
        payload,
      }];
    }

    const typeTokens = rest.toLowerCase().split(/[\s,]+/).filter(Boolean);
    if (typeTokens.length === 0) return [];
    const allowed = knownIcmpTypes[protocol];
    const uniqueTypes: string[] = [];
    for (const token of typeTokens) {
      if (!allowed.has(token)) return [];
      if (!uniqueTypes.includes(token)) uniqueTypes.push(token);
    }
    const joined = uniqueTypes.join(',');
    const name = `${protocol}/${joined}`;
    if (name.length > 128) return [];
    const payload: Omit<PortAlias, 'id'> = { name, protocol, types: uniqueTypes };
    return [{
      key: `${protocol}:${joined}`,
      label: `Use ${protocol.toUpperCase()} ${joined}`,
      hint: `Creates ${protocol.toUpperCase()} alias "${name}"`,
      payload,
    }];
  }

  // 2. Bare port number, range, or comma-separated list (e.g. 443, 8000-8080, 80,443) -> offer TCP and/or UDP.
  const ports = parsePortList(trimmed);
  if (!ports) return [];
  const joined = ports.join(',');
  const protocols: Array<'tcp' | 'udp'> = options.kind === 'tcp'
    ? ['tcp']
    : options.kind === 'udp'
      ? ['udp']
      : options.kind
        ? []
        : ['tcp', 'udp'];
  const out: Array<LiteralAliasCandidate<'ports'>> = [];
  for (const protocol of protocols) {
    const name = `${protocol}/${joined}`;
    if (name.length > 128) continue;
    const payload: Omit<PortAlias, 'id'> = { name, protocol, ports };
    out.push({
      key: `${protocol}:${joined}`,
      label: `Use ${protocol.toUpperCase()} ${joined}`,
      hint: `Creates ${protocol.toUpperCase()} port alias "${name}"`,
      payload,
    });
  }
  return out;
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
