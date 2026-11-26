import { fieldErrors } from './api';
import { parseDecimalInt } from './dhcpEdit';
import { isDynamicAddressAlias, type AddressAlias, type OneToOneNat, type PortForward } from './types';

export interface PortForwardDraft {
  name: string;
  description: string;
  enabled: boolean;
  family: 'ipv4' | 'ipv6';
  ingressZones: string[];
  protocol: PortForward['protocol'];
  from: string[];
  fromNegated: boolean;
  toKind: 'self' | 'address';
  toAddress: string;
  toNegated: boolean;
  ports: string;
  noRedirect: boolean;
  targetKind: 'address' | 'self';
  targetAddress: string;
  targetPort: string;
  filterRule: 'auto' | 'none';
  reflection: boolean;
  log: boolean;
}

export interface OneToOneDraft {
  name: string;
  description: string;
  enabled: boolean;
  zone: string;
  noBinat: boolean;
  external: string;
  internal: string;
  destination: string[];
  destinationNegated: boolean;
  reflection: boolean;
}

// Parse "port" or "first-last" within 1-65535 without leading zeros, matching internal/config/nat.go.
export function parsePortSpan(raw: string): { first: number; last: number } | null {
  const s = raw.trim();
  const dash = s.indexOf('-');
  const lo = dash >= 0 ? s.slice(0, dash) : s;
  const hi = dash >= 0 ? s.slice(dash + 1) : s;
  const first = parseDecimalInt(lo);
  const last = parseDecimalInt(hi);
  if (!Number.isInteger(first) || !Number.isInteger(last)) return null;
  if (first < 1 || last > 65535 || first > last) return null;
  if (String(first) !== lo || String(last) !== hi) return null;
  return { first, last };
}

export function portForwardDraftOf(entry?: PortForward, defaultZone?: string): PortForwardDraft {
  const family = entry?.family ?? 'ipv4';
  return {
    name: entry?.name ?? '',
    description: entry?.description ?? '',
    enabled: entry?.enabled ?? true,
    family,
    ingressZones: entry ? [...entry.ingressZones] : (defaultZone ? [defaultZone] : []),
    protocol: entry?.protocol ?? 'tcp',
    from: entry ? [...entry.from] : [],
    fromNegated: Boolean(entry?.fromNegated),
    toKind: entry?.to.address ? 'address' : 'self',
    toAddress: entry?.to.address ?? '',
    toNegated: Boolean(entry?.toNegated),
    ports: entry?.ports ?? '',
    noRedirect: Boolean(entry?.noRedirect),
    targetKind: entry?.target ? (entry.target.self ? 'self' : 'address') : (family === 'ipv6' ? 'self' : 'address'),
    targetAddress: entry?.target?.address ?? '',
    targetPort: entry?.targetPort !== undefined ? String(entry.targetPort) : '',
    filterRule: entry?.filterRule ?? 'auto',
    reflection: Boolean(entry?.reflection),
    log: Boolean(entry?.log),
  };
}

export function checkPortForward(draft: PortForwardDraft): Map<string, string> {
  const found = new Map<string, string>();
  if (!draft.name.trim()) found.set('name', 'A port forward needs a name.');
  if (draft.ingressZones.length === 0) found.set('ingressZones', 'Choose at least one ingress zone.');
  else if (draft.ingressZones.length > 64) found.set('ingressZones', 'At most 64 ingress zones are allowed.');
  if (draft.fromNegated && draft.from.length > 32) {
    found.set('from', 'A port forward leaves out at most 32 sources. Put more of them in a group.');
  }
  if (draft.toKind === 'address' && !draft.toAddress) found.set('to', 'Choose a destination address alias.');
  const span = parsePortSpan(draft.ports);
  if (!span) found.set('ports', 'Expected a port or a first-last range within 1-65535.');
  if (!draft.noRedirect) {
    if (draft.targetKind === 'address' && !draft.targetAddress.trim()) {
      found.set('target', draft.family === 'ipv6' ? 'Enter a firewall IPv6 address or switch target to This firewall.' : 'Enter a target IPv4 address.');
    }
    if (draft.targetPort.trim()) {
      const tp = parseDecimalInt(draft.targetPort);
      if (!Number.isInteger(tp) || tp < 1 || tp > 65535) {
        found.set('targetPort', 'Expected a port number from 1 to 65535.');
      } else if (span && tp + (span.last - span.first) > 65535) {
        found.set('targetPort', 'The shifted range would end past port 65535.');
      }
    }
  }
  return found;
}

export function portForwardPayload(draft: PortForwardDraft): Omit<PortForward, 'id'> {
  const value: Omit<PortForward, 'id'> = {
    name: draft.name.trim(),
    ...(draft.description.trim() ? { description: draft.description.trim() } : {}),
    enabled: draft.enabled,
    ...(draft.family === 'ipv6' ? { family: 'ipv6' } : {}),
    ingressZones: [...draft.ingressZones],
    protocol: draft.protocol,
    from: [...draft.from],
    ...(draft.fromNegated && draft.from.length > 0 ? { fromNegated: true } : {}),
    to: draft.toKind === 'address' ? { address: draft.toAddress } : { self: true },
    ...(draft.toNegated ? { toNegated: true } : {}),
    ports: draft.ports.trim(),
  };
  if (draft.noRedirect) {
    value.noRedirect = true;
  } else {
    value.target = draft.targetKind === 'self' ? { self: true } : { address: draft.targetAddress.trim() };
    if (draft.targetPort.trim()) value.targetPort = parseDecimalInt(draft.targetPort);
    value.filterRule = draft.filterRule;
    if (draft.reflection && !draft.toNegated && draft.family !== 'ipv6') value.reflection = true;
  }
  if (draft.log) value.log = true;
  return value;
}

