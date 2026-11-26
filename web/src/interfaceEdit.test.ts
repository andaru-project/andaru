import { expect, it } from 'vitest';
import { ApiError } from './api';
import { addressLines, draftOf, interfaceErrors, interfaceOperations, memberLinkOptions, newZoneOption, removeOperations, vlanParentOptions } from './interfaceEdit';
import type { LinkStatus, SavedLink, SavedObject, Segment, Snapshot } from './types';

const port: LinkStatus = { ifIndex: 3, name: 'enp2s0', linkType: 'ether', mtu: 1500, operState: 'UP', flags: [], linkAddress: '02:00:00:00:00:99', permanentAddress: '52:54:00:AA:BB:02', addresses: [] };
const link: SavedObject<SavedLink> = { version: '4', value: { id: 'lnk_a', name: 'WAN port', kind: 'ethernet', mtu: 1500, enabled: true, permanentMac: '52:54:00:aa:bb:01' } };
const ra = { mode: 'router' };
const segment = { version: '9', value: {
  id: 'seg_a', name: 'WAN', link: 'lnk_a', zone: 'zon_wan', description: 'uplink',
  addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'dhcp6', ra, requestPrefixLength: 56, sendRelease: true } },
} } as unknown as SavedObject<Segment>;

it('adds a port with a new zone in one batch, the segment naming both by their local labels', () => {
  const draft = { ...draftOf(undefined, undefined, port), zone: newZoneOption, newZone: 'DMZ', ipv4Mode: 'static', ipv4Addresses: '10.9.0.1/24', ipv6Mode: 'none' };
  expect(interfaceOperations(draft, undefined, undefined, port)).toEqual([
    { op: 'create', collection: 'links', localId: 'link', value: { name: 'enp2s0', kind: 'ethernet', permanentMac: '52:54:00:aa:bb:02', mtu: 1500, enabled: true } },
    { op: 'create', collection: 'zones', localId: 'zone', value: { name: 'DMZ' } },
    { op: 'create', collection: 'segments', value: { name: 'enp2s0', link: '@link', zone: '@zone', addressing: { ipv4: { mode: 'static', addresses: ['10.9.0.1/24'] }, ipv6: { mode: 'none' } } } },
  ]);
});

it('sends nothing for an unchanged interface, and keeps what the panel does not show while a mode stays', () => {
  const draft = draftOf(link.value, segment.value, undefined);
  expect(interfaceOperations(draft, link, segment, undefined)).toEqual([]);

  const operations = interfaceOperations({ ...draft, ipv6PrefixLength: '60' }, link, segment, undefined);
  expect(operations).toEqual([{ op: 'replace', collection: 'segments', id: 'seg_a', expectedVersion: '9', value: {
    ...segment.value, addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'dhcp6', ra, requestPrefixLength: 60, sendRelease: true } },
  } }]);
});

it('starts a new mode over, keeping only the router advertisements', () => {
  const draft = { ...draftOf(link.value, segment.value, undefined), ipv6Mode: 'slaac', mtu: '9000' };
  const operations = interfaceOperations(draft, link, segment, undefined);
  expect(operations[0]).toEqual({ op: 'replace', collection: 'links', id: 'lnk_a', expectedVersion: '4', value: { ...link.value, mtu: 9000 } });
  expect(operations[1].value?.addressing).toEqual({ ipv4: { mode: 'dhcp' }, ipv6: { mode: 'slaac', ra } });
});

it('takes the network off a port that keeps its link, and removes both when asked', () => {
  const draft = { ...draftOf(link.value, segment.value, undefined), assigned: false };
  expect(interfaceOperations(draft, link, segment, undefined)).toEqual([{ op: 'delete', collection: 'segments', id: 'seg_a', expectedVersion: '9' }]);
  expect(removeOperations(link, segment)).toEqual([
    { op: 'delete', collection: 'segments', id: 'seg_a', expectedVersion: '9' },
    { op: 'delete', collection: 'links', id: 'lnk_a', expectedVersion: '4' },
  ]);
});

