import { expect, it } from 'vitest';
import type { Preview, Snapshot } from './types';
import { zoneColor, zonesByInterface } from './zones';

it('finds each interface\'s zone in the saved configuration, by the interface\'s name on the box', () => {
  const snapshot = {
    state: 'saved', savedVersion: '1',
    value: {
      links: [
        { id: 'lnk_11111111-1111-4111-8111-111111111111', name: 'wan', kind: 'ethernet', mtu: 1500, enabled: true },
        { id: 'lnk_22222222-2222-4222-8222-222222222222', name: 'guests', kind: 'vlan', mtu: 1500, enabled: true },
        { id: 'lnk_33333333-3333-4333-8333-333333333333', name: 'spare', kind: 'ethernet', mtu: 1500, enabled: true },
        { id: 'lnk_44444444-4444-4444-8444-444444444444', name: 'unplugged', kind: 'ethernet', mtu: 1500, enabled: true },
      ],
      segments: [
        { id: 's1', name: 'wan', link: 'lnk_11111111-1111-4111-8111-111111111111', zone: 'z1', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'slaac' } } },
        { id: 's2', name: 'guests', link: 'lnk_22222222-2222-4222-8222-222222222222', zone: 'z2', addressing: { ipv4: { mode: 'static' }, ipv6: { mode: 'none' } } },
        { id: 's4', name: 'unplugged', link: 'lnk_44444444-4444-4444-8444-444444444444', zone: 'z1', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } } },
      ],
      zones: [{ id: 'z1', name: 'wan' }, { id: 'z2', name: 'guest' }],
    },
  } as Snapshot;
  const preview = { resolved: [{ link: 'lnk_11111111-1111-4111-8111-111111111111', interface: 'ens18' }, { link: 'lnk_33333333-3333-4333-8333-333333333333', interface: 'ens20' }] } as Preview;
  // An Ethernet link is the interface its MAC resolved to; a VLAN's name comes from its ID. A link with no
  // segment is in no zone, and one that resolved to no interface is not on the box.
  expect([...zonesByInterface(snapshot, preview)]).toEqual([['ens18', 'wan'], ['vl222222222222', 'guest']]);
});

it('gives a zone one colour everywhere: green inside, red outside, and the same for a name each time', () => {
  expect(zoneColor('lan')).toBe('green');
  expect(zoneColor('WAN')).toBe('red');
  expect(zoneColor('guest')).toBe(zoneColor('guest'));
  // Other zones keep off the two colours that mean inside and outside.
  for (const name of ['guest', 'dmz', 'iot', 'servers', 'mgmt', 'vpn', '']) expect(['green', 'red']).not.toContain(zoneColor(name));
  expect(new Set(['guest', 'dmz', 'iot', 'servers'].map(zoneColor)).size).toBeGreaterThan(1);
});
