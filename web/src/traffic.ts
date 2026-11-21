import type { TrafficSample, Usage } from './types';

// One point of the graph: the rates over the time up to at, in bits a second. A gap has no rates.
export interface RatePoint { at: number; rx: number | null; tx: number | null }

// rates works out a rate from each pair of neighbouring readings. A counter that went down has started again,
// and readings more than a few intervals apart had nothing between them: both are gaps, never spikes.
export function rates(samples: TrafficSample[], intervalSeconds: number): RatePoint[] {
  return samples.slice(1).map((sample, index) => {
    const before = samples[index];
    const at = Date.parse(sample.at);
    const seconds = (at - Date.parse(before.at)) / 1000;
    const gap = !(seconds > 0) || seconds > intervalSeconds * 3 || sample.rxBytes < before.rxBytes || sample.txBytes < before.txBytes;
    return gap ? { at, rx: null, tx: null } : { at, rx: ((sample.rxBytes - before.rxBytes) * 8) / seconds, tx: ((sample.txBytes - before.txBytes) * 8) / seconds };
  });
}

// summary says in numbers what the graph shows: the latest rate, the average and the peak. Null when there is
// no rate to speak of.
export function summary(values: Array<number | null>) {
  const known = values.filter((value): value is number => value !== null);
  if (known.length === 0) return null;
  return { now: values.at(-1) ?? null, average: known.reduce((sum, value) => sum + value, 0) / known.length, peak: Math.max(...known) };
}

// busiest names the interface that moved the most bytes over its history: the uplink, on a firewall.
export function busiest(interfaces: Array<{ name: string; samples: TrafficSample[] }>) {
  const moved = ({ samples }: { samples: TrafficSample[] }) => (samples.length < 2 ? 0 : Math.max(samples.at(-1)!.rxBytes - samples[0].rxBytes, 0) + Math.max(samples.at(-1)!.txBytes - samples[0].txBytes, 0));
  return interfaces.reduce<string | undefined>((best, entry) => (best === undefined || moved(entry) > moved(interfaces.find((other) => other.name === best)!) ? entry.name : best), undefined);
}

// cpuShare is the busy share of the processors between two readings, from 0 to 1; null when no time passed.
export function cpuShare(before: Usage['cpu'], after: Usage['cpu']) {
  const total = after.total - before.total;
  return total > 0 ? Math.min(Math.max(1 - (after.idle - before.idle) / total, 0), 1) : null;
}
