import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { act, fireEvent, render, screen, waitFor, within } from '../test/render';
import { sample } from '../test/traffic';
import type { Traffic } from '../types';
import { TrafficView, TrafficWidget } from './TrafficWidget';

// 5 seconds apart, 625,000 bytes is one megabit a second.
const megabit = 625_000;
// ens18 downloaded 1, 2, then 4 Mbps and uploaded a steady 1 Mbps; ens19 hardly moved.
const pair: Traffic = {
  intervalSeconds: 5, complete: true,
  interfaces: [
    { name: 'ens18', samples: [sample(0, 0, 0), sample(5, megabit, megabit), sample(10, 3 * megabit, 2 * megabit), sample(15, 7 * megabit, 3 * megabit)] },
    { name: 'ens19', samples: [sample(10, 0, 0), sample(15, 625, 0)] },
  ],
};
const many: Traffic = { ...pair, interfaces: [...pair.interfaces, { name: 'ens20', samples: [sample(10, 0, 0), sample(15, 5 * megabit, 0)] }, { name: 'veth0', samples: [sample(15, 5, 5)] }, { name: 'ppp0', samples: [sample(-1800, 0, 0), sample(-1795, 2 * megabit, 0)] }] };
const zones = new Map([['ens18', 'wan'], ['ens19', 'lan'], ['ens20', 'guest']]);

const graph = (name: string) => screen.getByRole('region', { name: `Traffic on ${name}` });
// The figures of one direction on one interface's graph: Current, Avg, Max.
const row = (name: string, direction: string) => within(within(graph(name)).getByRole('rowheader', { name: direction }).closest('tr')!).getAllByRole('cell').map((cell) => cell.textContent);
const graphs = () => screen.getAllByRole('region').map((region) => region.getAttribute('aria-label'));
const drawn = (name: string, line: string) => graph(name).querySelector(`path.${line}:not(.traffic-area)`)!.getAttribute('d')!;
const filled = (name: string, line: string) => graph(name).querySelector(`path.traffic-area.${line}`)!.getAttribute('d')!;

afterEach(() => vi.useRealTimers());

it('shows a graph for each interface, top to bottom, with its zone and its figures in a table', () => {
  render(<TrafficView traffic={pair} zones={zones} selected={['ens19', 'ens18']} live onSelect={vi.fn()} />);
  // In the box's order, whatever order they were chosen in; with two interfaces there is nothing to choose.
  expect(graphs()).toEqual(['Traffic on ens18', 'Traffic on ens19']);
  expect(screen.queryByRole('button', { name: /Interfaces shown/ })).not.toBeInTheDocument();
  expect(within(graph('ens18')).getByText('zone wan')).toBeInTheDocument();
  expect(within(graph('ens19')).getByText('zone lan')).toBeInTheDocument();

  // The arrows are for the eye; the names are what is read out.
  expect(within(graph('ens18')).getByRole('rowheader', { name: 'Download' })).toHaveTextContent('↓ Down');
  expect(within(graph('ens18')).getByRole('rowheader', { name: 'Upload' })).toHaveTextContent('↑ Up');
  expect(within(graph('ens18')).getAllByRole('columnheader').map((header) => header.textContent)).toEqual(['Current', 'Avg', 'Max']);
  expect(row('ens18', 'Download')).toEqual(['4.0 Mbps', '2.3 Mbps', '4.0 Mbps']);
  expect(row('ens18', 'Upload')).toEqual(['1.0 Mbps', '1.0 Mbps', '1.0 Mbps']);
  expect(row('ens19', 'Download')).toEqual(['1.0 kbps', '1.0 kbps', '1.0 kbps']);
  // The scale and the span are labels on the graph, not a sentence.
  expect(within(graph('ens18')).getByText('0 – 4.2 Mbps')).toBeInTheDocument();
  expect(within(graph('ens18')).getByText('−1 h')).toBeInTheDocument();
  expect(within(graph('ens18')).getByText('now')).toBeInTheDocument();
});

it('draws the rates where they belong: later to the right, faster higher, download and upload each as its own line', () => {
  render(<TrafficView traffic={many} zones={zones} selected={['ens18']} live onSelect={vi.fn()} />);
  const points = (line: string) => [...drawn('ens18', line).matchAll(/[ML]([\d.]+) ([\d.]+)/g)].map((match) => [Number(match[1]), Number(match[2])]);
  const download = points('traffic-received'), upload = points('traffic-sent');
  expect(download).toHaveLength(3);
  // The newest reading is at the right edge, and each point is right of the one before.
  expect(download[2][0]).toBe(500);
  expect(download[0][0]).toBeLessThan(download[1][0]);
  // Download rises almost to the top: the scale is the highest point drawn and a twentieth more. Upload stays level, lower.
  expect(download[0][1]).toBeGreaterThan(download[1][1]);
  expect(download[2][1]).toBeCloseTo(11.3, 1);
  expect(new Set(upload.map(([, y]) => y)).size).toBe(1);
  expect(upload[0][1]).toBeGreaterThan(download[2][1]);
});

