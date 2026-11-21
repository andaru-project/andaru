import { expect, it } from 'vitest';
import { arrange, moved } from './dashboardLayout';

it('puts a stored layout over the widgets this version has', () => {
  expect(arrange(['a', 'b', 'c'], {})).toEqual({ order: ['a', 'b', 'c'], hidden: [] });
  // "gone" is a widget of another version; "c" is new since the layout was saved and joins at the end, shown.
  expect(arrange(['a', 'b', 'c'], { order: ['b', 'gone', 'a'], hidden: ['a', 'gone'] })).toEqual({ order: ['b', 'a', 'c'], hidden: ['a'] });
});

it('moves a shown widget and leaves the others in their places', () => {
  expect(moved(['a', 'b', 'c'], ['a', 'b', 'c'], 'a', 'c')).toEqual(['b', 'c', 'a']);
  expect(moved(['a', 'b', 'c'], ['a', 'b', 'c'], 'c', 'a')).toEqual(['c', 'a', 'b']);
  expect(moved(['a', 'b', 'c'], ['a', 'b', 'c'], 'b', 'b')).toEqual(['a', 'b', 'c']);
  // "h" is hidden: it keeps the second place while "a" and "c" trade around it.
  expect(moved(['a', 'h', 'b', 'c'], ['a', 'b', 'c'], 'a', 'c')).toEqual(['b', 'h', 'c', 'a']);
});