it('puts each refusal beside the field of the object it names', () => {
  const draft = { ...draftOf(link.value, segment.value, undefined), mtu: '9000', ipv4Mode: 'static', ipv4Addresses: 'nonsense', ipv6PrefixLength: '60' };
  const operations = interfaceOperations(draft, link, segment, undefined);
  const error = new ApiError(422, 'VALIDATION_FAILED', 'Refused.', [
    { path: '/operations/0/value/mtu', message: 'Too small.' },
    { path: '/operations/1/value/addressing/ipv4/addresses/0', message: 'Not an address.' },
    { path: '/operations/1/value/addressing/ipv6/requestPrefixLength', message: 'Too long.' },
    { path: '/operations/1/value/addressing/ipv4', message: 'Wrong shape.' },
    { path: '', message: 'Object seg_a remains referenced or invalid.' },
  ]);
  expect(interfaceErrors(error, operations)).toEqual(new Map([
    ['mtu', 'Too small.'], ['ipv4Addresses', 'Not an address.'], ['ipv6PrefixLength', 'Too long.'], ['ipv4', 'Wrong shape.'], ['', 'Object seg_a remains referenced or invalid.'],
  ]));
});

it('leaves a PPPoE password out of a replace, so the engine keeps the one it has', () => {
  const pppoe = { version: '2', value: { id: 'lnk_p', name: 'DSL', kind: 'pppoe', mtu: 1492, enabled: true, parent: 'lnk_a', username: 'me', password: { secret: 'DSL_PASSWORD', isSet: true, version: '3' } } } as unknown as SavedObject<SavedLink>;
  const draft = draftOf(pppoe.value, undefined, undefined);
  expect(interfaceOperations({ ...draft, assigned: false }, pppoe, undefined, undefined)).toEqual([]);
  const rest: Record<string, unknown> = { ...pppoe.value };
  delete rest.password;
  expect(interfaceOperations({ ...draft, assigned: false, mtu: '1480' }, pppoe, undefined, undefined)).toEqual([
    { op: 'replace', collection: 'links', id: 'lnk_p', expectedVersion: '2', value: { ...rest, mtu: 1480 } },
  ]);
});

it('opens a link with no segment as carrying no network, and an added port as carrying one', () => {
  expect(draftOf(link.value, undefined, undefined).assigned).toBe(false);
  expect(draftOf(undefined, undefined, port).assigned).toBe(true);
});

it('sets the DNS servers and search domains, and leaves them out when emptied', () => {
  const draft = { ...draftOf(link.value, segment.value, undefined), dnsServers: '1.1.1.1, 9.9.9.9', searchDomains: 'lan' };
  expect(interfaceOperations(draft, link, segment, undefined)[0].value?.addressing).toMatchObject({ dns: { servers: ['1.1.1.1', '9.9.9.9'], searchDomains: ['lan'] } });
  const withDns = { ...segment, value: { ...segment.value, addressing: { ...segment.value.addressing, dns: { servers: ['1.1.1.1'] } } } } as SavedObject<Segment>;
  const cleared = interfaceOperations({ ...draftOf(link.value, withDns.value, undefined), dnsServers: '' }, link, withDns, undefined);
  expect(cleared[0].value?.addressing).not.toHaveProperty('dns');
});

it('drops a static address and gateway when the mode becomes DHCP', () => {
  const fixed = { ...segment, value: { ...segment.value, addressing: { ...segment.value.addressing, ipv4: { mode: 'static', addresses: ['10.0.0.2/24'], gateway: '10.0.0.1' } } } } as SavedObject<Segment>;
  const operations = interfaceOperations({ ...draftOf(link.value, fixed.value, undefined), ipv4Mode: 'dhcp' }, link, fixed, undefined);
  expect(operations[0].value?.addressing).toMatchObject({ ipv4: { mode: 'dhcp' } });
  expect((operations[0].value?.addressing as { ipv4: object }).ipv4).toEqual({ mode: 'dhcp' });
});