it('draws a gap as a gap: the line breaks where a counter started again', () => {
  const restarted: Traffic = { ...pair, interfaces: [{ name: 'ens18', samples: [sample(0, 0), sample(5, megabit), sample(10, 2 * megabit), sample(15, 100), sample(20, megabit), sample(25, 2 * megabit)] }] };
  render(<TrafficView traffic={restarted} zones={zones} selected={['ens18']} live onSelect={vi.fn()} />);
  // Two rates, a gap where the counter began again, two more.
  expect(drawn('ens18', 'traffic-received').match(/[ML]/g)!.join('')).toBe('MLML');
  // The area under the line is filled run by run: each starts and ends on the axis, and none spans the gap.
  expect(filled('ens18', 'traffic-received').match(/[MLZ]/g)!.join('')).toBe('MLLLZMLLLZ');
  expect(filled('ens18', 'traffic-received')).toMatch(/^M[\d.]+ 200L/);
});

it('reads out the point under the pointer, a finger or the arrow keys: its time and both rates', async () => {
  render(<TrafficView traffic={pair} zones={zones} selected={['ens18']} live onSelect={vi.fn()} />);
  const frame = within(graph('ens18')).getByRole('group');
  const readout = () => graph('ens18').querySelector('[aria-live]')!.textContent;
  expect(readout()).toBe('−1 hnow');
  expect(graph('ens18').querySelector('.traffic-dot')).not.toBeInTheDocument();

  // The keys start from the newest point and step back and forth; Escape lets go.
  frame.focus();
  await userEvent.keyboard('{ArrowLeft}');
  expect(readout()).toMatch(/^\d\d:\d\d:\d\d · ↓ 2\.0 Mbps · ↑ 1\.0 Mbps$/);
  expect(graph('ens18').querySelectorAll('.traffic-dot')).toHaveLength(2);
  await userEvent.keyboard('{ArrowRight}{ArrowRight}{ArrowRight}');
  expect(readout()).toMatch(/↓ 4\.0 Mbps/);
  await userEvent.keyboard('{ArrowLeft}{ArrowLeft}{ArrowLeft}{ArrowLeft}');
  expect(readout()).toMatch(/↓ 1\.0 Mbps/);
  await userEvent.keyboard('{Escape}');
  expect(readout()).toBe('−1 hnow');

  // A pointer at the right edge is on the newest point; leaving lets go.
  frame.getBoundingClientRect = () => DOMRect.fromRect({ x: 0, y: 0, width: 500, height: 200 });
  fireEvent.pointerMove(frame, { clientX: 500, pointerType: 'mouse' });
  expect(readout()).toMatch(/↓ 4\.0 Mbps/);
  // The hour is 500 wide here: 7 seconds left of the edge is nearer the reading before.
  fireEvent.pointerMove(frame, { clientX: 499, pointerType: 'mouse' });
  expect(readout()).toMatch(/↓ 2\.0 Mbps/);
  fireEvent.pointerLeave(frame, { pointerType: 'mouse' });
  expect(readout()).toBe('−1 hnow');
  // A finger leaves as it lifts: its mark stays. A touch that becomes a scroll is cancelled: its mark goes.
  fireEvent.pointerDown(frame, { clientX: 500, pointerType: 'touch' });
  fireEvent.pointerLeave(frame, { pointerType: 'touch' });
  expect(readout()).toMatch(/↓ 4\.0 Mbps/);
  fireEvent.pointerDown(frame, { clientX: 499, pointerType: 'touch' });
  fireEvent.pointerCancel(frame, { pointerType: 'touch' });
  expect(readout()).toBe('−1 hnow');
});

it('shows every interface when there are one or two, whatever was chosen before', () => {
  // A third interface came and went, or the choice was made when there were more: nothing is hidden with no way to show it.
  render(<TrafficView traffic={pair} zones={zones} selected={['ens18']} live onSelect={vi.fn()} />);
  expect(graphs()).toEqual(['Traffic on ens18', 'Traffic on ens19']);
});

it('shows no rate as current while the box does not answer', () => {
  render(<TrafficView traffic={pair} zones={zones} selected={['ens18']} live={false} onSelect={vi.fn()} />);
  expect(row('ens18', 'Download')).toEqual(['—', '2.3 Mbps', '4.0 Mbps']);
});

it('gives an interface that has gone its hour, but no rate now', () => {
  render(<TrafficView traffic={many} zones={zones} selected={['ppp0', 'veth0']} live onSelect={vi.fn()} />);
  expect(row('ppp0', 'Download')).toEqual(['—', '2.0 Mbps', '2.0 Mbps']);
  // One reading makes no rate at all.
  expect(row('veth0', 'Download')).toEqual(['—', '—', '—']);
});

