import { expect, it } from 'vitest';
import { arrange, moved } from './dashboardLayout';

it('puts a stored layout over the widgets this version has', () => {
  expect(arrange(['a', 'b', 'c'], {})).toEqual({ order: ['a', 'b', 'c'], hidden: [] });
  // "gone" is a widget of another version; "c" is new since the layout was saved and joins at the end, shown.
  expect(arrange(['a', 'b', 'c'], { order: ['b', 'gone', 'a'], hidden: ['a', 'gone'] })).toEqual({ order: ['b', 'a', 'c'], hidden: ['a'] });
});

it('takes nothing about a stored layout on trust', () => {
  const plain = { order: ['a', 'b', 'c'], hidden: [] };
  for (const order of [null, 'cab', {}, 5, [1, null, {}]]) expect(arrange(['a', 'b', 'c'], { order } as never)).toEqual(plain);
  // A string is not a list: "xxbxx" must not hide "b".
  for (const hidden of [null, 'xxbxx', {}, 5]) expect(arrange(['a', 'b', 'c'], { hidden } as never)).toEqual(plain);
  for (const stored of [undefined, null, 'x', []]) expect(arrange(['a', 'b', 'c'], stored as never)).toEqual(plain);
  // A name counts once: twice would be two widgets under one key.
  expect(arrange(['a', 'b', 'c'], { order: ['b', 'b', 'a'], hidden: ['a', 'a'] })).toEqual({ order: ['b', 'a', 'c'], hidden: ['a'] });
});

it('moves a shown widget and leaves the others in their places', () => {
  expect(moved(['a', 'b', 'c'], ['a', 'b', 'c'], 'a', 'c')).toEqual(['b', 'c', 'a']);
  expect(moved(['a', 'b', 'c'], ['a', 'b', 'c'], 'c', 'a')).toEqual(['c', 'a', 'b']);
  expect(moved(['a', 'b', 'c'], ['a', 'b', 'c'], 'b', 'b')).toEqual(['a', 'b', 'c']);
  // "h" is hidden: it keeps the second place while "a" and "c" trade around it.
  expect(moved(['a', 'h', 'b', 'c'], ['a', 'b', 'c'], 'a', 'c')).toEqual(['b', 'h', 'c', 'a']);
  // A widget that is not on the page cannot be moved, nor moved onto.
  expect(moved(['a', 'h', 'b'], ['a', 'b'], 'h', 'a')).toEqual(['a', 'h', 'b']);
  expect(moved(['a', 'h', 'b'], ['a', 'b'], 'a', 'h')).toEqual(['a', 'h', 'b']);
});
