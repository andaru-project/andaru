import { expect, it } from 'vitest';
import { ApiError } from './api';
import { draftOf, interfaceErrors, interfaceOperations, newZone, removeOperations } from './interfaceEdit';
import type { LinkStatus, SavedLink, SavedObject, Segment } from './types';

const port: LinkStatus = { ifIndex: 3, name: 'enp2s0', linkType: 'ether', mtu: 1500, operState: 'UP', flags: [], linkAddress: '52:54:00:AA:BB:02', addresses: [] };
const link: SavedObject<SavedLink> = { version: '4', value: { id: 'lnk_a', name: 'WAN port', kind: 'ethernet', mtu: 1500, enabled: true, permanentMac: '52:54:00:aa:bb:01' } };
const ra = { mode: 'router' };
const segment = { version: '9', value: {
  id: 'seg_a', name: 'WAN', link: 'lnk_a', zone: 'zon_wan', description: 'uplink',
  addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'dhcp6', ra, requestPrefixLength: 56, sendRelease: true } },
} } as unknown as SavedObject<Segment>;

it('adds a port with a new zone in one batch, the segment naming both by their local labels', () => {
  const draft = { ...draftOf(undefined, undefined, port), zone: newZone, newZone: 'DMZ', ipv4Mode: 'static', ipv4Addresses: '10.9.0.1/24', ipv6Mode: 'none' };
  const { operations, targets } = interfaceOperations(draft, undefined, undefined, port);
  expect(targets).toEqual(['link', 'zone', 'segment']);
  expect(operations).toEqual([
    { op: 'create', collection: 'links', localId: 'link', value: { name: 'enp2s0', kind: 'ethernet', permanentMac: '52:54:00:aa:bb:02', mtu: 1500, enabled: true } },
    { op: 'create', collection: 'zones', localId: 'zone', value: { name: 'DMZ' } },
    { op: 'create', collection: 'segments', value: { name: 'enp2s0', link: '@link', zone: '@zone', addressing: { ipv4: { mode: 'static', addresses: ['10.9.0.1/24'] }, ipv6: { mode: 'none' } } } },
  ]);
});

it('sends nothing for an unchanged interface, and keeps what the panel does not show while a mode stays', () => {
  const draft = draftOf(link.value, segment.value, undefined);
  expect(interfaceOperations(draft, link, segment, undefined).operations).toEqual([]);

  const { operations } = interfaceOperations({ ...draft, ipv6PrefixLength: '60' }, link, segment, undefined);
  expect(operations).toEqual([{ op: 'replace', collection: 'segments', id: 'seg_a', expectedVersion: '9', value: {
    ...segment.value, addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'dhcp6', ra, requestPrefixLength: 60, sendRelease: true } },
  } }]);
});

it('starts a new mode over, keeping only the router advertisements', () => {
  const draft = { ...draftOf(link.value, segment.value, undefined), ipv6Mode: 'slaac', mtu: '9000' };
  const { operations } = interfaceOperations(draft, link, segment, undefined);
  expect(operations[0]).toEqual({ op: 'replace', collection: 'links', id: 'lnk_a', expectedVersion: '4', value: { ...link.value, mtu: 9000 } });
  expect(operations[1].value?.addressing).toEqual({ ipv4: { mode: 'dhcp' }, ipv6: { mode: 'slaac', ra } });
});

it('takes the network off a port that keeps its link, and removes both when asked', () => {
  const draft = { ...draftOf(link.value, segment.value, undefined), assigned: false };
  expect(interfaceOperations(draft, link, segment, undefined).operations).toEqual([{ op: 'delete', collection: 'segments', id: 'seg_a', expectedVersion: '9' }]);
  expect(removeOperations(link, segment)).toEqual([
    { op: 'delete', collection: 'segments', id: 'seg_a', expectedVersion: '9' },
    { op: 'delete', collection: 'links', id: 'lnk_a', expectedVersion: '4' },
  ]);
});

it('puts each refusal beside the field of the object it names', () => {
  const error = new ApiError(422, 'VALIDATION_FAILED', 'Refused.', [
    { path: '/operations/0/value/mtu', message: 'Too small.' },
    { path: '/operations/1/value/addressing/ipv4/addresses/0', message: 'Not an address.' },
    { path: '', message: 'Object seg_a remains referenced or invalid.' },
  ]);
  expect(interfaceErrors(error, ['link', 'segment'])).toEqual(new Map([['mtu', 'Too small.'], ['ipv4', 'Not an address.'], ['', 'Object seg_a remains referenced or invalid.']]));
});
