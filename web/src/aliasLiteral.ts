import { parseIpv6Groups } from './dhcpEdit';
import { parsePortSpan } from './natEdit';
import type { AliasKinds } from './types';

export interface LiteralOption {
  value: string;
  optionLabel: string;
  hint: string;
}

export type ParsedAddressLiteral =
  | { kind: 'host'; family: 4 | 6; value: bigint; canonical: string }
  | { kind: 'network'; family: 4 | 6; network: bigint; mask: bigint; canonical: string }
  | { kind: 'range'; family: 4 | 6; first: bigint; last: bigint; canonical: string };

export type ParsedPortLiteral =
  | { protocol: 'tcp' | 'udp'; ports: string[] }
  | { protocol: 'icmp' | 'icmpv6'; types: string[] };

// The types an ICMP or ICMPv6 alias or literal may name, matching internal/config/policy.go.
export const icmpTypes: Record<'icmp' | 'icmpv6', readonly string[]> = {
  icmp: ['echo-request', 'echo-reply', 'destination-unreachable', 'time-exceeded', 'parameter-problem', 'timestamp-request', 'timestamp-reply'],
  icmpv6: ['echo-request', 'echo-reply', 'destination-unreachable', 'packet-too-big', 'time-exceeded', 'parameter-problem'],
};

interface ParsedAddress {
  family: 4 | 6;
  value: bigint;
  canonical: string;
}

const ipv4Max = (1n << 32n) - 1n;
const ipv6Max = (1n << 128n) - 1n;

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
    return { family: 4, value, canonical: text };
  }

  if (text.includes(':')) {
    const groups = parseIpv6Groups(text);
    if (!groups) return null;
    let value = 0n;
    for (const g of groups) value = (value << 16n) | BigInt(`0x${g}`);
    if ((value >> 32n) === 0xffffn) return null;
    return { family: 6, value, canonical: formatIpv6Groups(groups) };
  }

  return null;
}

export function parseAddressLiteralValue(raw: string): ParsedAddressLiteral | null {
  const trimmed = raw.trim();
  if (!trimmed || trimmed.length > 128) return null;

  if (trimmed.includes('/')) {
    const slash = trimmed.indexOf('/');
    if (trimmed.indexOf('/', slash + 1) !== -1) return null;
    const addrPart = trimmed.slice(0, slash).trim();
    const bitsPart = trimmed.slice(slash + 1).trim();
    if (!/^(0|[1-9]\d{0,2})$/.test(bitsPart)) return null;
    const parsed = parseBareAddress(addrPart);
    if (!parsed) return null;
    const bits = Number(bitsPart);
    const maxBits = parsed.family === 4 ? 32 : 128;
    if (bits < 0 || bits > maxBits) return null;
    // Mirrors internal/config/policy.go — network prefixes must have zero host bits.
    const hostBits = maxBits - bits;
    const hostMask = (1n << BigInt(hostBits)) - 1n;
    if ((parsed.value & hostMask) !== 0n) return null;
    const allOnes = parsed.family === 4 ? ipv4Max : ipv6Max;
    return {
      kind: 'network',
      family: parsed.family,
      network: parsed.value,
      mask: allOnes ^ hostMask,
      canonical: `${parsed.canonical}/${bits}`,
    };
  }

  const dash = trimmed.indexOf('-');
  if (dash !== -1 && trimmed.indexOf('-', dash + 1) === -1) {
    const first = parseBareAddress(trimmed.slice(0, dash));
    const last = parseBareAddress(trimmed.slice(dash + 1));
    if (!first || !last || first.family !== last.family || last.value < first.value) return null;
    const canonical = `${first.canonical}-${last.canonical}`;
    if (canonical.length > 128) return null;
    return {
      kind: 'range',
      family: first.family,
      first: first.value,
      last: last.value,
      canonical,
    };
  }

  const host = parseBareAddress(trimmed);
  if (!host) return null;
  return {
    kind: 'host',
    family: host.family,
    value: host.value,
    canonical: host.canonical,
  };
}

