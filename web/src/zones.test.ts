import { expect, it } from 'vitest';
import type { Preview, Snapshot } from './types';
import { zoneColor, zonesByInterface } from './zones';

const link = (number: number, name: string, kind = 'ethernet', enabled = true) => ({ id: `lnk_${String(number).repeat(8)}-${String(number).repeat(4)}-4${String(number).repeat(3)}-8${String(number).repeat(3)}-${String(number).repeat(12)}`, name, kind, mtu: 1500, enabled });
const segment = (id: string, linkId: string, zone: string) => ({ id, name: id, link: linkId, zone, addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } } });

it('finds each interface\'s zone in the saved configuration, by the interface\'s name on the box', () => {
  const links = [link(1, 'wan'), link(2, 'guests', 'vlan'), link(3, 'spare'), link(4, 'unplugged'), link(5, 'off', 'ethernet', false), link(6, 'broken'), link(7, 'twin-a'), link(8, 'twin-b'), link(9, 'shadowed'), link(0, 'shadow', 'ethernet', false)];
  const snapshot = {
    state: 'saved', savedVersion: '1',
    value: {
      links,
      segments: [segment('s1', links[0].id, 'z1'), segment('s2', links[1].id, 'z2'), segment('s4', links[3].id, 'z1'), segment('s5', links[4].id, 'z1'), segment('s6', links[5].id, 'z1'), segment('s7', links[6].id, 'z1'), segment('s8', links[7].id, 'z2'), segment('s9', links[8].id, 'z1')],
      zones: [{ id: 'z1', name: 'wan' }, { id: 'z2', name: 'guest' }],
    },
  } as Snapshot;
  const resolved = (index: number, name: string) => ({ link: links[index].id, interface: name });
  const preview = {
    resolved: [resolved(0, 'ens18'), resolved(2, 'ens20'), resolved(4, 'ens21'), resolved(5, 'ens22'), resolved(6, 'ens23'), resolved(7, 'ens23'), resolved(8, 'ens24'), resolved(9, 'ens24')],
    findings: [{ severity: 'error', code: 'HARDWARE_AMBIGUOUS', path: '/links/5/permanentMac', message: 'Two devices match.' }, { severity: 'warning', code: 'SOMETHING_ELSE', path: '/links/0', message: 'Not about binding.' }],
  } as Preview;
  // An Ethernet link is the interface its MAC resolved to; a VLAN's name comes from its ID. Left out: a link
  // with no segment, one that resolved to nothing, a disabled one, one the renderer will not bind, and an
  // interface two links claim, also when one of the two is disabled: the Interfaces page shows that as a conflict.
  expect([...zonesByInterface(snapshot, preview)]).toEqual([['ens18', 'wan'], ['vl222222222222', 'guest']]);
});

it('gives a zone one colour everywhere: green inside, red outside, and the same for a name each time', () => {
  expect(zoneColor('lan')).toBe('green');
  expect(zoneColor('WAN')).toBe('red');
  expect(zoneColor('lan', 'violet')).toBe('violet');
  expect(zoneColor('lan', 'not-a-color')).toBe('green');
  expect(zoneColor('Guest')).toBe(zoneColor('guest'));
  const others = ['guest', 'dmz', 'iot', 'servers', 'mgmt', 'vpn', 'voip', 'kids', 'work', 'lab', 'lan2', 'wan2', 'constructor', 'toString', ''];
  for (const name of others) {
    // Off the two colours that mean inside and outside, and only colours whose text the theme keeps readable.
    expect(['blue', 'orange', 'teal', 'pink', 'cyan', 'lime']).toContain(zoneColor(name));
  }
  // Names spread over the palette: a handful of zones do not all look alike.
  expect(new Set(others.map((name) => zoneColor(name))).size).toBeGreaterThanOrEqual(4);
});