it('turns router advertisements off without IPv6, and leaves their lifetimes behind for a delegated prefix', () => {
  const lan = { ...segment, value: { ...segment.value, addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'static', addresses: ['2001:db8::1/64'], ra: { mode: 'unmanaged', validLifetime: 3600, preferredLifetime: 1800 } } } } } as unknown as SavedObject<Segment>;
  const draft = draftOf(link.value, lan.value, undefined);
  const ipv6 = (next: Partial<typeof draft>, saved = lan) => (interfaceOperations({ ...draft, ...next }, link, saved, undefined)[0].value?.addressing as { ipv6: object }).ipv6;
  expect(ipv6({ ipv6Mode: 'none' })).toEqual({ mode: 'none', ra: { mode: 'disabled', validLifetime: 3600, preferredLifetime: 1800 } });
  // The addresses stay, shown beside the delegated prefix.
  expect(ipv6({ ipv6Mode: 'delegated', ipv6From: 'seg_wan', ipv6SubnetId: '1' }))
    .toEqual({ mode: 'delegated', ra: { mode: 'unmanaged' }, from: 'seg_wan', subnetId: '1', addresses: ['2001:db8::1/64'] });
  const older = { ...lan, value: { ...lan.value, addressing: { ...lan.value.addressing, ipv6: { mode: 'static', addresses: ['2001:db8::1/64'], ra: { enabled: true, managed: false, otherInfo: true } } } } } as unknown as SavedObject<Segment>;
  expect(ipv6({ ipv6Mode: 'none' }, older)).toEqual({ mode: 'none', ra: { enabled: false, managed: false, otherInfo: true } });
});