export function isAddressLiteral(value: string): boolean {
  return parseAddressLiteralValue(value) !== null;
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

export function parsePortLiteralValue(raw: string): ParsedPortLiteral | null {
  const trimmed = raw.trim();
  if (!trimmed || trimmed.length > 256) return null;

  if (trimmed === 'icmp' || trimmed === 'icmpv6') {
    return { protocol: trimmed, types: [] };
  }

  const slash = trimmed.indexOf('/');
  if (slash <= 0 || slash === trimmed.length - 1) return null;
  const proto = trimmed.slice(0, slash);
  const rest = trimmed.slice(slash + 1);

  if (proto === 'tcp' || proto === 'udp') {
    const parts = rest.split(',');
    if (parts.length > 64) return null;
    const ports: string[] = [];
    for (const part of parts) {
      const span = parsePortSpan(part);
      if (!span) return null;
      const canonical = span.first === span.last ? String(span.first) : `${span.first}-${span.last}`;
      if (part !== canonical && part !== `${span.first}-${span.last}`) return null;
      if (!ports.includes(canonical)) ports.push(canonical);
    }
    return { protocol: proto, ports };
  }

  if (proto === 'icmp' || proto === 'icmpv6') {
    const parts = rest.split(',');
    if (parts.length > 32) return null;
    const allowed = icmpTypes[proto];
    const types: string[] = [];
    for (const part of parts) {
      if (!allowed.includes(part)) return null;
      if (!types.includes(part)) types.push(part);
    }
    return { protocol: proto, types };
  }

  return null;
}

export function isPortLiteral(value: string): boolean {
  return parsePortLiteralValue(value) !== null;
}

export function isAliasLiteral(aliasType: keyof AliasKinds, value: string): boolean {
  return aliasType === 'addresses' ? isAddressLiteral(value) : isPortLiteral(value);
}

export function formatAddressRef(ref: string, names: Map<string, string>, fallback = 'unknown alias'): string {
  return names.get(ref) ?? (isAddressLiteral(ref) ? ref : fallback);
}

export function formatPortRef(ref: string, names: Map<string, string>, fallback = 'unknown alias'): string {
  return names.get(ref) ?? (isPortLiteral(ref) ? ref : fallback);
}

function buildPortOption(protocol: 'tcp' | 'udp', ports: string[]): LiteralOption | null {
  const joined = ports.join(',');
  const value = `${protocol}/${joined}`;
  if (value.length > 256) return null;
  const upper = protocol.toUpperCase();
  const isPlural = ports.length > 1 || ports[0].includes('-');
  return {
    value,
    optionLabel: `${upper} ${joined}`,
    hint: isPlural ? `${upper} ports` : `${upper} port`,
  };
}

export function parseAddressLiteral(
  input: string,
  options: { family?: 'ipv4' | 'ipv6'; kind?: string } = {},
): LiteralOption[] {
  const parsed = parseAddressLiteralValue(input);
  if (!parsed) return [];
  if (options.kind && parsed.kind !== options.kind) return [];
  const wantedFamily = options.family === 'ipv4' ? 4 : options.family === 'ipv6' ? 6 : undefined;
  if (wantedFamily !== undefined && parsed.family !== wantedFamily) return [];
  const famLabel = parsed.family === 4 ? 'IPv4' : 'IPv6';
  const kindLabel = parsed.kind === 'host' ? 'address' : parsed.kind === 'network' ? 'network' : 'range';
  return [{
    value: parsed.canonical,
    optionLabel: parsed.canonical,
    hint: `${famLabel} ${kindLabel}`,
  }];
}

export function parsePortLiteral(
  input: string,
  options: { kind?: string } = {},
): LiteralOption[] {
  const trimmed = input.trim();
  if (!trimmed || trimmed.length > 256) return [];

  const lower = trimmed.toLowerCase();
  if (lower === 'icmp' || lower === 'icmpv6') {
    if (options.kind && options.kind !== lower) return [];
    const label = lower === 'icmp' ? 'ICMP' : 'ICMPv6';
    return [{
      value: lower,
      optionLabel: `${label} (any type)`,
      hint: `${label} protocol`,
    }];
  }

  const protoMatch = /^(tcp|udp|icmp|icmpv6)(?:[/:]|\s+)(.+)$/i.exec(trimmed);
  if (protoMatch) {
    const protocol = protoMatch[1].toLowerCase() as 'tcp' | 'udp' | 'icmp' | 'icmpv6';
    const rest = protoMatch[2].trim();
    if (options.kind && options.kind !== protocol) return [];

    if (protocol === 'tcp' || protocol === 'udp') {
      const ports = parsePortList(rest);
      if (!ports) return [];
      const option = buildPortOption(protocol, ports);
      return option ? [option] : [];
    }

    const typeTokens = rest.toLowerCase().split(/[\s,]+/).filter(Boolean);
    if (typeTokens.length === 0 || typeTokens.length > 32) return [];
    const allowed = icmpTypes[protocol];
    const uniqueTypes: string[] = [];
    for (const token of typeTokens) {
      if (!allowed.includes(token)) return [];
      if (!uniqueTypes.includes(token)) uniqueTypes.push(token);
    }
    const joined = uniqueTypes.join(',');
    const value = `${protocol}/${joined}`;
    if (value.length > 256) return [];
    const summaryPrefix = protocol === 'icmp' ? 'ICMP' : 'ICMPv6';
    return [{
      value,
      optionLabel: `${summaryPrefix} ${joined}`,
      hint: `${summaryPrefix} ${uniqueTypes.length === 1 ? 'type' : 'types'}`,
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
    const option = buildPortOption(protocol, ports);
    return option ? [option] : [];
  });
}

export function parseAliasLiterals(
  aliasType: keyof AliasKinds,
  input: string,
  options: { family?: 'ipv4' | 'ipv6'; kind?: string } = {},
): LiteralOption[] {
  if (aliasType === 'addresses') {
    return parseAddressLiteral(input, options);
  }
  return parsePortLiteral(input, options);
}
