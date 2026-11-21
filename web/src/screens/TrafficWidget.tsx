import { Button, ComboboxChevron, Group, Menu, Stack, Table, Text, VisuallyHidden } from '@mantine/core';
import { useCallback, useRef, useState, type CSSProperties, type KeyboardEvent, type PointerEvent } from 'react';
import type { ApiClient } from '../api';
import { bitrate } from '../format';
import { chosenLabel, figures, firstShown, hourMilliseconds, merge, newest, rates, roundUp, thin, type RatePoint } from '../traffic';
import type { Traffic } from '../types';
import { usePolledResource } from '../useResource';
import { zoneColor } from '../zones';
import { Loaded } from './Loaded';
import { ZoneBadge } from './ZoneBadge';

// The drawing's own units: five wide to two high, which the stylesheet keeps as the graph's proportions.
const width = 500;
const height = 200;

// One line for a direction. It breaks where a point has no rate, so a gap is drawn as a gap.
function path(points: RatePoint[], pick: (point: RatePoint) => number | null, x: (at: number) => number, y: (rate: number) => number) {
  return points.map((point, index) => {
    const rate = pick(point);
    if (rate === null) return '';
    return `${index === 0 || pick(points[index - 1]) === null ? 'M' : 'L'}${x(point.at).toFixed(1)} ${y(rate).toFixed(1)}`;
  }).join('');
}

// The same line closed down to the axis, run by run, to fill the area under it.
function area(points: RatePoint[], pick: (point: RatePoint) => number | null, x: (at: number) => number, y: (rate: number) => number) {
  let closed = '';
  let from: number | undefined;
  points.forEach((point, index) => {
    const rate = pick(point);
    if (rate === null) return;
    if (from === undefined) {
      from = x(point.at);
      closed += `M${from.toFixed(1)} ${height}`;
    }
    closed += `L${x(point.at).toFixed(1)} ${y(rate).toFixed(1)}`;
    if (index === points.length - 1 || pick(points[index + 1]) === null) {
      closed += `L${x(point.at).toFixed(1)} ${height}Z`;
      from = undefined;
    }
  });
  return closed;
}

const shown = (rate: number | null) => (rate === null ? '—' : bitrate(rate));
const clock = (at: number) => new Date(at).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });

// An interface's lines take its zone's colour: a shade dark enough on the light ground, and a light one in the
// dark scheme. The two are custom properties the stylesheet reads; without a zone it falls back to its own.
const lineColors = (zone: string) => ({ '--traffic-line-light': `var(--mantine-color-${zoneColor(zone)}-9)`, '--traffic-line-dark': `var(--mantine-color-${zoneColor(zone)}-4)` }) as CSSProperties;

// A direction's row of figures: its line as drawn, its arrow and name, then the rate now, the hour's average
// and its peak. The row is the legend too.
// The words are short for the eye. For the ear they are whole: "down" and "up" are also what a link is.
function Figures({ arrow, name, heard, line, values }: { arrow: string; name: string; heard: string; line: string; values: ReturnType<typeof figures> }) {
  return (
    <Table.Tr>
      <Table.Th scope="row"><svg className="traffic-key" viewBox="0 0 24 8" aria-hidden="true"><path className={line} d="M1 4H23" /></svg> <span aria-hidden="true">{arrow} {name}</span><VisuallyHidden>{heard}</VisuallyHidden></Table.Th>
      <Table.Td>{shown(values.current)}</Table.Td>
      <Table.Td>{shown(values.average)}</Table.Td>
      <Table.Td>{shown(values.peak)}</Table.Td>
    </Table.Tr>
  );
}

