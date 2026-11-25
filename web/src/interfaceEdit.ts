import { fieldErrors } from './api';
import type { ConfigBatchOperation, LinkStatus, SavedLink, SavedObject, Segment } from './types';

// What the interface panel edits: the link, and the segment that puts a network on it.
export interface InterfaceDraft {
  name: string;
  enabled: boolean;
  mtu: string;
  // Whether the interface carries a network of its own: a segment, with a zone and addressing.
  // A link without one still serves as a VLAN trunk's parent or a bridge member.
  assigned: boolean;
  segmentName: string;
  zone: string;
  newZone: string;
  ipv4Mode: string;
  ipv4Addresses: string;
  ipv4Gateway: string;
  ipv6Mode: string;
  ipv6Addresses: string;
  ipv6Gateway: string;
  ipv6PrefixLength: string;
  ipv6From: string;
  ipv6SubnetId: string;
  dnsServers: string;
  searchDomains: string;
}

// isPort says whether a live link is hardware a saved Ethernet link can name, by its permanent address, as the
// renderer matches it. A VLAN, bridge or veth has a kind of its own, and the loopback is no Ethernet device.
export const isPort = (live?: LinkStatus) => live?.linkType === 'ether' && !live.kind && Boolean(live.permanentAddress);

// The zone option that makes a new zone from the name typed beside it.
export const newZoneOption = '__new';

type Fields = Record<string, unknown>;

const list = (value: unknown) => (Array.isArray(value) ? value.join(', ') : '');
const text = (value: unknown) => (value === undefined ? '' : String(value));
const words = (raw: string) => raw.split(/[\s,]+/).filter(Boolean);

export function draftOf(link: SavedLink | undefined, segment: Segment | undefined, live: LinkStatus | undefined): InterfaceDraft {
  const addressing = (segment?.addressing ?? {}) as Fields;
  const ipv4 = (addressing.ipv4 ?? { mode: 'none' }) as Fields;
  const ipv6 = (addressing.ipv6 ?? { mode: 'none' }) as Fields;
  const dns = (addressing.dns ?? {}) as Fields;
  return {
    name: link?.name ?? live?.name ?? '',
    enabled: link?.enabled ?? true,
    mtu: String(link?.mtu ?? live?.mtu ?? 1500),
    assigned: Boolean(segment) || !link,
    segmentName: segment?.name ?? link?.name ?? live?.name ?? '',
    zone: segment?.zone ?? '',
    newZone: '',
    ipv4Mode: text(ipv4.mode),
    ipv4Addresses: list(ipv4.addresses),
    ipv4Gateway: text(ipv4.gateway),
    ipv6Mode: text(ipv6.mode),
    ipv6Addresses: list(ipv6.addresses),
    ipv6Gateway: text(ipv6.gateway),
    ipv6PrefixLength: text(ipv6.requestPrefixLength),
    ipv6From: text(ipv6.from),
    ipv6SubnetId: text(ipv6.subnetId),
    dnsServers: list(dns.servers),
    searchDomains: list(dns.searchDomains),
  };
}

// Optional fields are set when filled and left out when empty.
function put(target: Fields, key: string, value: unknown) {
  if (value === '' || value === undefined || (Array.isArray(value) && value.length === 0)) delete target[key];
  else target[key] = value;
}

// An addressing family keeps what the panel does not show (router advertisements, DHCPv6 options) while its mode
// stays; a new mode starts over, keeping only the router advertisements, which IPv6 modes other than none carry.
function family(saved: Fields | undefined, mode: string, keep: string[]): Fields {
  if (saved?.mode === mode) return { ...saved };
  const out: Fields = { mode };
  for (const key of keep) if (saved?.[key] !== undefined) out[key] = saved[key];
  return out;
}

function ipv4Of(draft: InterfaceDraft, saved: Fields | undefined): Fields {
  const out = family(saved, draft.ipv4Mode, []);
  if (draft.ipv4Mode === 'static') {
    put(out, 'addresses', words(draft.ipv4Addresses));
    put(out, 'gateway', draft.ipv4Gateway.trim());
  }
  return out;
}

function ipv6Of(draft: InterfaceDraft, saved: Fields | undefined): Fields {
  const out = family(saved, draft.ipv6Mode, ['ra']);
  if (out.ra) {
    const ra: Fields = { ...(out.ra as Fields) };
    // Without IPv6 there is nothing to advertise, and the engine refuses advertisements on such a network: they are
    // turned off, their other settings kept for when IPv6 returns. The older form says it with enabled.
    if (draft.ipv6Mode === 'none') {
      if ('enabled' in ra) ra.enabled = false;
      else ra.mode = 'disabled';
    }
    // A delegated prefix's lifetimes are the upstream lease's, and the engine refuses them set by hand.
    if (draft.ipv6Mode === 'delegated') {
      delete ra.validLifetime;
      delete ra.preferredLifetime;
    }
    out.ra = ra;
  }
  if (draft.ipv6Mode === 'static') {
    put(out, 'addresses', words(draft.ipv6Addresses));
    put(out, 'gateway', draft.ipv6Gateway.trim());
  } else if (draft.ipv6Mode === 'dhcp6') {
    put(out, 'requestPrefixLength', draft.ipv6PrefixLength.trim() === '' ? undefined : Number(draft.ipv6PrefixLength));
  } else if (draft.ipv6Mode === 'delegated') {
    put(out, 'from', draft.ipv6From);
    put(out, 'subnetId', draft.ipv6SubnetId.trim());
    // Static addresses beside the delegated prefix, such as a unique local one.
    put(out, 'addresses', words(draft.ipv6Addresses));
  }
  return out;
}

