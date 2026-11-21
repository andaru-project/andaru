import type { Usage } from './types';

// cpuShare is the busy share of the processors between two readings, from 0 to 1. It is null when the two do
// not make a period worth showing: under a second between them (a handful of ticks says nothing), too much
// time (a tab that slept for an hour would otherwise show the hour's average as the present), or counters
// that went back, as after a reboot.
export function cpuShare(before: Usage, after: Usage, longestSeconds: number) {
  const seconds = (Date.parse(after.time) - Date.parse(before.time)) / 1000;
  const total = after.cpu.total - before.cpu.total;
  if (!(seconds >= 1) || seconds > longestSeconds || !(total > 0)) return null;
  return Math.min(Math.max(1 - (after.cpu.idle - before.cpu.idle) / total, 0), 1);
}