// One interface's hour: its name and zone, the graph, and the same in figures. end is the newest reading of
// any interface, so an interface that has gone shows where it stopped. live is false while the box does not
// answer: what is held is then the past, and has no rate now.
function Graph({ entry, zone, end, intervalSeconds, live }: { entry: Traffic['interfaces'][number]; zone: string | undefined; end: number; intervalSeconds: number; live: boolean }) {
  // every is each reading's rate, for the figures; points is what is drawn and marked, thinned to a line.
  const every = rates(entry.samples, intervalSeconds);
  const points = thin(every, 180, intervalSeconds);
  // The scale is from every reading, so it is never below the Max in the figures, and round, so it stays put.
  const top = roundUp(every.reduce((most, point) => Math.max(most, point.rx ?? 0, point.tx ?? 0), 0));
  const x = (at: number) => ((at - (end - hourMilliseconds)) / hourMilliseconds) * width;
  const y = (rate: number) => height - (rate / top) * (height - 4) - 2;
  // An interface read within the last few intervals has a rate now; one that has gone has not.
  const last = entry.samples.at(-1);
  const fresh = live && last !== undefined && end - Date.parse(last.at) <= intervalSeconds * 3000;

  // The point under the pointer, a finger or the arrow keys: its time and rates are read out beside the graph.
  // A mouse or a pen lets go on leaving. A finger leaves as it lifts, so a tap or a slide along the graph keeps
  // its mark; a touch the browser takes over to scroll the page is cancelled, and leaves no mark behind.
  const [marked, setMarked] = useState<number>();
  const mark = marked === undefined ? undefined : points[Math.min(marked, points.length - 1)];
  const nearest = (event: PointerEvent<HTMLDivElement>) => {
    const box = event.currentTarget.getBoundingClientRect();
    const at = end - hourMilliseconds + ((event.clientX - box.left) / box.width) * hourMilliseconds;
    let best = 0;
    points.forEach((point, index) => { if (Math.abs(point.at - at) < Math.abs(points[best].at - at)) best = index; });
    if (points.length > 0) setMarked(best);
  };
  const step = (event: KeyboardEvent<HTMLDivElement>) => {
    const by = { ArrowLeft: -1, ArrowRight: 1 }[event.key];
    if (event.key === 'Escape') setMarked(undefined);
    if (by === undefined || points.length === 0) return;
    event.preventDefault();
    setMarked(Math.min(Math.max((marked ?? points.length - 1) + by, 0), points.length - 1));
  };
  const place = (rate: number): CSSProperties => ({ left: `${(x(mark!.at) / width) * 100}%`, top: `${(y(rate) / height) * 100}%` });

  return (
    <section aria-label={`Traffic on ${entry.name}`} style={zone ? lineColors(zone) : undefined}>
      {/* The scale and the span are labels beside the graph, not in it, where the lines would run through them. */}
      <Group gap="xs" mb={4} justify="space-between">
        <Group gap="xs"><Text ff="monospace" fw={600}>{entry.name}</Text><ZoneBadge name={zone} /></Group>
        <Text c="dimmed" size="xs">0 – {bitrate(top)}</Text>
      </Group>
      <div className="traffic-frame" tabIndex={0} role="group" aria-label={`${entry.name} over the last hour: the arrow keys read out a point`} onPointerMove={nearest} onPointerDown={nearest} onPointerCancel={() => setMarked(undefined)} onPointerLeave={(event) => { if (event.pointerType !== 'touch') setMarked(undefined); }} onKeyDown={step} onBlur={() => setMarked(undefined)}>
        <svg className="traffic-graph" viewBox={`0 0 ${width} ${height}`} aria-hidden="true" preserveAspectRatio="none">
          <line className="traffic-mid" x1="0" y1={height / 2} x2={width} y2={height / 2} />
          <path className="traffic-area traffic-sent" d={area(points, (point) => point.tx, x, y)} />
          <path className="traffic-area traffic-received" d={area(points, (point) => point.rx, x, y)} />
          <path className="traffic-sent" d={path(points, (point) => point.tx, x, y)} />
          <path className="traffic-received" d={path(points, (point) => point.rx, x, y)} />
        </svg>
        {mark && <span className="traffic-guide" style={{ left: `${(x(mark.at) / width) * 100}%` }} />}
        {mark && mark.tx !== null && <span className="traffic-dot traffic-sent" style={place(mark.tx)} />}
        {mark && mark.rx !== null && <span className="traffic-dot traffic-received" style={place(mark.rx)} />}
      </div>
      {/* One line under the graph: the span while nothing is marked, the marked point's time and rates when one is. */}
      <Group justify="space-between" wrap="nowrap" aria-live="polite">
        {mark
          ? <Text size="xs">{clock(mark.at)} · <span aria-hidden="true">↓</span> {shown(mark.rx)} · <span aria-hidden="true">↑</span> {shown(mark.tx)}</Text>
          : <><Text c="dimmed" size="xs">−1 h</Text><Text c="dimmed" size="xs">now</Text></>}
      </Group>
      <Table className="traffic-figures" withRowBorders={false} verticalSpacing={2} horizontalSpacing={4}>
        <Table.Thead><Table.Tr><Table.Td /><Table.Th>Current</Table.Th><Table.Th>Avg</Table.Th><Table.Th>Max</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>
          <Figures arrow="↓" name="Down" heard="Download" line="traffic-received" values={figures(every, (point) => point.rx, fresh)} />
          <Figures arrow="↑" name="Up" heard="Upload" line="traffic-sent" values={figures(every, (point) => point.tx, fresh)} />
        </Table.Tbody>
      </Table>
    </section>
  );
}