export function portForwardErrors(error: unknown, draft: PortForwardDraft): Map<string, string> {
  return fieldErrors(error, (path) => {
    const clean = path.replace(/^\/nat\/portForwards\/\d+\//, '').replace(/^\//, '');
    if (/^ingressZones(\/\d+)?$/.test(clean)) return 'ingressZones';
    if (/^from(\/\d+)?$/.test(clean)) return 'from';
    if (clean === 'to' || clean === 'to/address' || clean === 'to/self') return draft.toKind === 'self' ? '' : 'to';
    if (clean === 'target' || clean === 'target/address' || clean === 'target/self') return draft.noRedirect ? '' : 'target';
    if (clean === 'targetPort' || clean === 'filterRule') return draft.noRedirect ? '' : clean;
    if (clean === 'reflection') return (draft.noRedirect || draft.toNegated || draft.family === 'ipv6') ? '' : 'reflection';
    if (clean === 'name' || clean === 'description' || clean === 'protocol' || clean === 'ports' || clean === 'family' || clean === 'log') {
      return clean;
    }
    return '';
  });
}

export function oneToOneDraftOf(entry?: OneToOneNat, defaultZone?: string): OneToOneDraft {
  return {
    name: entry?.name ?? '',
    description: entry?.description ?? '',
    enabled: entry?.enabled ?? true,
    zone: entry?.zone ?? defaultZone ?? '',
    noBinat: Boolean(entry?.noBinat),
    external: entry?.external ?? '',
    internal: entry?.internal ?? '',
    destination: entry ? [...entry.destination] : [],
    destinationNegated: Boolean(entry?.destinationNegated),
    reflection: Boolean(entry?.reflection),
  };
}

export function checkOneToOne(draft: OneToOneDraft): Map<string, string> {
  const found = new Map<string, string>();
  if (!draft.name.trim()) found.set('name', 'A 1:1 NAT entry needs a name.');
  if (!draft.zone) found.set('zone', 'Choose a zone.');
  if (!draft.internal.trim()) found.set('internal', 'Enter an internal IPv4 address or prefix.');
  if (!draft.noBinat) {
    if (!draft.external.trim()) found.set('external', 'Enter an external IPv4 address or prefix.');
    if (draft.destinationNegated && draft.destination.length === 0) {
      found.set('destinationNegated', 'Name the destinations to leave out.');
    } else if (draft.destinationNegated && draft.destination.length > 32) {
      found.set('destination', 'A 1:1 entry leaves out at most 32 destinations. Put more of them in a group.');
    }
  }
  return found;
}

export function oneToOnePayload(draft: OneToOneDraft): Omit<OneToOneNat, 'id'> {
  const value: Omit<OneToOneNat, 'id'> = {
    name: draft.name.trim(),
    ...(draft.description.trim() ? { description: draft.description.trim() } : {}),
    enabled: draft.enabled,
    zone: draft.zone,
    internal: draft.internal.trim(),
    destination: draft.noBinat ? [] : [...draft.destination],
  };
  if (draft.noBinat) {
    value.noBinat = true;
  } else {
    value.external = draft.external.trim();
    if (draft.destinationNegated && draft.destination.length > 0) value.destinationNegated = true;
    if (draft.reflection) value.reflection = true;
  }
  return value;
}

export function oneToOneErrors(error: unknown, draft: OneToOneDraft): Map<string, string> {
  return fieldErrors(error, (path) => {
    const clean = path.replace(/^\/nat\/oneToOne\/\d+\//, '').replace(/^\//, '');
    if (/^destination(\/\d+)?$/.test(clean)) return draft.noBinat ? '' : 'destination';
    if (clean === 'external' || clean === 'destinationNegated' || clean === 'reflection') return draft.noBinat ? '' : clean;
    if (clean === 'name' || clean === 'description' || clean === 'zone' || clean === 'internal') return clean;
    return '';
  });
}

// Mirrors internal/config.addressMatchesFamily for initial dropdown options before a search query is typed.
export function addressAliasesForFamily(
  aliases: Array<Partial<AddressAlias> & { id: string; name: string }>,
  family: 'ipv4' | 'ipv6',
  excludeDynamic = false,
): Array<{ value: string; label: string }> {
  const byId = new Map(aliases.map((a) => [a.id, a]));
  const matches = (id: string, nested: boolean): boolean => {
    const a = byId.get(id);
    if (!a) return false;
    if (isDynamicAddressAlias(a)) return !excludeDynamic;
    if (a.kind === 'group') {
      if (nested || !a.members || a.members.length === 0) return !nested && a.members === undefined;
      return a.members.some((m) => matches(m, true));
    }
    const sample = a.kind === 'network' ? a.prefix : a.kind === 'range' ? a.first : a.address;
    if (!sample) return true;
    const isV6 = sample.includes(':');
    return family === 'ipv6' ? isV6 : !isV6;
  };
  return aliases.filter((a) => matches(a.id, false)).map((a) => ({ value: a.id, label: a.name }));
}
