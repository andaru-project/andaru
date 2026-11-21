import type { Traffic, TrafficSample } from './types';

export const hourMilliseconds = 3_600_000;

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

// thin draws fewer points than were read: each run of neighbours becomes one point at their average, so an hour
// of 5-second readings is a line and not a blur in a graph a few hundred pixels wide. The figures beside the
// graph are worked out from every reading, so no peak is lost to the averaging. A run that is all gap stays a gap.
export function thin(points: RatePoint[], most: number): RatePoint[] {
  const run = Math.ceil(points.length / most);
  if (run <= 1) return points;
  const mean = (values: Array<number | null>) => {
    const known = values.filter((value): value is number => value !== null);
    return known.length === 0 ? null : known.reduce((sum, value) => sum + value, 0) / known.length;
  };
  const thinned: RatePoint[] = [];
  // Runs are counted from the newest point back, so the newest run is whole and the line ends at the present.
  for (let end = points.length; end > 0; end -= run) {
    const part = points.slice(Math.max(end - run, 0), end);
    thinned.unshift({ at: part[part.length - 1].at, rx: mean(part.map((point) => point.rx)), tx: mean(part.map((point) => point.tx)) });
  }
  return thinned;
}

// figures are the numbers beside the graph. current is the latest rate, and null when the interface has not
// been read lately: an interface that has gone still has an hour's average and peak, but no rate now.
export function figures(points: RatePoint[], pick: (point: RatePoint) => number | null, fresh: boolean) {
  const known = points.map(pick).filter((value): value is number => value !== null);
  if (known.length === 0) return { current: null, average: null, peak: null };
  return { current: fresh ? pick(points.at(-1)!) : null, average: known.reduce((sum, value) => sum + value, 0) / known.length, peak: Math.max(...known) };
}

// newest is the time of the latest reading of any interface, as the box wrote it, and its value; undefined
// when there is none. Its text is what a client sends back as since.
export function newest(traffic: Traffic) {
  let best: { at: string; time: number } | undefined;
  for (const { samples } of traffic.interfaces) {
    const last = samples.at(-1);
    const time = last ? Date.parse(last.at) : NaN;
    if (last && Number.isFinite(time) && (!best || time > best.time)) best = { at: last.at, time };
  }
  return best;
}

// merge adds an answer's readings to what is held, and keeps the hour. An answer marked complete is the whole
// history and replaces it. An interface the box no longer names is gone from the history too. A reading no
// newer than the newest one held for its interface is not added: two answers to one question would otherwise
// put the same readings in twice, and readings out of order draw as gaps for an hour.
export function merge(held: Traffic | undefined, answer: Traffic): Traffic {
  if (!held || answer.complete) return answer;
  const interfaces = answer.interfaces.map((entry) => {
    const before = held.interfaces.find((other) => other.name === entry.name)?.samples ?? [];
    const lastAt = before.length > 0 ? Date.parse(before[before.length - 1].at) : -Infinity;
    return { name: entry.name, samples: [...before, ...entry.samples.filter((sample) => Date.parse(sample.at) > lastAt)] };
  });
  const merged = { ...answer, complete: true, interfaces };
  const end = newest(merged)?.time;
  if (end === undefined) return merged;
  return { ...merged, interfaces: interfaces.map((entry) => ({ name: entry.name, samples: entry.samples.filter((sample) => Date.parse(sample.at) > end - hourMilliseconds) })) };
}

// busiest names the interface that moved the most bytes over its history; the first wins a tie.
export function busiest(interfaces: Traffic['interfaces']) {
  const moved = ({ samples }: { samples: TrafficSample[] }) => {
    const first = samples[0], last = samples.at(-1);
    return first && last ? Math.max(last.rxBytes - first.rxBytes, 0) + Math.max(last.txBytes - first.txBytes, 0) : 0;
  };
  return interfaces.reduce<Traffic['interfaces'][number] | undefined>((best, entry) => (best && moved(best) >= moved(entry) ? best : entry), undefined)?.name;
}

// firstShown is what shows before the user chooses: every interface when there are one or two; else those in
// the zones named "wan" and "lan"; else the busiest one.
export function firstShown(traffic: Traffic, zones: Map<string, string>) {
  const names = traffic.interfaces.map((entry) => entry.name);
  if (names.length <= 2) return names;
  const usual = names.filter((name) => ['wan', 'lan'].includes(zones.get(name)?.toLowerCase() ?? ''));
  return usual.length > 0 ? usual : [busiest(traffic.interfaces)!];
}

// chosenLabel is the names when they fit the button, and their number when they do not.
const longestLabel = 24;
export const chosenLabel = (names: string[]) => (names.length === 0 ? 'None selected' : names.join(', ').length <= longestLabel ? names.join(', ') : `${names.length} selected`);