it('pairs each method with the live addresses, and says which saved ones are not applied yet', () => {
  const lan = { id: 'seg_l', name: 'lan', link: 'lnk_l', zone: 'zon_l', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24', '192.168.9.1/24'] }, ipv6: { mode: 'none' } } } as unknown as Segment;
  const device = { ...port, addresses: [
    { family: 'ipv4' as const, address: '192.168.1.1', prefixLength: 24, scope: 'global' },
    { family: 'ipv6' as const, address: 'fe80::1', prefixLength: 64, scope: 'link' },
  ] };
  expect(addressLines(device, lan)).toEqual([{ family: 'IPv4', method: 'Static', addresses: ['192.168.1.1/24'], pending: ['192.168.9.1/24'] }]);
  // No device: the saved addresses are what there is to show.
  expect(addressLines(undefined, lan)).toEqual([{ family: 'IPv4', method: 'Static', addresses: ['192.168.1.1/24', '192.168.9.1/24'], pending: [] }]);
  // No segment: the live addresses, with no method.
  expect(addressLines(device, undefined)).toEqual([{ family: 'IPv4', method: undefined, addresses: ['192.168.1.1/24'], pending: [] }]);
});

it('builds create and replace operations for VLAN, bridge, and bond links', () => {
  const vlanDraft = { ...draftOf(undefined, undefined, undefined, 'vlan'), name: 'Guest VLAN', parent: 'lnk_a', vlanId: '20', segmentName: 'Guest', zone: 'zon_guest', ipv4Mode: 'static', ipv4Addresses: '10.20.0.1/24', ipv6Mode: 'none' };
  expect(interfaceOperations(vlanDraft, undefined, undefined, undefined)).toEqual([
    { op: 'create', collection: 'links', localId: 'link', value: { name: 'Guest VLAN', kind: 'vlan', parent: 'lnk_a', vlanId: 20, mtu: 1500, enabled: true } },
    { op: 'create', collection: 'segments', value: { name: 'Guest', link: '@link', zone: 'zon_guest', addressing: { ipv4: { mode: 'static', addresses: ['10.20.0.1/24'] }, ipv6: { mode: 'none' } } } },
  ]);

  const bridgeDraft = { ...draftOf(undefined, undefined, undefined, 'bridge'), name: 'LAN bridge', members: ['lnk_b', 'lnk_c'], assigned: false };
  expect(interfaceOperations(bridgeDraft, undefined, undefined, undefined)).toEqual([
    { op: 'create', collection: 'links', localId: 'link', value: { name: 'LAN bridge', kind: 'bridge', members: ['lnk_b', 'lnk_c'], mtu: 1500, enabled: true } },
  ]);

  const bondDraft = draftOf(undefined, undefined, undefined, 'bond');
  expect(bondDraft.assigned).toBe(false);
  expect(bondDraft.bondMode).toBe('802.3ad');
  expect(bondDraft.miiMonitorMs).toBe('100');
  expect(interfaceOperations({ ...bondDraft, name: 'Uplink bond', bondMode: 'active-backup', members: ['lnk_b', 'lnk_c'] }, undefined, undefined, undefined)).toEqual([
    { op: 'create', collection: 'links', localId: 'link', value: { name: 'Uplink bond', kind: 'bond', mode: 'active-backup', members: ['lnk_b', 'lnk_c'], mtu: 1500, enabled: true, miiMonitorMs: 100 } },
  ]);

  // Editing an existing VLAN without changes sends nothing; changing its tag replaces only the link.
  const savedVlan: SavedObject<SavedLink> = { version: '3', value: { id: 'lnk_v', name: 'Guest VLAN', kind: 'vlan', mtu: 1500, enabled: true, parent: 'lnk_a', vlanId: 20 } };
  const unchanged = draftOf(savedVlan.value, undefined, undefined);
  expect(interfaceOperations(unchanged, savedVlan, undefined, undefined)).toEqual([]);
  expect(interfaceOperations({ ...unchanged, vlanId: '30' }, savedVlan, undefined, undefined)).toEqual([
    { op: 'replace', collection: 'links', id: 'lnk_v', expectedVersion: '3', value: { ...savedVlan.value, vlanId: 30 } },
  ]);
});

it('maps virtual link refusal paths to their fields', () => {
  const operations = interfaceOperations({ ...draftOf(undefined, undefined, undefined, 'bond'), name: 'Bond 0', members: ['lnk_a'] }, undefined, undefined, undefined);
  const error = new ApiError(422, 'VALIDATION_FAILED', 'Refused.', [
    { path: '/operations/0/value/parent', message: 'Bad parent.' },
    { path: '/operations/0/value/vlanId', message: 'Duplicate tag.' },
    { path: '/operations/0/value/members/0', message: 'Invalid member.' },
    { path: '/operations/0/value/mode', message: 'Unsupported mode.' },
  ]);
  expect(interfaceErrors(error, operations)).toEqual(new Map([
    ['parent', 'Bad parent.'],
    ['vlanId', 'Duplicate tag.'],
    ['members', 'Invalid member.'],
    ['bondMode', 'Unsupported mode.'],
  ]));
});

it('filters eligible VLAN parents and bridge/bond members according to link rules', () => {
  const snapshot: Snapshot = {
    state: 'saved',
    savedVersion: '5',
    value: {
      links: [
        { id: 'lnk_free', name: 'Free eth', kind: 'ethernet', mtu: 1500, enabled: true, permanentMac: '52:54:00:00:00:01' },
        { id: 'lnk_seg', name: 'Segmented eth', kind: 'ethernet', mtu: 1500, enabled: true, permanentMac: '52:54:00:00:00:02' },
        { id: 'lnk_trunk', name: 'Trunk eth', kind: 'ethernet', mtu: 1500, enabled: true, permanentMac: '52:54:00:00:00:03' },
        { id: 'lnk_vlan', name: 'VLAN 10', kind: 'vlan', mtu: 1500, enabled: true, parent: 'lnk_trunk', vlanId: 10 },
        { id: 'lnk_pppoe', name: 'ISP PPPoE', kind: 'pppoe', mtu: 1492, enabled: true, parent: 'lnk_trunk' },
        { id: 'lnk_bond_member', name: 'Bond member eth', kind: 'ethernet', mtu: 1500, enabled: true, permanentMac: '52:54:00:00:00:04' },
        { id: 'lnk_bond', name: 'Bond 0', kind: 'bond', mtu: 1500, enabled: true, mode: '802.3ad', members: ['lnk_bond_member'] },
        { id: 'lnk_br', name: 'Bridge 0', kind: 'bridge', mtu: 1500, enabled: true, members: [] },
        { id: 'lnk_br_vlan', name: 'Bridge VLAN 50', kind: 'vlan', mtu: 1500, enabled: true, parent: 'lnk_br', vlanId: 50 },
      ],
      segments: [{ id: 'seg_1', name: 'LAN', link: 'lnk_seg', zone: 'zon_1', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } } }],
      zones: [{ id: 'zon_1', name: 'LAN' }],
    },
  };

  // VLAN parents exclude PPPoE, enslaved members, self, and direct/transitive dependents of self.
  expect(vlanParentOptions(snapshot, undefined, '').map((o) => o.value)).toEqual(['lnk_free', 'lnk_seg', 'lnk_trunk', 'lnk_vlan', 'lnk_bond', 'lnk_br', 'lnk_br_vlan']);
  expect(vlanParentOptions(snapshot, 'lnk_trunk', '').map((o) => o.value)).toEqual(['lnk_free', 'lnk_seg', 'lnk_bond', 'lnk_br', 'lnk_br_vlan']);
  expect(vlanParentOptions(snapshot, 'lnk_br', '').map((o) => o.value)).toEqual(['lnk_free', 'lnk_seg', 'lnk_trunk', 'lnk_vlan', 'lnk_bond']);

  // Bridge members allow unassigned Ethernet, VLAN, and bond links with no children or other master, excluding dependents (lnk_br_vlan depends on lnk_br).
  expect(memberLinkOptions(snapshot, 'bridge', 'lnk_br', []).map((o) => o.value)).toEqual(['lnk_free', 'lnk_vlan', 'lnk_bond']);
  // Bond members allow only unassigned Ethernet ports with no children or other master, keeping and deduplicating current members of self.
  expect(memberLinkOptions(snapshot, 'bond', undefined, []).map((o) => o.value)).toEqual(['lnk_free']);
  expect(memberLinkOptions(snapshot, 'bond', 'lnk_bond', ['lnk_bond_member', 'lnk_missing', 'lnk_missing']).map((o) => o.value)).toEqual(['lnk_free', 'lnk_bond_member', 'lnk_missing']);
});

