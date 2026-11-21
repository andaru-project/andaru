import { describe, expect, it } from 'vitest';
import { differences, fieldChanges, objectAt } from './changes';

const rule = (id: string, name: string) => ({ id, name, enabled: true, action: 'accept' });

it('reads a list order as its IDs and a singleton as its value', () => {
  const declaration = { policy: { input: [rule('rul_a', 'A'), rule('rul_b', 'B')], forward: [] }, management: { segment: 'seg_lan' } };
  expect(objectAt(declaration, 'policy.input', 'policy.input')).toEqual(['rul_a', 'rul_b']);
  expect(objectAt(declaration, 'policy.input', 'rul_b')).toEqual(rule('rul_b', 'B'));
  expect(objectAt(declaration, 'management', 'management')).toEqual({ segment: 'seg_lan' });
  expect(objectAt({ objects: { addresses: [{ id: 'adr_a', name: 'Printer' }], services: [] } }, 'addresses', 'adr_a')).toEqual({ id: 'adr_a', name: 'Printer' });
});

it('shows a reorder as the order before and after', () => {
  const live = { policy: { input: [rule('rul_a', 'A'), rule('rul_b', 'B')] } };
  const head = { policy: { input: [rule('rul_b', 'B'), rule('rul_a', 'A')] } };
  expect(differences(live, head, [{ collection: 'policy.input', id: 'policy.input' }])).toEqual([
    { collection: 'policy.input', id: 'policy.input', label: 'policy.input order', kind: 'changed', before: ['rul_a', 'rul_b'], after: ['rul_b', 'rul_a'] },
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