// Key order does not make two values differ.
function same(a: unknown, b: unknown): boolean {
  const canonical = (value: unknown): unknown => {
    if (Array.isArray(value)) return value.map(canonical);
    if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).sort(([x], [y]) => x.localeCompare(y)).map(([k, v]) => [k, canonical(v)]));
    return value;
  };
  return JSON.stringify(canonical(a)) === JSON.stringify(canonical(b));
}

// interfaceOperations is the one batch that makes the configuration say what the panel says: a new zone, the link,
// and its segment created, replaced or deleted. Only what changed is sent, so an unchanged link is not rewritten.
export function interfaceOperations(draft: InterfaceDraft, link: SavedObject<SavedLink> | undefined, segment: SavedObject<Segment> | undefined, live: LinkStatus | undefined): ConfigBatchOperation[] {
  const operations: ConfigBatchOperation[] = [];
  // A PPPoE password is read as its state, not its value: left out of a replace, the engine keeps it.
  const saved: Fields = { ...link?.value };
  delete saved.password;
  const linkValue: Fields = link
    ? { ...saved, name: draft.name.trim(), enabled: draft.enabled, mtu: Number(draft.mtu) }
    : { name: draft.name.trim(), kind: 'ethernet', permanentMac: (live?.permanentAddress ?? '').toLowerCase(), mtu: Number(draft.mtu), enabled: draft.enabled };
  if (!link) operations.push({ op: 'create', collection: 'links', localId: 'link', value: linkValue });
  else if (!same(linkValue, saved)) operations.push({ op: 'replace', collection: 'links', id: link.value.id, expectedVersion: link.version, value: linkValue });

  if (!draft.assigned) {
    if (segment) operations.push({ op: 'delete', collection: 'segments', id: segment.value.id, expectedVersion: segment.version });
    return operations;
  }
  let zone = draft.zone;
  if (zone === newZoneOption) {
    operations.push({ op: 'create', collection: 'zones', localId: 'zone', value: { name: draft.newZone.trim() } });
    zone = '@zone';
  }
  const savedAddressing = (segment?.value.addressing ?? {}) as Fields;
  const addressing: Fields = {
    ...savedAddressing,
    ipv4: ipv4Of(draft, savedAddressing.ipv4 as Fields | undefined),
    ipv6: ipv6Of(draft, savedAddressing.ipv6 as Fields | undefined),
  };
  const servers = words(draft.dnsServers);
  if (servers.length === 0) delete addressing.dns;
  else {
    const dns: Fields = { servers };
    put(dns, 'searchDomains', words(draft.searchDomains));
    addressing.dns = dns;
  }
  const segmentValue: Fields = { ...segment?.value, name: draft.segmentName.trim(), link: link?.value.id ?? '@link', zone, addressing };
  if (!segment) operations.push({ op: 'create', collection: 'segments', value: segmentValue });
  else if (!same(segmentValue, segment.value)) operations.push({ op: 'replace', collection: 'segments', id: segment.value.id, expectedVersion: segment.version, value: segmentValue });
  return operations;
}

// removeOperations takes the interface out of the configuration: its segment, then its link.
export function removeOperations(link: SavedObject<SavedLink>, segment: SavedObject<Segment> | undefined): ConfigBatchOperation[] {
  return [
    ...(segment ? [{ op: 'delete' as const, collection: 'segments', id: segment.value.id, expectedVersion: segment.version }] : []),
    { op: 'delete', collection: 'links', id: link.value.id, expectedVersion: link.version },
  ];
}

// The panel's field for each part of an object the engine may refuse, by the object's collection; the first match wins.
const fieldOf: Record<string, Array<[RegExp, string]>> = {
  zones: [[/^\/name/, 'newZone']],
  links: [[/^\/name/, 'name'], [/^\/mtu/, 'mtu']],
  segments: [
    [/^\/name/, 'segmentName'], [/^\/zone/, 'zone'],
    [/^\/addressing\/ipv4\/addresses/, 'ipv4Addresses'], [/^\/addressing\/ipv4\/gateway/, 'ipv4Gateway'], [/^\/addressing\/ipv4/, 'ipv4'],
    [/^\/addressing\/ipv6\/addresses/, 'ipv6Addresses'], [/^\/addressing\/ipv6\/gateway/, 'ipv6Gateway'], [/^\/addressing\/ipv6\/requestPrefixLength/, 'ipv6PrefixLength'],
    [/^\/addressing\/ipv6\/from/, 'ipv6From'], [/^\/addressing\/ipv6\/subnetId/, 'ipv6SubnetId'], [/^\/addressing\/ipv6/, 'ipv6'],
    [/^\/addressing\/dns\/searchDomains/, 'searchDomains'], [/^\/addressing\/dns/, 'dns'],
  ],
};

// interfaceErrors puts each of the engine's refusals of a batch beside its field; the rest go above the form.
export function interfaceErrors(error: unknown, operations: ConfigBatchOperation[]) {
  return fieldErrors(error, (path) => {
    const match = /^\/operations\/(\d+)\/value(\/.*)?$/.exec(path);
    const fields = match ? fieldOf[operations[Number(match[1])]?.collection] : undefined;
    return fields?.find(([pattern]) => pattern.test(match?.[2] ?? ''))?.[1] ?? '';
  });
}