it('serializes bridge STP/L2 options and bond L2 options, and strips mode-incompatible fields', () => {
  // Bridge with STP + timers + multicastSnooping=false.
  const brDraft = {
    ...draftOf(undefined, undefined, undefined, 'bridge'),
    name: 'Core bridge',
    assigned: false,
    stp: true,
    stpPriority: '4096',
    forwardDelay: '12',
    helloTime: '2',
    maxAge: '18',
    multicastSnooping: false,
  };
  expect(interfaceOperations(brDraft, undefined, undefined, undefined)).toEqual([
    {
      op: 'create',
      collection: 'links',
      localId: 'link',
      value: {
        name: 'Core bridge',
        kind: 'bridge',
        members: [],
        mtu: 1500,
        enabled: true,
        stp: true,
        stpPriority: 4096,
        forwardDelay: 12,
        helloTime: 2,
        maxAge: 18,
        multicastSnooping: false,
      },
    },
  ]);

  // Turning STP off on an existing bridge removes STP timers cleanly; unchanged bridge with explicit defaults emits nothing.
  const savedBr: SavedObject<SavedLink> = {
    version: '4',
    value: { id: 'lnk_br', name: 'Core bridge', kind: 'bridge', mtu: 1500, enabled: true, members: [], stp: true, stpPriority: 4096, forwardDelay: 12 },
  };
  expect(interfaceOperations(draftOf(savedBr.value, undefined, undefined), savedBr, undefined, undefined)).toEqual([]);
  expect(interfaceOperations({ ...draftOf(savedBr.value, undefined, undefined), stp: false }, savedBr, undefined, undefined)).toEqual([
    { op: 'replace', collection: 'links', id: 'lnk_br', expectedVersion: '4', value: { id: 'lnk_br', name: 'Core bridge', kind: 'bridge', mtu: 1500, enabled: true, members: [] } },
  ]);

  // Bond with 802.3ad LACP rate, hash policy, and MII monitor; switching mode to active-backup drops lacpRate and transmitHashPolicy.
  const savedBond: SavedObject<SavedLink> = {
    version: '5',
    value: { id: 'lnk_bo', name: 'Uplink', kind: 'bond', mtu: 1500, enabled: true, mode: '802.3ad', members: ['lnk_a'], lacpRate: 'fast', transmitHashPolicy: 'layer3+4', miiMonitorMs: 100 },
  };
  expect(interfaceOperations(draftOf(savedBond.value, undefined, undefined), savedBond, undefined, undefined)).toEqual([]);
  expect(interfaceOperations({ ...draftOf(savedBond.value, undefined, undefined), bondMode: 'active-backup' }, savedBond, undefined, undefined)).toEqual([
    { op: 'replace', collection: 'links', id: 'lnk_bo', expectedVersion: '5', value: { id: 'lnk_bo', name: 'Uplink', kind: 'bond', mtu: 1500, enabled: true, mode: 'active-backup', members: ['lnk_a'], miiMonitorMs: 100 } },
  ]);

  // Field error mapping for bridge STP and bond L2 paths.
  const error = new ApiError(422, 'VALIDATION_FAILED', 'Refused.', [
    { path: '/operations/0/value/stpPriority', message: 'Bad priority.' },
    { path: '/operations/0/value/stp', message: 'Bad stp.' },
    { path: '/operations/0/value/forwardDelay', message: 'Bad delay.' },
    { path: '/operations/0/value/lacpRate', message: 'Needs 802.3ad.' },
    { path: '/operations/0/value/miiMonitorMs', message: 'Too large.' },
    { path: '/operations/0/value/transmitHashPolicy', message: 'Unsupported mode.' },
  ]);
  expect(interfaceErrors(error, [{ op: 'create', collection: 'links', value: {} }])).toEqual(new Map([
    ['stpPriority', 'Bad priority.'],
    ['stp', 'Bad stp.'],
    ['forwardDelay', 'Bad delay.'],
    ['lacpRate', 'Needs 802.3ad.'],
    ['miiMonitorMs', 'Too large.'],
    ['transmitHashPolicy', 'Unsupported mode.'],
  ]));
});

