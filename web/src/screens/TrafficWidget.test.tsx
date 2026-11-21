import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { act, render, screen, waitFor, within } from '../test/render';
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
// The figures of one direction on one interface's graph: Current, Avg., Max.
const row = (name: string, direction: string) => within(within(graph(name)).getByRole('rowheader', { name: direction }).closest('tr')!).getAllByRole('cell').map((cell) => cell.textContent);
const drawn = (name: string, line: string) => graph(name).querySelector(`path.${line}`)!.getAttribute('d')!;

afterEach(() => vi.useRealTimers());

it('shows a graph for each interface, top to bottom, with its zone and its figures in a table', () => {
  render(<TrafficView traffic={pair} zones={zones} selected={['ens19', 'ens18']} onSelect={vi.fn()} />);
  // In the box's order, whatever order they were chosen in; with two interfaces there is nothing to choose.
  expect(screen.getAllByRole('region').map((region) => region.getAttribute('aria-label'))).toEqual(['Traffic on ens18', 'Traffic on ens19']);
  expect(screen.queryByRole('button', { name: /Interfaces shown/ })).not.toBeInTheDocument();
  expect(within(graph('ens18')).getByLabelText('zone wan')).toBeInTheDocument();
  expect(within(graph('ens19')).getByLabelText('zone lan')).toBeInTheDocument();

  // The arrows are for the eye; the names are what is read out.
  expect(within(graph('ens18')).getByRole('rowheader', { name: 'Download' })).toHaveTextContent('↓ Download');
  expect(within(graph('ens18')).getByRole('rowheader', { name: 'Upload' })).toHaveTextContent('↑ Upload');
  expect(within(graph('ens18')).getAllByRole('columnheader').map((header) => header.textContent)).toEqual(['Current', 'Avg.', 'Max']);
  expect(row('ens18', 'Download')).toEqual(['4.0 Mbps', '2.3 Mbps', '4.0 Mbps']);
  expect(row('ens18', 'Upload')).toEqual(['1.0 Mbps', '1.0 Mbps', '1.0 Mbps']);
  expect(row('ens19', 'Download')).toEqual(['1.0 kbps', '1.0 kbps', '1.0 kbps']);
  // The scale and the span are labels on the graph, not a sentence.
  expect(within(graph('ens18')).getByText('0 – 4.0 Mbps')).toBeInTheDocument();
  expect(within(graph('ens18')).getByText('−1 h')).toBeInTheDocument();
  expect(within(graph('ens18')).getByText('now')).toBeInTheDocument();
});

it('draws the rates where they belong: later to the right, faster higher, download and upload each as its own line', () => {
  render(<TrafficView traffic={pair} zones={zones} selected={['ens18']} onSelect={vi.fn()} />);
  const points = (line: string) => [...drawn('ens18', line).matchAll(/[ML]([\d.]+) ([\d.]+)/g)].map((match) => [Number(match[1]), Number(match[2])]);
  const download = points('traffic-received'), upload = points('traffic-sent');
  expect(download).toHaveLength(3);
  // The newest reading is at the right edge, and each point is right of the one before.
  expect(download[2][0]).toBe(600);
  expect(download[0][0]).toBeLessThan(download[1][0]);
  // Download rises to the top of the graph; upload stays level, lower than the peak.
  expect(download[0][1]).toBeGreaterThan(download[1][1]);
  expect(download[2][1]).toBe(2);
  expect(new Set(upload.map(([, y]) => y)).size).toBe(1);
  expect(upload[0][1]).toBeGreaterThan(download[2][1]);
});

it('draws a gap as a gap, and a rate with no neighbour as a dot', () => {
  const restarted: Traffic = { ...pair, interfaces: [{ name: 'ens18', samples: [sample(0, 0), sample(5, megabit), sample(10, 2 * megabit), sample(15, 100), sample(20, megabit), sample(25, 50), sample(30, 2 * megabit)] }] };
  render(<TrafficView traffic={restarted} zones={zones} selected={['ens18']} onSelect={vi.fn()} />);
  // Two rates, a gap where the counter began again, one rate alone, another gap, one more alone.
  expect(drawn('ens18', 'traffic-received').match(/[MLh]/g)!.join('')).toBe('MLMhMh');
});

