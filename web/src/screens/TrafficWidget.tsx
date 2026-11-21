import { Group, NativeSelect, Stack, Text } from '@mantine/core';
import { useCallback, useState } from 'react';
import type { ApiClient } from '../api';
import { bitrate } from '../format';
import { busiest, rates, summary, type RatePoint } from '../traffic';
import type { Traffic } from '../types';
import { usePolledResource } from '../useResource';
import { Loaded } from './Loaded';
import { ZoneBadge } from './ZoneBadge';

const width = 600;
const height = 160;
const hourMilliseconds = 3_600_000;

// A line breaks where a point has no rate: a gap is drawn as a gap.
function segments(points: RatePoint[], pick: (point: RatePoint) => number | null, x: (at: number) => number, y: (rate: number) => number) {
  const lines: string[] = [];
  let current: string[] = [];
  for (const point of points) {
    const rate = pick(point);
    if (rate === null) {
      if (current.length > 0) lines.push(current.join(' '));
      current = [];
    } else {
      current.push(`${x(point.at).toFixed(1)},${y(rate).toFixed(1)}`);
    }
  }
  if (current.length > 0) lines.push(current.join(' '));
  return lines;
}

const sentence = (direction: string, values: Array<number | null>) => {
  const figures = summary(values);
  return figures ? `${direction} ${figures.now === null ? 'no reading now' : `${bitrate(figures.now)} now`}, ${bitrate(figures.average)} on average, ${bitrate(figures.peak)} at most.` : `${direction} no readings yet.`;
};

export function TrafficView({ traffic, zones, chosen, onChoose }: { traffic: Traffic; zones: Map<string, string>; chosen: string | undefined; onChoose(name: string): void }) {
  if (traffic.interfaces.length === 0) return <Text c="dimmed">No readings yet: the first comes within half a minute of the engine's start.</Text>;
  const name = traffic.interfaces.some((entry) => entry.name === chosen) ? chosen! : busiest(traffic.interfaces)!;
  const points = rates(traffic.interfaces.find((entry) => entry.name === name)!.samples, traffic.intervalSeconds);
  // The hour up to the newest reading of any interface, so an interface that has gone shows where it stopped.
  const end = Math.max(...traffic.interfaces.flatMap((entry) => entry.samples.map((sample) => Date.parse(sample.at))));
  const top = Math.max(...points.flatMap((point) => [point.rx ?? 0, point.tx ?? 0]), 1);
  const x = (at: number) => ((at - (end - hourMilliseconds)) / hourMilliseconds) * width;
  const y = (rate: number) => height - (rate / top) * (height - 4) - 2;
  const received = points.map((point) => point.rx), sent = points.map((point) => point.tx);

  return (
    <Stack gap="xs">
      <Group gap="xs" align="flex-end">
        <NativeSelect label="Interface" size="xs" value={name} onChange={(event) => onChoose(event.currentTarget.value)} data={traffic.interfaces.map((entry) => ({ value: entry.name, label: zones.has(entry.name) ? `${entry.name} (${zones.get(entry.name)})` : entry.name }))} />
        <ZoneBadge name={zones.get(name)} />
      </Group>
      {/* The picture is for the eye; the sentences beneath say the same in numbers. */}
      <svg className="traffic-graph" viewBox={`0 0 ${width} ${height}`} role="img" aria-label={`Traffic on ${name} over the last hour, up to ${bitrate(top)}`} preserveAspectRatio="none">
        <line className="traffic-axis" x1="0" y1={height - 1} x2={width} y2={height - 1} />
        {segments(points, (point) => point.rx, x, y).map((line) => <polyline key={`rx ${line}`} className="traffic-received" points={line} />)}
        {segments(points, (point) => point.tx, x, y).map((line) => <polyline key={`tx ${line}`} className="traffic-sent" points={line} />)}
      </svg>
      <Text size="xs" c="dimmed">One hour up to now; the top of the graph is {bitrate(top)}. Solid line: received. Dashed line: sent.</Text>
      <Text size="sm">{sentence('Received:', received)}</Text>
      <Text size="sm">{sentence('Sent:', sent)}</Text>
    </Stack>
  );
}

export function TrafficWidget({ api, zones, onSessionLost }: { api: ApiClient; zones: Map<string, string>; onSessionLost(): void }) {
  const resource = usePolledResource(useCallback(() => api.traffic(), [api]), onSessionLost, 30_000);
  const [chosen, setChosen] = useState<string>();
  return <Loaded resource={resource} what="traffic">{(traffic) => <TrafficView traffic={traffic} zones={zones} chosen={chosen} onChoose={setChosen} />}</Loaded>;
}
