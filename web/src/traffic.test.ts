import { expect, it } from 'vitest';
import { busiest, cpuShare, rates, summary } from './traffic';
import type { TrafficSample } from './types';

const sample = (second: number, rxBytes: number, txBytes = 0): TrafficSample => ({
  at: new Date(Date.UTC(2026, 10, 21, 12, 0, second)).toISOString(), rxBytes, txBytes, rxPackets: 0, txPackets: 0, rxErrors: 0, txErrors: 0, rxDropped: 0, txDropped: 0,
});

it('works out a rate in bits a second from each pair of neighbouring readings', () => {
  // 30 seconds apart: 3750 bytes is 1000 bit/s.
  expect(rates([sample(0, 0, 0), sample(30, 3750, 7500), sample(60, 3750, 7500)], 30)).toEqual([
    { at: Date.UTC(2026, 10, 21, 12, 0, 30), rx: 1000, tx: 2000 },
    { at: Date.UTC(2026, 10, 21, 12, 1, 0), rx: 0, tx: 0 },
  ]);
  expect(rates([], 30)).toEqual([]);
  expect(rates([sample(0, 5)], 30)).toEqual([]);
});

it('draws a gap, never a spike, where a counter started again or readings are far apart or out of order', () => {
  const gap = { rx: null, tx: null };
  // The interface was replaced: its counters begin at zero again.
  expect(rates([sample(0, 9_000_000), sample(30, 100)], 30)[0]).toMatchObject(gap);
  expect(rates([sample(0, 0, 9_000_000), sample(30, 100, 100)], 30)[0]).toMatchObject(gap);
  // The engine was down for five minutes: nothing is known of that time.
  expect(rates([sample(0, 0), sample(300, 100)], 30)[0]).toMatchObject(gap);
  // Two readings at once, or the clock set back.
  expect(rates([sample(30, 0), sample(30, 100)], 30)[0]).toMatchObject(gap);
  expect(rates([sample(60, 0), sample(30, 100)], 30)[0]).toMatchObject(gap);
  // A late tick is not a gap.
  expect(rates([sample(0, 0), sample(45, 4500)], 30)[0]).toMatchObject({ rx: 800 });
});

it('says the graph in numbers: now, on average, at most', () => {
  expect(summary([100, null, 300, 200])).toEqual({ now: 200, average: 200, peak: 300 });
  // The newest point is a gap: there is no rate now, but the hour still has its figures.
  expect(summary([100, 300, null])).toEqual({ now: null, average: 200, peak: 300 });
  expect(summary([null, null])).toBeNull();
  expect(summary([])).toBeNull();
});

it('names the interface that moved the most as the one to show first', () => {
  expect(busiest([
    { name: 'ens19', samples: [sample(0, 0), sample(30, 1000)] },
    { name: 'ens18', samples: [sample(0, 500, 0), sample(30, 1000, 5000)] },
    { name: 'veth0', samples: [sample(0, 9_000_000)] },
  ])).toBe('ens18');
  expect(busiest([{ name: 'only', samples: [] }])).toBe('only');
  expect(busiest([])).toBeUndefined();
});

it('works out the processors\' busy share between two readings', () => {
  expect(cpuShare({ total: 1000, idle: 800 }, { total: 2000, idle: 1550 })).toBe(0.25);
  expect(cpuShare({ total: 1000, idle: 800 }, { total: 1000, idle: 800 })).toBeNull();
  // Counters that went back, after a restart of the box between two reads.
  expect(cpuShare({ total: 2000, idle: 1000 }, { total: 100, idle: 90 })).toBeNull();
  expect(cpuShare({ total: 0, idle: 0 }, { total: 100, idle: 200 })).toBe(0);
});
