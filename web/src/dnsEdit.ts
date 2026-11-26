import { fieldErrors } from './api';
import { parseDecimalInt, servedIpv4 } from './dhcpEdit';
import type { DnsMode, DnsService, DnsUpstreamProtocol, Segment, Snapshot } from './types';

let nextDnsRowKey = 1;
export function newDnsRowKey(): number {
  return nextDnsRowKey++;
}

export interface DnsUpstreamDraft {
  key: number;
  address: string;
  tlsName: string;
}

export interface DnsDraft {
  mode: DnsMode;
  protocol: DnsUpstreamProtocol;
  upstreams: DnsUpstreamDraft[];
  segments: string[];
  virtualAddresses: string[];
  localDomain: string;
  searchDomains: string;
  registerLeases: boolean;
}

const words = (raw: string) => raw.split(/[\s,]+/).filter(Boolean);

// First non-link-local static IPv6 address on a static or delegated segment, matching the engine's servedV6().
export function servedDnsIpv6(segment: Segment): string | null {
  const mode = segment.addressing.ipv6.mode;
  if (mode !== 'static' && mode !== 'delegated') return null;
  const sorted = [...(segment.addressing.ipv6.addresses ?? [])].sort();
  for (const raw of sorted) {
    const [addr, bitsRaw] = raw.split('/');
    if (!addr || !bitsRaw || !addr.includes(':') || addr.includes('.')) continue;
    const bits = parseDecimalInt(bitsRaw);
    if (!Number.isInteger(bits) || bits < 0 || bits > 128) continue;
    if (/^fe[89ab][0-9a-f](:|$)/i.test(addr)) continue;
    return addr;
  }
  return null;
}

// Addresses the DNS service will listen on for a segment (first static IPv4 and/or first non-link-local IPv6).
export function dnsListenAddresses(segment: Segment): string[] {
  const out: string[] = [];
  const v4 = servedIpv4(segment);
  if (v4) out.push(v4.own);
  const v6 = servedDnsIpv6(segment);
  if (v6) out.push(v6);
  return out;
}

export function isDnsEligibleSegment(segment: Segment): boolean {
  return dnsListenAddresses(segment).length > 0;
}

export function segmentsWithHostDns(segments: Segment[]): Segment[] {
  return segments.filter((s) => Boolean(s.addressing.dns));
}

export function defaultDnsSegments(snapshot: Snapshot): string[] {
  const zones = new Map(snapshot.value.zones.map((z) => [z.id, z.name.toLowerCase()]));
  return snapshot.value.segments
    .filter((s) => isDnsEligibleSegment(s) && zones.get(s.zone) !== 'wan')
    .map((s) => s.id);
}

export function dnsDraftOf(dns: DnsService | null | undefined, snapshot: Snapshot): DnsDraft {
  const upstreams = (dns?.upstreams ?? []).map((u) => ({
    key: newDnsRowKey(),
    address: u.address,
    tlsName: u.tlsName ?? '',
  }));
  return {
    mode: dns?.mode ?? 'recursive',
    protocol: dns?.upstreams?.[0]?.protocol ?? 'do53',
    upstreams,
    segments: dns ? [...dns.segments] : defaultDnsSegments(snapshot),
    virtualAddresses: [...(dns?.virtualAddresses ?? [])],
    localDomain: dns?.localDomain ?? 'home.arpa',
    searchDomains: (dns?.searchDomains ?? []).join(', '),
    registerLeases: dns?.registerLeases ?? true,
  };
}

const isBlankUpstream = (u: DnsUpstreamDraft) => !u.address.trim() && !u.tlsName.trim();

// Drop blank upstream rows in forward mode before validation and submission so indices match server errors 1:1.
export function pruneDnsDraft(draft: DnsDraft): DnsDraft {
  if (draft.mode !== 'forward') return draft;
  const nonBlank = draft.upstreams.filter((u) => !isBlankUpstream(u));
  return {
    ...draft,
    upstreams: nonBlank.length > 0 ? nonBlank : draft.upstreams.slice(0, 1),
  };
}

export function checkDns(draft: DnsDraft): Map<string, string> {
  const found = new Map<string, string>();
  if (draft.mode === 'forward') {
    if (draft.upstreams.length === 0) {
      found.set('upstreams', 'Forwarding needs at least one upstream.');
    } else if (draft.upstreams.length > 8) {
      found.set('upstreams', 'At most 8 upstreams are allowed.');
    }
    draft.upstreams.forEach((u, i) => {
      if (!u.address.trim()) found.set(`upstreams/${i}/address`, 'Enter an IP address, optionally with a port.');
      if (draft.protocol === 'dot' && !u.tlsName.trim()) {
        found.set(`upstreams/${i}/tlsName`, 'DNS over TLS needs the certificate hostname.');
      }
    });
  }
  if (draft.localDomain.trim().length > 189) {
    found.set('localDomain', 'Domain name must be 189 characters or fewer.');
  }
  if (words(draft.searchDomains).length > 8) {
    found.set('searchDomains', 'At most 8 search domains are allowed.');
  }
  return found;
}

export function dnsPayload(draft: DnsDraft): DnsService {
  const value: DnsService = {
    mode: draft.mode,
    segments: Array.from(new Set(draft.segments)).sort(),
    registerLeases: draft.registerLeases,
  };
  if (draft.mode === 'forward') {
    const upstreams = draft.upstreams
      .filter((u) => !isBlankUpstream(u))
      .map((u) => ({
        address: u.address.trim(),
        protocol: draft.protocol,
        ...(draft.protocol === 'dot' ? { tlsName: u.tlsName.trim() } : {}),
      }));
    if (upstreams.length > 0) value.upstreams = upstreams;
  }
  if (draft.localDomain.trim()) value.localDomain = draft.localDomain.trim();
  const search = words(draft.searchDomains);
  if (search.length > 0) value.searchDomains = search;
  if (draft.virtualAddresses.length > 0) {
    value.virtualAddresses = Array.from(new Set(draft.virtualAddresses)).sort();
  }
  return value;
}

// Map engine JSON pointer error paths (e.g. "/dns/upstreams/0/address" or "/upstreams/0/tlsName") to field keys.
export function dnsErrors(error: unknown): Map<string, string> {
  return fieldErrors(error, (path) => {
    const clean = path.replace(/^\/dns\//, '').replace(/^\//, '');
    if (/^(searchDomains|segments|virtualAddresses)\/\d+$/.test(clean)) return clean.split('/')[0];
    return clean;
  });
}
