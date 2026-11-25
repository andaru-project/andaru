import { expect, it } from 'vitest';
import { ApiError } from './api';
import { draftOf, interfaceErrors, interfaceOperations, newZoneOption, removeOperations } from './interfaceEdit';
import type { LinkStatus, SavedLink, SavedObject, Segment } from './types';

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

it('leaves router advertisements behind with IPv6, and their lifetimes behind for a delegated prefix', () => {
  const lan = { ...segment, value: { ...segment.value, addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'static', addresses: ['2001:db8::1/64'], ra: { mode: 'unmanaged', validLifetime: 3600, preferredLifetime: 1800 } } } } } as unknown as SavedObject<Segment>;
  const draft = draftOf(link.value, lan.value, undefined);
  expect((interfaceOperations({ ...draft, ipv6Mode: 'none' }, link, lan, undefined)[0].value?.addressing as { ipv6: object }).ipv6).toEqual({ mode: 'none' });
  expect((interfaceOperations({ ...draft, ipv6Mode: 'delegated', ipv6From: 'seg_wan', ipv6SubnetId: '1' }, link, lan, undefined)[0].value?.addressing as { ipv6: object }).ipv6)
    .toEqual({ mode: 'delegated', ra: { mode: 'unmanaged' }, from: 'seg_wan', subnetId: '1' });
});
