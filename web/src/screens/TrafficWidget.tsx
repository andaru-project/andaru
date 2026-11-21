import { Button, ComboboxChevron, Group, Menu, Stack, Table, Text } from '@mantine/core';
import { useCallback, useRef, useState, type CSSProperties } from 'react';
import type { ApiClient } from '../api';
import { bitrate } from '../format';
import { chosenLabel, figures, firstShown, merge, newest, rates, type RatePoint } from '../traffic';
import type { Traffic } from '../types';
import { usePolledResource } from '../useResource';
import { Loaded } from './Loaded';
import { zoneColor } from '../zones';
import { ZoneBadge } from './ZoneBadge';

const width = 600;
const height = 120;
const hourMilliseconds = 3_600_000;

// One path for a direction. It breaks where a point has no rate, so a gap is drawn as a gap, and a rate with
// no neighbour is a dot: a move with nothing after it would draw nothing.
function path(points: RatePoint[], pick: (point: RatePoint) => number | null, x: (at: number) => number, y: (rate: number) => number) {
  return points.map((point, index) => {
    const rate = pick(point);
    if (rate === null) return '';
    const starts = index === 0 || pick(points[index - 1]) === null;
    const alone = starts && (index === points.length - 1 || pick(points[index + 1]) === null);
    return `${starts ? 'M' : 'L'}${x(point.at).toFixed(1)} ${y(rate).toFixed(1)}${alone ? 'h0' : ''}`;
  }).join('');
}

// An interface's lines take its zone's colour: a shade dark enough on the light ground, and a light one in the
// dark scheme. The two are custom properties the stylesheet reads; without a zone it falls back to its own.
const lineColors = (zone: string) => ({ '--traffic-line-light': `var(--mantine-color-${zoneColor(zone)}-8)`, '--traffic-line-dark': `var(--mantine-color-${zoneColor(zone)}-4)` }) as CSSProperties;

const shown = (rate: number | null) => (rate === null ? '—' : bitrate(rate));

// A direction's row of figures: its line as drawn, its arrow and name, then the rate now, the hour's average
// and its peak. The row is the legend too.
function Figures({ arrow, name, line, values }: { arrow: string; name: string; line: string; values: ReturnType<typeof figures> }) {
  return (
    <Table.Tr>
      <Table.Th scope="row"><svg className={`traffic-key ${line}`} viewBox="0 0 24 8" aria-hidden="true"><path d="M1 4H23" /></svg> <span aria-hidden="true">{arrow}</span> {name}</Table.Th>
      <Table.Td>{shown(values.current)}</Table.Td>
      <Table.Td>{shown(values.average)}</Table.Td>
      <Table.Td>{shown(values.peak)}</Table.Td>
    </Table.Tr>
  );
}

// One interface's hour: its name and zone, the graph, and the same in figures. end is the newest reading of
// any interface, so an interface that has gone shows where it stopped.
function Graph({ entry, zone, end, intervalSeconds }: { entry: Traffic['interfaces'][number]; zone: string | undefined; end: number; intervalSeconds: number }) {
  const points = rates(entry.samples, intervalSeconds);
  const top = points.reduce((most, point) => Math.max(most, point.rx ?? 0, point.tx ?? 0), 1);
  const x = (at: number) => ((at - (end - hourMilliseconds)) / hourMilliseconds) * width;
  const y = (rate: number) => height - (rate / top) * (height - 4) - 2;
  // An interface read within the last few intervals has a rate now; one that has gone has not.
  const last = entry.samples.at(-1);
  const fresh = last !== undefined && end - Date.parse(last.at) <= intervalSeconds * 3000;
  return (
    <section aria-label={`Traffic on ${entry.name}`} style={zone ? lineColors(zone) : undefined}>
      {/* The scale and the span are labels beside the graph, not in it, where the lines would run through them. */}
      <Group gap="xs" mb={4} justify="space-between">
        <Group gap="xs"><Text ff="monospace" fw={600}>{entry.name}</Text><ZoneBadge name={zone} /></Group>
        <Text className="traffic-label" size="xs">0 – {bitrate(top)}</Text>
      </Group>
      <svg className="traffic-graph" viewBox={`0 0 ${width} ${height}`} role="img" aria-label={`${entry.name} over the last hour`} preserveAspectRatio="none">
        <path className="traffic-received" d={path(points, (point) => point.rx, x, y)} />
        <path className="traffic-sent" d={path(points, (point) => point.tx, x, y)} />
      </svg>
      <Group justify="space-between"><Text className="traffic-label" size="xs">−1 h</Text><Text className="traffic-label" size="xs">now</Text></Group>
      <Table className="traffic-figures" withRowBorders={false} verticalSpacing={2} horizontalSpacing={4}>
        <Table.Thead><Table.Tr><Table.Td /><Table.Th>Current</Table.Th><Table.Th>Avg.</Table.Th><Table.Th>Max</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>
          <Figures arrow="↓" name="Download" line="traffic-received" values={figures(points, (point) => point.rx, fresh)} />
          <Figures arrow="↑" name="Upload" line="traffic-sent" values={figures(points, (point) => point.tx, fresh)} />
        </Table.Tbody>
      </Table>
    </section>
  );
}

