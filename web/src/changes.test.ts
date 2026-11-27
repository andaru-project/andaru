import { describe, expect, it } from 'vitest';
import { differences, fieldChanges, objectAt, referenceNames, resolveReferences } from './changes';

const rule = (id: string, name: string) => ({ id, name, enabled: true, action: 'accept' });

it('reads a list order as its IDs and a singleton as its value', () => {
  const declaration = { rules: [rule('rul_a', 'A'), rule('rul_b', 'B')], management: { segment: 'seg_lan' } };
  expect(objectAt(declaration, 'rules', 'rules')).toEqual(['rul_a', 'rul_b']);
  expect(objectAt(declaration, 'rules', 'rul_b')).toEqual(rule('rul_b', 'B'));
  expect(objectAt(declaration, 'management', 'management')).toEqual({ segment: 'seg_lan' });
  expect(objectAt({ aliases: { addresses: [{ id: 'adr_a', name: 'Printer' }], ports: [] } }, 'aliases.addresses', 'adr_a')).toEqual({ id: 'adr_a', name: 'Printer' });
});

it('shows a reorder as the order before and after', () => {
  const live = { rules: [rule('rul_a', 'A'), rule('rul_b', 'B')] };
  const head = { rules: [rule('rul_b', 'B'), rule('rul_a', 'A')] };
  expect(differences(live, head, [{ collection: 'rules', id: 'rules' }])).toEqual([
    { collection: 'rules', id: 'rules', label: 'rules order', kind: 'changed', before: ['rul_a', 'rul_b'], after: ['rul_b', 'rul_a'] },
  ]);
});

it('skips objects that end where they started and lists a repeated change once', () => {
  const live = { zones: [{ id: 'zon_a', name: 'LAN' }, { id: 'zon_b', name: 'Guest' }] };
  const head = { zones: [{ id: 'zon_a', name: 'Inside' }, { id: 'zon_b', name: 'Guest' }] };
  const result = differences(live, head, [
    { collection: 'zones', id: 'zon_a' },
    { collection: 'zones', id: 'zon_b' },
    { collection: 'zones', id: 'zon_a' },
    { collection: 'zones', id: 'zon_gone' },
  ]);
  expect(result.map((item) => [item.label, item.kind])).toEqual([['Inside', 'changed']]);
});

describe('fieldChanges', () => {
  it('lists only the fields that differ, with unset sides', () => {
    expect(fieldChanges({ id: 'z', name: 'lan', tags: ['a'] }, { id: 'z', name: 'home', tags: ['a'], description: 'x' })).toEqual([
      { field: 'name', before: 'lan', after: 'home' },
      { field: 'description', before: undefined, after: 'x' },
    ]);
  });
  it('shows two values whole when they differ only in key order', () => {
    expect(fieldChanges({ a: 1, b: 2 }, { b: 2, a: 1 })).toEqual([{ field: '', before: { a: 1, b: 2 }, after: { b: 2, a: 1 } }]);
  });
  it('treats a list order or a missing side as one value', () => {
    expect(fieldChanges(['a', 'b'], ['b', 'a'])).toEqual([{ field: '', before: ['a', 'b'], after: ['b', 'a'] }]);
  });
});

describe('referenceNames and resolveReferences', () => {
  it('resolves known IDs to names, keeps own id fields intact, and disambiguates duplicate names', () => {
    const live = {
      zones: [{ id: 'zon_lan', name: 'LAN' }],
      aliases: { addresses: [{ id: 'adr_nas', name: 'NAS' }], ports: [{ id: 'svc_web', name: 'HTTPS' }] },
      rules: [rule('rul_1', 'Allow'), rule('rul_2', 'Allow')],
    };
    const head = {
      zones: [{ id: 'zon_lan', name: 'Trusted' }],
      aliases: { addresses: [{ id: 'adr_nas', name: 'NAS' }], ports: [{ id: 'svc_web', name: 'HTTPS' }] },
      rules: [rule('rul_2', 'Allow'), rule('rul_1', 'Allow')],
    };
    const names = referenceNames(live, head);
    expect(resolveReferences({ id: 'rul_1', from: { zones: ['zon_lan'], addresses: ['adr_nas'] }, to: { zones: ['zon_firewall'] }, ports: ['svc_web'] }, names, 'rules')).toEqual({
      id: 'rul_1',
      from: { zones: ['Trusted'], addresses: ['NAS'] },
      to: { zones: ['This firewall'] },
      ports: ['HTTPS'],
    });
    expect(resolveReferences(['rul_1', 'rul_2'], names, 'rules')).toEqual(['Allow (rul_1)', 'Allow (rul_2)']);
    // Non-reference fields (such as PPPoE username or free-text name/description) are never substituted even when matching an ID.
    expect(resolveReferences({ id: 'lnk_1', name: 'zon_lan', username: 'zon_firewall', parent: 'zon_lan' }, names, 'links')).toEqual({
      id: 'lnk_1', name: 'zon_lan', username: 'zon_firewall', parent: 'Trusted',
    });
    expect(resolveReferences({ id: 'vip_1', name: 'Proxy', kind: 'proxyArp', segment: 'seg_wan', address: '203.0.113.50' }, new Map([['seg_wan', 'WAN']]), 'virtualAddresses')).toEqual({
      id: 'vip_1', name: 'Proxy', kind: 'proxyArp', segment: 'WAN', address: '203.0.113.50',
    });
  });
});
