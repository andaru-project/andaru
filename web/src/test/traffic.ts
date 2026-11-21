import type { TrafficSample } from '../types';

// sample is one reading, second seconds into a fixed minute, with the two byte counters that make a rate.
export const sample = (second: number, rxBytes: number, txBytes = 0): TrafficSample => ({
  at: new Date(Date.UTC(2026, 10, 21, 12, 0, second)).toISOString(), rxBytes, txBytes, rxPackets: 0, txPackets: 0, rxErrors: 0, txErrors: 0, rxDropped: 0, txDropped: 0,
});
