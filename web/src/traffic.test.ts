import { expect, it } from 'vitest';
import { sample } from './test/traffic';
import { busiest, chosenLabel, figures, firstShown, merge, newest, rates, roundUp, thin, type RatePoint } from './traffic';
import type { Traffic } from './types';

const history = (interfaces: Traffic['interfaces'], complete = true): Traffic => ({ intervalSeconds: 5, complete, interfaces });

it('works out a rate in bits a second from each pair of neighbouring readings', () => {
  // 5 seconds apart: 625 bytes is 1000 bits a second.
  expect(rates([sample(0, 0, 0), sample(5, 625, 1250), sample(10, 625, 1250)], 5)).toEqual([
    { at: Date.UTC(2026, 10, 21, 12, 0, 5), rx: 1000, tx: 2000 },
    { at: Date.UTC(2026, 10, 21, 12, 0, 10), rx: 0, tx: 0 },
  ]);
  expect(rates([], 5)).toEqual([]);
  expect(rates([sample(0, 5)], 5)).toEqual([]);
});

it('makes a gap, never a spike, where a counter started again or readings are far apart or out of order', () => {
  const gap = { rx: null, tx: null };
  // The interface was replaced: its counters begin at zero again.
  expect(rates([sample(0, 9_000_000), sample(5, 100)], 5)[0]).toMatchObject(gap);
  expect(rates([sample(0, 0, 9_000_000), sample(5, 100, 100)], 5)[0]).toMatchObject(gap);
  // More than three intervals with nothing read: nothing is known of that time.
  expect(rates([sample(0, 0), sample(16, 100)], 5)[0]).toMatchObject(gap);
  // Exactly three intervals, a late tick, is still a rate.
  expect(rates([sample(0, 0), sample(15, 1875)], 5)[0]).toMatchObject({ rx: 1000 });
  // Two readings at once, or the clock set back.
  expect(rates([sample(5, 0), sample(5, 100)], 5)[0]).toMatchObject(gap);
  expect(rates([sample(10, 0), sample(5, 100)], 5)[0]).toMatchObject(gap);
});

it('thins many points to a line by averaging each stretch of time, gaps kept', () => {
  // Seven readings a second apart, to at most three points: stretches of three seconds on the clock.
  const points: RatePoint[] = [1, 2, 3, 4, 5, 6, 7].map((value) => ({ at: value * 1000, rx: value * 10, tx: value }));
  expect(thin(points, 3, 1)).toEqual([{ at: 2000, rx: 15, tx: 1.5 }, { at: 5000, rx: 40, tx: 4 }, { at: 7000, rx: 65, tx: 6.5 }]);
  // A new reading changes the newest stretch only: the line before it stays as it was.
  const next = thin([...points, { at: 8000, rx: 80, tx: 8 }], 3, 1);
  expect(next.slice(0, 2)).toEqual(thin(points, 3, 1).slice(0, 2));
  expect(next[2]).toEqual({ at: 8000, rx: 70, tx: 7 });
  // Few enough already: as they are.
  expect(thin(points, 7, 1)).toBe(points);
  expect(thin([], 3, 1)).toEqual([]);
  // A stretch with a gap in it averages what it has; one that is all gap is a gap.
  const gaps: RatePoint[] = [{ at: 0, rx: null, tx: null }, { at: 1000, rx: null, tx: null }, { at: 2000, rx: 10, tx: 1 }, { at: 3000, rx: null, tx: null }];
  expect(thin(gaps, 2, 1)).toEqual([{ at: 1000, rx: null, tx: null }, { at: 3000, rx: 10, tx: 1 }]);
});

it('gives the figures beside a graph: the rate now, the average and the peak', () => {
  const points: RatePoint[] = [{ at: 1, rx: 100, tx: 1 }, { at: 2, rx: null, tx: null }, { at: 3, rx: 300, tx: 3 }, { at: 4, rx: 200, tx: 2 }];
  expect(figures(points, (point) => point.rx, true)).toEqual({ current: 200, average: 200, peak: 300 });
  expect(figures(points, (point) => point.tx, true)).toEqual({ current: 2, average: 2, peak: 3 });
  // An interface that has gone still has its hour, but no rate now.
  expect(figures(points, (point) => point.rx, false)).toEqual({ current: null, average: 200, peak: 300 });
  // The newest point is a gap.
  expect(figures([...points, { at: 5, rx: null, tx: null }], (point) => point.rx, true)).toEqual({ current: null, average: 200, peak: 300 });
  expect(figures([], (point) => point.rx, true)).toEqual({ current: null, average: null, peak: null });
});

