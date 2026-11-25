import { expect, it } from 'vitest';
import { arrange, columnsFor, dropAt, readAcross, withShown } from './dashboardLayout';

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

it('keeps a hand-made arrangement, each name once and only names it knows', () => {
  expect(arrange(['a', 'b', 'c'], { columns: [['b', 'gone'], ['a', 'b'], 'x' as never] })).toEqual({ order: ['a', 'b', 'c'], hidden: [], columns: [['b'], ['a'], []] });
});

it('lays the shown widgets out as arranged at that column count, and deals them in turn otherwise', () => {
  // Dealt in turn: no arrangement, or one made for another column count.
  expect(columnsFor(['a', 'b', 'c', 'd', 'e'], 3)).toEqual([['a', 'd'], ['b', 'e'], ['c']]);
  expect(columnsFor(['a', 'b', 'c'], 2, [['a'], ['b'], ['c']])).toEqual([['a', 'c'], ['b']]);
  // As arranged, a column longer than the one before it included; a hidden widget drops out, a new one joins the shortest.
  expect(columnsFor(['a', 'b', 'c', 'n'], 3, [['a'], [], ['b', 'c', 'h']])).toEqual([['a'], ['n'], ['b', 'c']]);
});

it('reads columns across, row by row', () => {
  expect(readAcross([['a', 'd'], ['b'], ['c', 'e', 'f']])).toEqual(['a', 'b', 'c', 'd', 'e', 'f']);
  expect(readAcross([])).toEqual([]);
});

it('drops a widget at the end of any column, or where another is, as a sortable list does', () => {
  const columns = [['a', 'd'], ['b', 'e'], ['c']];
  // Below every widget of another column, which may then be the longest.
  expect(dropAt(columns, 'a', 1)).toEqual([['d'], ['b', 'e', 'a'], ['c']]);
  expect(dropAt(columns, 'a', 2)).toEqual([['d'], ['b', 'e'], ['c', 'a']]);
  // Onto another column's widget: before it. Down its own column: after the one it is over; up: before it.
  expect(dropAt(columns, 'a', 1, 'e')).toEqual([['d'], ['b', 'a', 'e'], ['c']]);
  expect(dropAt(columns, 'a', 0, 'd')).toEqual([['d', 'a'], ['b', 'e'], ['c']]);
  expect(dropAt(columns, 'd', 0, 'a')).toEqual([['d', 'a'], ['b', 'e'], ['c']]);
  // Its own column's end, and a column that is not there.
  expect(dropAt(columns, 'a', 0)).toEqual([['d', 'a'], ['b', 'e'], ['c']]);
  expect(dropAt(columns, 'a', 5)).toBe(columns);
});

it('puts the shown widgets in a new order, leaving the others in their places', () => {
  // "h" is hidden: it keeps the second place.
  expect(withShown(['a', 'h', 'b', 'c'], ['a', 'b', 'c'], ['b', 'c', 'a'])).toEqual(['b', 'h', 'c', 'a']);
});