it('serializes per-link LLDP options and maps LLDP refusal paths', () => {
  const draft = { ...draftOf(link.value, segment.value, undefined), lldp: 'receive' as const, emitLldp: 'nearest-bridge' as const };
  expect(interfaceOperations(draft, link, segment, undefined)).toEqual([
    { op: 'replace', collection: 'links', id: 'lnk_a', expectedVersion: '4', value: { ...link.value, lldp: 'receive', emitLldp: 'nearest-bridge' } },
  ]);

  const savedWithLldp: SavedObject<SavedLink> = { version: '5', value: { ...link.value, lldp: 'routers-only', emitLldp: 'customer-bridge' } };
  expect(interfaceOperations(draftOf(savedWithLldp.value, undefined, undefined), savedWithLldp, undefined, undefined)).toEqual([]);
  expect(interfaceOperations({ ...draftOf(savedWithLldp.value, undefined, undefined), lldp: 'off', emitLldp: 'off' }, savedWithLldp, undefined, undefined)).toEqual([
    { op: 'replace', collection: 'links', id: 'lnk_a', expectedVersion: '5', value: { ...link.value } },
  ]);

  const error = new ApiError(422, 'VALIDATION_FAILED', 'Refused.', [
    { path: '/operations/0/value/lldp', message: 'Bad lldp.' },
    { path: '/operations/0/value/emitLldp', message: 'Bad emitLldp.' },
  ]);
  expect(interfaceErrors(error, [{ op: 'replace', collection: 'links', value: {} }])).toEqual(new Map([
    ['lldp', 'Bad lldp.'],
    ['emitLldp', 'Bad emitLldp.'],
  ]));
});
