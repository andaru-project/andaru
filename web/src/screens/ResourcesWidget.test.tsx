import { afterEach, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { act, render, screen, waitFor } from '../test/render';
import type { Usage } from '../types';
import { ResourcesView, ResourcesWidget } from './ResourcesWidget';

const usage: Usage = {
  time: '2026-11-21T12:00:05Z', cpu: { total: 2000, idle: 1550 }, load: [0.52, 0.31, 0.1],
  memory: { totalBytes: 8 * 1024 ** 3, availableBytes: 6 * 1024 ** 3, swapTotalBytes: 2 * 1024 ** 3, swapFreeBytes: 1024 ** 3 },
  fileSystems: [{ mountPoint: '/var', type: 'xfs', totalBytes: 20 * 1024 ** 3, availableBytes: 1024 ** 3 }, { mountPoint: '/boot', type: 'ext4', totalBytes: 1024 ** 3, availableBytes: 512 * 1024 ** 2 }],
  connections: { count: 1234, limit: 262144 },
  temperatures: [{ sensor: 'coretemp Package id 0', celsius: 45.5 }],
};
const before: Usage = { ...usage, time: '2026-11-21T12:00:00Z', cpu: { total: 1000, idle: 800 } };

const bar = (name: string) => screen.getByRole('progressbar', { name });
// The figures on a measure's line, to the right of its label.
const figures = (label: string) => screen.getByText(label).nextElementSibling?.textContent;

afterEach(() => vi.useRealTimers());

it('shows each measure as a label, its figures and a bar', () => {
  render(<ResourcesView usage={usage} before={before} />);
  expect(figures('CPU')).toBe('25%');
  expect(bar('CPU')).toHaveAttribute('aria-valuenow', '25');
  expect(figures('Load 1 · 5 · 15 min')).toBe('0.52 · 0.31 · 0.10');
  expect(figures('Memory')).toBe('2.0 GiB / 8.0 GiB · 25%');
  expect(figures('Swap')).toBe('1.0 GiB / 2.0 GiB · 50%');
  expect(figures('Disk /var')).toBe('19 GiB / 20 GiB · 95%');
  expect(bar('Disk /var')).toHaveAttribute('aria-valuenow', '95');
  expect(figures('Disk /boot')).toBe('512 MiB / 1.0 GiB · 50%');
  expect(figures('Connections')).toBe('1,234 / 262,144 · 0%');
  expect(figures('coretemp Package id 0')).toBe('45.5 °C');
});

it('marks elevated and high sensor temperatures with warning and error styles', () => {
  render(<ResourcesView usage={{ ...usage, temperatures: [
    { sensor: 'warm', celsius: 72.0 },
    { sensor: 'hot', celsius: 86.5 },
  ] }} before={before} />);
  expect(screen.getByText('72.0 °C')).toHaveClass('warning-text');
  expect(screen.getByText('86.5 °C')).toHaveClass('error-text');
});

it('leaves out what the box does not have, and guesses nothing', () => {
  render(<ResourcesView usage={{ ...usage, load: null, connections: null, temperatures: [], fileSystems: [], memory: { ...usage.memory, swapTotalBytes: 0, swapFreeBytes: 0 } }} />);
  // One reading gives no share of the processors.
  expect(figures('CPU')).toBe('—');
  expect(bar('CPU: not known yet')).toBeInTheDocument();
  for (const label of [/^Load/, /^Swap/, /^Disk/, /^Connections/]) expect(screen.queryByText(label)).not.toBeInTheDocument();
  expect(screen.queryByText(/°C/)).not.toBeInTheDocument();
});

it('keeps a share between nothing and all, whatever the box reports', () => {
  render(<ResourcesView usage={{ ...usage, memory: { ...usage.memory, availableBytes: 12 * 1024 ** 3 }, fileSystems: [{ mountPoint: '/a', type: 'x', totalBytes: 100, availableBytes: 10 }, { mountPoint: '/a', type: 'x', totalBytes: 100, availableBytes: 20 }] }} />);
  // More available than there is: no negative bar. Two file systems at one mount point: both show.
  expect(bar('Memory')).toHaveAttribute('aria-valuenow', '0');
  expect(figures('Memory')).toBe('0 B / 8.0 GiB · 0%');
  expect(screen.getAllByRole('progressbar', { name: 'Disk /a' })).toHaveLength(2);
});

it('reads the usage every 5 seconds and works out the processors\' share from the last two readings', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  let reading = before;
  const fetchMock = fetchRoutes({ '/api/v1/status/usage': () => jsonResponse(200, reading) });
  render(<ResourcesWidget api={new ApiClient('csrf')} onSessionLost={vi.fn()} />);
  await waitFor(() => expect(bar('CPU: not known yet')).toBeInTheDocument());
  act(() => { vi.advanceTimersByTime(4_999); });
  expect(fetchMock).toHaveBeenCalledTimes(1);
  reading = { ...usage, cpu: { total: 2000, idle: 1000 } };
  act(() => { vi.advanceTimersByTime(1); });
  await waitFor(() => expect(bar('CPU')).toHaveAttribute('aria-valuenow', '80'));
});