export function TrafficView({ traffic, zones, selected, live, onSelect }: { traffic: Traffic; zones: Map<string, string>; selected: string[]; live: boolean; onSelect(names: string[]): void }) {
  const end = newest(traffic)?.time;
  if (traffic.interfaces.length === 0 || end === undefined) return <Text c="dimmed">No readings yet.</Text>;
  // With one or two interfaces all of them show and there is nothing to choose. With more, the chosen ones
  // show, in the box's order whatever order they were ticked in.
  const choosing = traffic.interfaces.length > 2;
  const drawn = choosing ? traffic.interfaces.filter((entry) => selected.includes(entry.name)) : traffic.interfaces;
  const label = chosenLabel(drawn.map((entry) => entry.name));
  return (
    <Stack gap="md">
      {choosing && (
        <Menu closeOnItemClick={false} position="bottom-start" withinPortal>
          <Menu.Target><Button variant="default" size="xs" justify="space-between" rightSection={<ComboboxChevron />} aria-label={`Interfaces shown: ${label}`}>{label}</Button></Menu.Target>
          <Menu.Dropdown>
            {traffic.interfaces.map((entry) => (
              <Menu.CheckboxItem key={entry.name} checked={selected.includes(entry.name)} onChange={(checked) => onSelect(checked ? [...selected, entry.name] : selected.filter((name) => name !== entry.name))}>
                {entry.name}{zones.has(entry.name) && ` (${zones.get(entry.name)})`}
              </Menu.CheckboxItem>
            ))}
          </Menu.Dropdown>
        </Menu>
      )}
      {drawn.map((entry) => <Graph key={entry.name} entry={entry} zone={zones.get(entry.name)} end={end} intervalSeconds={traffic.intervalSeconds} live={live} />)}
    </Stack>
  );
}

export function TrafficWidget({ api, zones, zonesKnown, onSessionLost }: { api: ApiClient; zones: Map<string, string>; zonesKnown: boolean; onSessionLost(): void }) {
  // The hour is read once; after that the box is asked only for what came after the newest reading held.
  const held = useRef<Traffic>(undefined);
  const resource = usePolledResource(useCallback(async () => {
    const answer = await api.traffic(held.current && newest(held.current)?.at);
    held.current = merge(held.current, answer);
    return held.current;
  }, [api]), onSessionLost, 5_000);
  // What shows is the user's choice once made. Before that it is the first choice, fixed as soon as there are
  // more than two interfaces to choose among: the busiest interface may change at every poll, and a graph must
  // not change under the eye by itself. A first choice made before the zones were known is made once more when
  // they are, and then stands.
  const [selected, setSelected] = useState<string[]>();
  const [first, setFirst] = useState<{ names: string[]; zoned: boolean }>();
  const traffic = resource.data;
  if (traffic && traffic.interfaces.length > 2 && (!first || (!first.zoned && zonesKnown))) setFirst({ names: firstShown(traffic, zones), zoned: zonesKnown });
  return <Loaded resource={resource} what="traffic">{(loaded) => <TrafficView traffic={loaded} zones={zones} selected={selected ?? first?.names ?? firstShown(loaded, zones)} live={resource.error === ''} onSelect={setSelected} />}</Loaded>;
}