it('gives an interface that has gone its hour, but no rate now', () => {
  render(<TrafficView traffic={many} zones={zones} selected={['ppp0', 'veth0']} onSelect={vi.fn()} />);
  expect(row('ppp0', 'Download')).toEqual(['—', '2.0 Mbps', '2.0 Mbps']);
  // One reading makes no rate at all.
  expect(row('veth0', 'Download')).toEqual(['—', '—', '—']);
});

it('lets the user tick which interfaces show when there are more than two', async () => {
  const onSelect = vi.fn();
  const view = render(<TrafficView traffic={many} zones={zones} selected={['ens18', 'ens19']} onSelect={onSelect} />);
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
  view.rerender(<TrafficView traffic={many} zones={zones} selected={['ens18', 'ens19', 'ens20', 'veth0', 'ppp0']} onSelect={onSelect} />);
  expect(screen.getByRole('button', { name: 'Interfaces shown: 5 selected' })).toHaveTextContent('5 selected');
  expect(screen.getAllByRole('region')).toHaveLength(5);
});

it('says so when the engine has read nothing yet', () => {
  render(<TrafficView traffic={{ intervalSeconds: 5, complete: true, interfaces: [] }} zones={zones} selected={[]} onSelect={vi.fn()} />);
  expect(screen.getByText('No readings yet.')).toBeInTheDocument();
});

it('gives an interface\'s lines its zone\'s colour, and its own to one in no zone', () => {
  render(<TrafficView traffic={many} zones={zones} selected={['ens18', 'veth0']} onSelect={vi.fn()} />);
  expect(graph('ens18').style.getPropertyValue('--traffic-line-light')).toBe('var(--mantine-color-red-8)');
  expect(graph('ens18').style.getPropertyValue('--traffic-line-dark')).toBe('var(--mantine-color-red-4)');
  expect(graph('veth0').style.getPropertyValue('--traffic-line-light')).toBe('');
});

it('reads the hour once, then every 5 seconds only what came after the newest reading it holds', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  const tail: Traffic = { intervalSeconds: 5, complete: false, interfaces: [{ name: 'ens18', samples: [sample(20, 12 * megabit, 4 * megabit)] }, { name: 'ens19', samples: [] }] };
  const fetchMock = fetchRoutes({ '/api/v1/status/traffic?since=': () => jsonResponse(200, tail), '/api/v1/status/traffic': () => jsonResponse(200, pair) });
  render(<TrafficWidget api={new ApiClient('csrf')} zones={zones} zonesSettled onSessionLost={vi.fn()} />);
  await waitFor(() => expect(row('ens18', 'Download')[0]).toBe('4.0 Mbps'));
  expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(['/api/v1/status/traffic']);
  act(() => { vi.advanceTimersByTime(5_000); });
  // 5 megabits more in 5 seconds; the average and the peak now count four rates.
  await waitFor(() => expect(row('ens18', 'Download')).toEqual(['5.0 Mbps', '3.0 Mbps', '5.0 Mbps']));
  expect(fetchMock.mock.calls.map(([url]) => url)[1]).toBe(`/api/v1/status/traffic?since=${encodeURIComponent(sample(15, 0).at)}`);
  // ens19 had nothing new: it keeps what it had.
  expect(row('ens19', 'Download')[1]).toBe('1.0 kbps');
});

it('makes its first choice once, when the zones are known: wan and lan, and it stays as chosen', async () => {
  fetchRoutes({ '/api/v1/status/traffic': many });
  const view = render(<TrafficWidget api={new ApiClient('csrf')} zones={new Map()} zonesSettled={false} onSessionLost={vi.fn()} />);
  // Before the zones are in, the busiest shows, but nothing is fixed yet.
  await waitFor(() => expect(screen.getAllByRole('region').map((region) => region.getAttribute('aria-label'))).toEqual(['Traffic on ens18']));
  view.rerender(<TrafficWidget api={new ApiClient('csrf')} zones={zones} zonesSettled onSessionLost={vi.fn()} />);
  await waitFor(() => expect(screen.getAllByRole('region').map((region) => region.getAttribute('aria-label'))).toEqual(['Traffic on ens18', 'Traffic on ens19']));
  await userEvent.click(screen.getByRole('button', { name: /Interfaces shown/ }));
  await userEvent.click((await screen.findAllByRole('menuitemcheckbox'))[1]);
  await waitFor(() => expect(screen.getAllByRole('region')).toHaveLength(1));
  expect(screen.getByRole('button', { name: 'Interfaces shown: ens18' })).toBeInTheDocument();
});