export function TrafficView({ traffic, zones, selected, onSelect }: { traffic: Traffic; zones: Map<string, string>; selected: string[]; onSelect(names: string[]): void }) {
  const end = newest(traffic)?.time;
  if (traffic.interfaces.length === 0 || end === undefined) return <Text c="dimmed">No readings yet.</Text>;
  // In the box's order, whatever order they were ticked in.
  const drawn = traffic.interfaces.filter((entry) => selected.includes(entry.name));
  return (
    <Stack gap="md">
      {/* With one or two interfaces all of them show and there is nothing to choose. */}
      {traffic.interfaces.length > 2 && (
        <Menu closeOnItemClick={false} position="bottom-start" withinPortal>
          <Menu.Target><Button variant="default" size="xs" justify="space-between" rightSection={<ComboboxChevron />} aria-label={`Interfaces shown: ${chosenLabel(drawn.map((entry) => entry.name))}`}>{chosenLabel(drawn.map((entry) => entry.name))}</Button></Menu.Target>
          <Menu.Dropdown>
            {traffic.interfaces.map((entry) => (
              <Menu.CheckboxItem key={entry.name} checked={selected.includes(entry.name)} onChange={(checked) => onSelect(checked ? [...selected, entry.name] : selected.filter((name) => name !== entry.name))}>
                {entry.name}{zones.has(entry.name) && ` (${zones.get(entry.name)})`}
              </Menu.CheckboxItem>
            ))}
          </Menu.Dropdown>
        </Menu>
      )}
      {drawn.map((entry) => <Graph key={entry.name} entry={entry} zone={zones.get(entry.name)} end={end} intervalSeconds={traffic.intervalSeconds} />)}
    </Stack>
  );
}

export function TrafficWidget({ api, zones, zonesSettled, onSessionLost }: { api: ApiClient; zones: Map<string, string>; zonesSettled: boolean; onSessionLost(): void }) {
  // The hour is read once; after that the box is asked only for what came after the newest reading held.
  const held = useRef<Traffic>(undefined);
  const resource = usePolledResource(useCallback(async () => {
    held.current = merge(held.current, await api.traffic(held.current && newest(held.current)?.at));
    return held.current;
  }, [api]), onSessionLost, 5_000);
  // Until the user chooses, the first choice stands as it was made: the busiest interface may change every poll.
  // It is made once the zones are known or will not be, which is when the traffic and the zones have both come.
  const [selected, setSelected] = useState<string[]>();
  const traffic = resource.data;
  if (!selected && zonesSettled && traffic && traffic.interfaces.length > 0) setSelected(firstShown(traffic, zones));
  return <Loaded resource={resource} what="traffic">{(loaded) => <TrafficView traffic={loaded} zones={zones} selected={selected ?? firstShown(loaded, zones)} onSelect={setSelected} />}</Loaded>;
}