it('lets the user tick which interfaces show when there are more than two', async () => {
  const onSelect = vi.fn();
  const view = render(<TrafficView traffic={many} zones={zones} selected={['ens18', 'ens19']} live onSelect={onSelect} />);
  const button = screen.getByRole('button', { name: 'Interfaces shown: ens18, ens19' });
  expect(button).toHaveTextContent('ens18, ens19');
  await userEvent.click(button);
  const items = await screen.findAllByRole('menuitemcheckbox');
  expect(items.map((item) => [item.textContent, item.getAttribute('aria-checked')])).toEqual([['ens18 (wan)', 'true'], ['ens19 (lan)', 'true'], ['ens20 (guest)', 'false'], ['veth0', 'false'], ['ppp0', 'false']]);
  await userEvent.click(items[2]);
  expect(onSelect).toHaveBeenLastCalledWith(['ens18', 'ens19', 'ens20']);
  await userEvent.click(items[0]);
  expect(onSelect).toHaveBeenLastCalledWith(['ens19']);

  // The names while they fit, their number when they do not.
  view.rerender(<TrafficView traffic={many} zones={zones} selected={['ens18', 'ens19', 'ens20', 'veth0', 'ppp0']} live onSelect={onSelect} />);
  expect(screen.getByRole('button', { name: 'Interfaces shown: 5 selected' })).toHaveTextContent('5 selected');
  expect(screen.getAllByRole('region')).toHaveLength(5);
});

it('says so when the engine has read nothing yet', () => {
  render(<TrafficView traffic={{ intervalSeconds: 5, complete: true, interfaces: [] }} zones={zones} selected={[]} live onSelect={vi.fn()} />);
  expect(screen.getByText('No readings yet.')).toBeInTheDocument();
});

it('gives an interface\'s lines its zone\'s colour, and its own to one in no zone', () => {
  render(<TrafficView traffic={many} zones={zones} selected={['ens18', 'veth0']} live onSelect={vi.fn()} />);
  expect(graph('ens18').style.getPropertyValue('--traffic-line-light')).toBe('var(--mantine-color-red-9)');
  expect(graph('ens18').style.getPropertyValue('--traffic-line-dark')).toBe('var(--mantine-color-red-4)');
  expect(graph('veth0').style.getPropertyValue('--traffic-line-light')).toBe('');
});

it('reads the hour once, then every 5 seconds only what came after the newest reading it holds', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  const tail: Traffic = { intervalSeconds: 5, complete: false, interfaces: [{ name: 'ens18', samples: [sample(20, 12 * megabit, 4 * megabit)] }, { name: 'ens19', samples: [] }] };
  const fetchMock = fetchRoutes({ '/api/v1/status/traffic?since=': () => jsonResponse(200, tail), '/api/v1/status/traffic': () => jsonResponse(200, pair) });
  render(<TrafficWidget api={new ApiClient('csrf')} zones={zones} zonesKnown onSessionLost={vi.fn()} />);
  await waitFor(() => expect(row('ens18', 'Download')[0]).toBe('4.0 Mbps'));
  expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(['/api/v1/status/traffic']);
  act(() => { vi.advanceTimersByTime(5_000); });
  // 5 megabits more in 5 seconds; the average and the peak now count four rates.
  await waitFor(() => expect(row('ens18', 'Download')).toEqual(['5.0 Mbps', '3.0 Mbps', '5.0 Mbps']));
  expect(fetchMock.mock.calls.map(([url]) => url)[1]).toBe(`/api/v1/status/traffic?since=${encodeURIComponent(sample(15, 0).at)}`);
  // ens19 had nothing new: it keeps what it had.
  expect(row('ens19', 'Download')[1]).toBe('1.0 kbps');
});

it('makes its first choice once the zones are known, wan and lan, and keeps it until the user chooses', async () => {
  fetchRoutes({ '/api/v1/status/traffic': many });
  const view = render(<TrafficWidget api={new ApiClient('csrf')} zones={new Map()} zonesKnown={false} onSessionLost={vi.fn()} />);
  // Before the zones are in, the busiest shows; that choice is made once more when they come.
  await waitFor(() => expect(graphs()).toEqual(['Traffic on ens18']));
  view.rerender(<TrafficWidget api={new ApiClient('csrf')} zones={zones} zonesKnown onSessionLost={vi.fn()} />);
  await waitFor(() => expect(graphs()).toEqual(['Traffic on ens18', 'Traffic on ens19']));
  // The zones changing later does not move the graphs under the eye.
  view.rerender(<TrafficWidget api={new ApiClient('csrf')} zones={new Map([['ens20', 'wan']])} zonesKnown onSessionLost={vi.fn()} />);
  expect(graphs()).toEqual(['Traffic on ens18', 'Traffic on ens19']);
  await userEvent.click(screen.getByRole('button', { name: /Interfaces shown/ }));
  await userEvent.click((await screen.findAllByRole('menuitemcheckbox'))[1]);
  await waitFor(() => expect(graphs()).toEqual(['Traffic on ens18']));
  expect(screen.getByRole('button', { name: 'Interfaces shown: ens18' })).toBeInTheDocument();
});
