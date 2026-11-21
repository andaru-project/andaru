import { expect, it } from 'vitest';
import type { Usage } from './types';
import { cpuShare } from './usage';

const reading = (second: number, total: number, idle: number) => ({ time: new Date(Date.UTC(2026, 10, 21, 12, 0, second)).toISOString(), cpu: { total, idle } }) as Usage;

it('works out the processors\' busy share between two readings a poll or so apart', () => {
  expect(cpuShare(reading(0, 1000, 800), reading(5, 2000, 1550), 20)).toBe(0.25);
  expect(cpuShare(reading(0, 0, 0), reading(5, 100, 200), 20)).toBe(0);
  expect(cpuShare(reading(0, 0, 100), reading(5, 100, 0), 20)).toBe(1);
});

it('shows no share for a period that is not one', () => {
  // Two answers a moment apart: a handful of ticks says nothing.
  expect(cpuShare({ ...reading(0, 1000, 800), time: '2026-11-21T12:00:00.000Z' }, { ...reading(0, 1004, 800), time: '2026-11-21T12:00:00.005Z' }, 20)).toBeNull();
  // No ticks, no time, or time that went back.
  expect(cpuShare(reading(0, 1000, 800), reading(5, 1000, 800), 20)).toBeNull();
  expect(cpuShare(reading(5, 1000, 800), reading(5, 2000, 900), 20)).toBeNull();
  expect(cpuShare(reading(10, 1000, 800), reading(5, 2000, 900), 20)).toBeNull();
  // The box restarted between the two: its counters began again.
  expect(cpuShare(reading(0, 2000, 1000), reading(5, 100, 90), 20)).toBeNull();
  // The tab slept: an hour's average is not the present.
  expect(cpuShare(reading(0, 1000, 800), reading(3600, 2000, 900), 20)).toBeNull();
});