it('finds the newest reading of any interface, as the box wrote it', () => {
  expect(newest(history([{ name: 'a', samples: [sample(0, 0), sample(5, 0)] }, { name: 'b', samples: [sample(20, 0)] }, { name: 'c', samples: [] }]))).toEqual({ at: sample(20, 0).at, time: Date.UTC(2026, 10, 21, 12, 0, 20) });
  expect(newest(history([{ name: 'a', samples: [] }]))).toBeUndefined();
  expect(newest(history([]))).toBeUndefined();
});

it('adds an answer\'s readings to what is held, keeps the hour, and forgets an interface the box no longer names', () => {
  const held = history([{ name: 'ens18', samples: [sample(-3600, 1), sample(0, 2), sample(5, 3)] }, { name: 'veth0', samples: [sample(0, 9)] }]);
  const merged = merge(held, history([{ name: 'ens18', samples: [sample(10, 4)] }, { name: 'ens19', samples: [sample(10, 7)] }], false));
  expect(merged.interfaces.map((entry) => [entry.name, entry.samples.map((item) => item.rxBytes)])).toEqual([['ens18', [2, 3, 4]], ['ens19', [7]]]);
  // Nothing new for an interface: it keeps what it had.
  expect(merge(held, history([{ name: 'ens18', samples: [] }, { name: 'veth0', samples: [] }], false)).interfaces[0].samples.map((item) => item.rxBytes)).toEqual([2, 3]);
  // The whole history replaces what is held: the box restarted, or its clock was set back.
  const whole = history([{ name: 'ens18', samples: [sample(-60, 100)] }]);
  expect(merge(held, whole)).toBe(whole);
  expect(merge(undefined, whole)).toBe(whole);
  // Two answers to one question: the readings go in once, in order, so no gap is made of them.
  const twice = merge(merge(held, history([{ name: 'ens18', samples: [sample(10, 4), sample(15, 5)] }], false)), history([{ name: 'ens18', samples: [sample(10, 4), sample(15, 5), sample(20, 6)] }], false));
  expect(twice.interfaces[0].samples.map((item) => item.rxBytes)).toEqual([2, 3, 4, 5, 6]);
});

it('names the interface that moved the most', () => {
  expect(busiest([
    { name: 'ens19', samples: [sample(0, 0), sample(5, 1000)] },
    { name: 'ens18', samples: [sample(0, 500, 0), sample(5, 1000, 5000)] },
    { name: 'veth0', samples: [sample(0, 9_000_000)] },
  ])).toBe('ens18');
  // The first wins a tie, so the answer does not flip between two that move alike.
  expect(busiest([{ name: 'a', samples: [sample(0, 0), sample(5, 10)] }, { name: 'b', samples: [sample(0, 0), sample(5, 10)] }])).toBe('a');
  expect(busiest([{ name: 'only', samples: [] }])).toBe('only');
  expect(busiest([])).toBeUndefined();
});

it('chooses what shows first: all of one or two, else the wan and lan zones, else the busiest', () => {
  const entry = (name: string, moved = 0) => ({ name, samples: [sample(0, 0), sample(5, moved)] });
  const zones = new Map([['ens18', 'WAN'], ['ens19', 'lan'], ['ens20', 'guest']]);
  expect(firstShown(history([entry('ens18'), entry('ens19')]), new Map())).toEqual(['ens18', 'ens19']);
  expect(firstShown(history([entry('ens20', 900), entry('ens19'), entry('ens18'), entry('veth0')]), zones)).toEqual(['ens19', 'ens18']);
  // No zone named wan or lan, or no zones at all.
  expect(firstShown(history([entry('ens20', 900), entry('br0', 5), entry('veth0')]), zones)).toEqual(['ens20']);
  expect(firstShown(history([entry('a'), entry('b', 7), entry('c')]), new Map())).toEqual(['b']);
  expect(firstShown(history([]), zones)).toEqual([]);
});

it('labels the choice with the names while they fit, and with their number when they do not', () => {
  expect(chosenLabel(['ens18'])).toBe('ens18');
  expect(chosenLabel(['ens18', 'ens19'])).toBe('ens18, ens19');
  expect(chosenLabel(['ens18', 'ens19', 'ens20', 'ens21', 'ens22'])).toBe('5 selected');
  expect(chosenLabel([])).toBe('None selected');
});

it('rounds a scale up to the next 1, 2 or 5, never below the value', () => {
  expect([0, 1, 1.01, 2, 4_000_000, 5_000_001, 99, 100].map(roundUp)).toEqual([1, 1, 2, 2, 5_000_000, 10_000_000, 100, 100]);
});
