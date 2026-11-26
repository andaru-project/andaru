import { fieldErrors } from './api';
import type { BondMode, ConfigBatchOperation, LacpRate, LinkStatus, LldpEmitMode, LldpReceiveMode, SavedLink, SavedObject, Segment, Snapshot, TransmitHashPolicy, VirtualLinkKind } from './types';

// What the interface panel edits: the link, and the segment that puts a network on it.
export interface InterfaceDraft {
  kind: SavedLink['kind'];
  name: string;
  enabled: boolean;
  mtu: string;
  parent: string;
  vlanId: string;
  members: string[];
  bondMode: BondMode | '';
  stp: boolean;
  stpPriority: string;
  forwardDelay: string;
  helloTime: string;
  maxAge: string;
  multicastSnooping: boolean;
  lacpRate: LacpRate | '';
  miiMonitorMs: string;
  transmitHashPolicy: TransmitHashPolicy | '';
  lldp: LldpReceiveMode;
  emitLldp: LldpEmitMode;
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

export const bondModes: Array<{ value: BondMode; label: string }> = [
  { value: '802.3ad', label: '802.3ad (LACP)' },
  { value: 'active-backup', label: 'Active-backup' },
  { value: 'balance-rr', label: 'Round-robin (balance-rr)' },
  { value: 'balance-xor', label: 'XOR (balance-xor)' },
  { value: 'broadcast', label: 'Broadcast' },
  { value: 'balance-tlb', label: 'Adaptive transmit load balancing (balance-tlb)' },
  { value: 'balance-alb', label: 'Adaptive load balancing (balance-alb)' },
];

export const lacpRates: Array<{ value: '' | LacpRate; label: string }> = [
  { value: '', label: 'Default (slow, 30s)' },
  { value: 'slow', label: 'Slow (30s)' },
  { value: 'fast', label: 'Fast (1s)' },
];

export const transmitHashPolicies: Array<{ value: '' | TransmitHashPolicy; label: string }> = [
  { value: '', label: 'Default (layer2)' },
  { value: 'layer2', label: 'Layer 2 (MAC)' },
  { value: 'layer2+3', label: 'Layer 2+3 (MAC + IP)' },
  { value: 'layer3+4', label: 'Layer 3+4 (IP + port)' },
  { value: 'encap2+3', label: 'Encapsulated Layer 2+3' },
  { value: 'encap3+4', label: 'Encapsulated Layer 3+4' },
];

export const hashPolicyModes = new Set<BondMode>(['balance-xor', '802.3ad']);

export const lldpReceiveModes: Array<{ value: LldpReceiveMode; label: string }> = [
  { value: 'off', label: 'Off' },
  { value: 'receive', label: 'Receive all neighbors' },
  { value: 'routers-only', label: 'Routers only' },
];

export const lldpEmitModes: Array<{ value: LldpEmitMode; label: string }> = [
  { value: 'off', label: 'Off' },
  { value: 'nearest-bridge', label: 'Nearest bridge' },
  { value: 'non-tpmr-bridge', label: 'Non-TPMR bridge' },
  { value: 'customer-bridge', label: 'Customer bridge' },
];

// How a segment gets its addresses, in the words the panel and the table use.
export const ipv4Modes = [{ value: 'none', label: 'None' }, { value: 'dhcp', label: 'DHCP client' }, { value: 'static', label: 'Static' }, { value: 'ppp', label: 'PPP' }];
export const ipv6Modes = [
  { value: 'none', label: 'None' }, { value: 'slaac', label: 'SLAAC' }, { value: 'dhcp6', label: 'DHCPv6 client' },
  { value: 'static', label: 'Static' }, { value: 'delegated', label: 'From a delegated prefix' },
];
export const modeLabel = (modes: Array<{ value: string; label: string }>, mode: string) => modes.find((item) => item.value === mode)?.label ?? mode;

// The table's short words for a method; the panel uses the full ones above.
const shortMethod: Record<string, string> = { dhcp: 'DHCP', static: 'Static', ppp: 'PPP', slaac: 'SLAAC', dhcp6: 'DHCPv6', delegated: 'Delegated' };

export interface AddressLine {
  family: 'IPv4' | 'IPv6';
  method?: string;
  addresses: string[];
  // Saved static addresses the device does not have yet: a change not applied.
  pending: string[];
}

// addressLines pairs each family's saved method with the addresses it has: the live ones, link-local left out,
// and the saved static ones where they differ; with no device on the box, the saved ones alone. A family with no
// method and no address is left out.
// shortcut: addresses compare as written, case aside; an IPv6 address saved in another spelling shows as pending.
export function addressLines(live: LinkStatus | undefined, segment: Segment | undefined): AddressLine[] {
  return (['IPv4', 'IPv6'] as const).flatMap((family) => {
    const saved = segment?.addressing[family === 'IPv4' ? 'ipv4' : 'ipv6'] as { mode: string; addresses?: string[] } | undefined;
    const method = saved && saved.mode !== 'none' ? shortMethod[saved.mode] ?? saved.mode : undefined;
    const fixed = saved?.addresses ?? [];
    const now = (live?.addresses ?? []).filter((item) => item.family === family.toLowerCase() && item.scope !== 'link').map((item) => `${item.address}/${item.prefixLength}`);
    const addresses = live ? now : fixed;
    const pending = live ? fixed.filter((address) => !now.some((item) => item.toLowerCase() === address.toLowerCase())) : [];
    return method || addresses.length > 0 || pending.length > 0 ? [{ family, method, addresses, pending }] : [];
  });
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

export interface LinkOption {
  value: string;
  label: string;
}

// selfId and every link that depends directly or transitively on it (via parent or members) cannot become selfId's parent or member without a cycle.
function dependentsOf(links: SavedLink[], selfId: string | undefined): Set<string> {
  const blocked = new Set<string>();
  if (!selfId) return blocked;
  blocked.add(selfId);
  let grew = true;
  while (grew) {
    grew = false;
    for (const item of links) {
      if (blocked.has(item.id)) continue;
      if ((item.parent && blocked.has(item.parent)) || item.members?.some((member) => blocked.has(member))) {
        blocked.add(item.id);
        grew = true;
      }
    }
  }
  return blocked;
}

function withSelectedLinks(options: LinkOption[], selected: string[], links: SavedLink[]): LinkOption[] {
  const byId = new Map(links.map((item) => [item.id, item.name]));
  const present = new Set(options.map((item) => item.value));
  const missing: LinkOption[] = [];
  for (const id of selected) {
    if (!id || present.has(id)) continue;
    present.add(id);
    missing.push({ value: id, label: byId.get(id) ?? id });
  }
  return missing.length > 0 ? [...options, ...missing] : options;
}

// A VLAN parent must be a non-PPPoE link that is not a member of a bridge or bond, and cannot depend on this link.
export function vlanParentOptions(snapshot: Snapshot, selfId: string | undefined, selected: string): LinkOption[] {
  const links = snapshot.value.links;
  const enslaved = new Set(links.flatMap((item) => item.members ?? []));
  const blocked = dependentsOf(links, selfId);
  const options = links
    .filter((item) => item.kind !== 'pppoe' && !enslaved.has(item.id) && !blocked.has(item.id))
    .map((item) => ({ value: item.id, label: item.name }));
  return withSelectedLinks(options, selected ? [selected] : [], links);
}

// Bridge/bond members allow Ethernet/VLAN/bond for bridges (Ethernet only for bonds), cannot already belong to another master,
// cannot carry a segment or child VLAN/PPPoE links, and cannot depend on this link.
export function memberLinkOptions(snapshot: Snapshot, kind: 'bridge' | 'bond', selfId: string | undefined, selected: string[]): LinkOption[] {
  const links = snapshot.value.links;
  const otherMasters = new Set<string>();
  const parents = new Set<string>();
  for (const item of links) {
    if (item.id !== selfId) for (const member of item.members ?? []) otherMasters.add(member);
    if (item.parent) parents.add(item.parent);
  }
  const segmented = new Set(snapshot.value.segments.map((item) => item.link));
  const blocked = dependentsOf(links, selfId);
  const options = links
    .filter((item) => {
      if (blocked.has(item.id)) return false;
      if (kind === 'bond' ? item.kind !== 'ethernet' : item.kind !== 'ethernet' && item.kind !== 'vlan' && item.kind !== 'bond') return false;
      if (otherMasters.has(item.id) || segmented.has(item.id) || parents.has(item.id)) return false;
      return true;
    })
    .map((item) => ({ value: item.id, label: item.name }));
  return withSelectedLinks(options, selected, links);
}

export function draftOf(link: SavedLink | undefined, segment: Segment | undefined, live: LinkStatus | undefined, newKind?: VirtualLinkKind): InterfaceDraft {
  const kind: SavedLink['kind'] = link?.kind ?? newKind ?? 'ethernet';
  const addressing = (segment?.addressing ?? {}) as Fields;
  const ipv4 = (addressing.ipv4 ?? { mode: 'none' }) as Fields;
  const ipv6 = (addressing.ipv6 ?? { mode: 'none' }) as Fields;
  const dns = (addressing.dns ?? {}) as Fields;
  return {
    kind,
    name: link?.name ?? live?.name ?? '',
    enabled: link?.enabled ?? true,
    mtu: String(link?.mtu ?? live?.mtu ?? 1500),
    parent: link?.parent ?? '',
    vlanId: link?.vlanId !== undefined ? String(link.vlanId) : '',
    members: link?.members ? [...link.members] : [],
    bondMode: link?.mode ?? (kind === 'bond' ? '802.3ad' : ''),
    stp: link?.stp ?? false,
    stpPriority: text(link?.stpPriority),
    forwardDelay: text(link?.forwardDelay),
    helloTime: text(link?.helloTime),
    maxAge: text(link?.maxAge),
    multicastSnooping: link?.multicastSnooping ?? true,
    lacpRate: link?.lacpRate ?? '',
    miiMonitorMs: link ? text(link.miiMonitorMs) : kind === 'bond' ? '100' : '',
    transmitHashPolicy: link?.transmitHashPolicy ?? '',
    lldp: link?.lldp ?? 'off',
    emitLldp: link?.emitLldp ?? 'off',
    assigned: link ? Boolean(segment) : kind !== 'bond',
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

function putOptionalDefault<T>(target: Fields, saved: Fields, key: string, value: T, defaultValue: T) {
  if (value !== defaultValue) target[key] = value;
  else if (saved[key] === defaultValue) target[key] = defaultValue;
  else delete target[key];
}

function putOptionalInt(target: Fields, key: string, raw: string) {
  const trimmed = raw.trim();
  if (trimmed === '') delete target[key];
  else target[key] = Number(trimmed);
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
    : { name: draft.name.trim(), kind: draft.kind, mtu: Number(draft.mtu), enabled: draft.enabled };
  if (!link && draft.kind === 'ethernet') linkValue.permanentMac = (live?.permanentAddress ?? '').toLowerCase();
  if (draft.kind === 'vlan') {
    linkValue.parent = draft.parent;
    linkValue.vlanId = Number(draft.vlanId);
  } else if (draft.kind === 'bridge') {
    linkValue.members = [...draft.members];
    putOptionalDefault(linkValue, saved, 'stp', draft.stp, false);
    if (draft.stp) {
      putOptionalInt(linkValue, 'stpPriority', draft.stpPriority);
      putOptionalInt(linkValue, 'forwardDelay', draft.forwardDelay);
      putOptionalInt(linkValue, 'helloTime', draft.helloTime);
      putOptionalInt(linkValue, 'maxAge', draft.maxAge);
    } else {
      delete linkValue.stpPriority;
      delete linkValue.forwardDelay;
      delete linkValue.helloTime;
      delete linkValue.maxAge;
    }
    putOptionalDefault(linkValue, saved, 'multicastSnooping', draft.multicastSnooping, true);
  } else if (draft.kind === 'bond') {
    linkValue.mode = draft.bondMode;
    linkValue.members = [...draft.members];
    if (draft.bondMode === '802.3ad' && draft.lacpRate) linkValue.lacpRate = draft.lacpRate;
    else delete linkValue.lacpRate;
    if (draft.bondMode && hashPolicyModes.has(draft.bondMode) && draft.transmitHashPolicy) linkValue.transmitHashPolicy = draft.transmitHashPolicy;
    else delete linkValue.transmitHashPolicy;
    putOptionalInt(linkValue, 'miiMonitorMs', draft.miiMonitorMs);
  }
  if (draft.kind === 'ethernet') {
    putOptionalDefault(linkValue, saved, 'lldp', draft.lldp, 'off');
    putOptionalDefault(linkValue, saved, 'emitLldp', draft.emitLldp, 'off');
  } else {
    delete linkValue.lldp;
    delete linkValue.emitLldp;
  }
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
  links: [
    [/^\/name/, 'name'],
    [/^\/mtu/, 'mtu'],
    [/^\/parent/, 'parent'],
    [/^\/vlanId/, 'vlanId'],
    [/^\/members/, 'members'],
    [/^\/mode/, 'bondMode'],
    [/^\/stpPriority(\/|$)/, 'stpPriority'],
    [/^\/forwardDelay(\/|$)/, 'forwardDelay'],
    [/^\/helloTime(\/|$)/, 'helloTime'],
    [/^\/maxAge(\/|$)/, 'maxAge'],
    [/^\/stp(\/|$)/, 'stp'],
    [/^\/multicastSnooping(\/|$)/, 'multicastSnooping'],
    [/^\/lacpRate(\/|$)/, 'lacpRate'],
    [/^\/miiMonitorMs(\/|$)/, 'miiMonitorMs'],
    [/^\/transmitHashPolicy(\/|$)/, 'transmitHashPolicy'],
    [/^\/lldp(\/|$)/, 'lldp'],
    [/^\/emitLldp(\/|$)/, 'emitLldp'],
  ],
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
