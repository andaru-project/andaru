import { afterEach, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { act, render, screen, waitFor } from '../test/render';
import type { Usage } from '../types';
import { ResourcesView, ResourcesWidget } from './ResourcesWidget';

const usage: Usage = {
  time: '2026-11-21T12:00:00Z', cpu: { total: 2000, idle: 1550 }, load: [0.52, 0.31, 0.1],
  memory: { totalBytes: 8 * 1024 ** 3, availableBytes: 6 * 1024 ** 3, swapTotalBytes: 2 * 1024 ** 3, swapFreeBytes: 1024 ** 3 },
  fileSystems: [{ mountPoint: '/var', type: 'xfs', totalBytes: 20 * 1024 ** 3, availableBytes: 1024 ** 3 }, { mountPoint: '/boot', type: 'ext4', totalBytes: 1024 ** 3, availableBytes: 512 * 1024 ** 2 }],
  connections: { count: 1234, limit: 262144 },
  temperatures: [{ sensor: 'coretemp Package id 0', celsius: 45.5 }],
};

const bar = (name: RegExp) => screen.getByRole('progressbar', { name });

afterEach(() => vi.useRealTimers());

it('shows each measure as a bar with its number in words beside it', () => {
  render(<ResourcesView usage={usage} before={{ ...usage, cpu: { total: 1000, idle: 800 } }} />);
  expect(screen.getByText(/25%, load 0\.52 0\.31 0\.10/)).toBeInTheDocument();
  expect(bar(/^CPU: 25% used$/)).toHaveAttribute('aria-valuenow', '25');
  expect(screen.getByText('25%: 6.0 GiB free of 8.0 GiB')).toBeInTheDocument();
  expect(bar(/^Memory: 25% used$/)).toBeInTheDocument();
  expect(screen.getByText('50%: 1.0 GiB free of 2.0 GiB')).toBeInTheDocument();
  expect(screen.getByText('95%: 1.0 GiB free of 20 GiB')).toBeInTheDocument();
  expect(bar(/^Disk \/var: 95% used$/)).toBeInTheDocument();
  expect(bar(/^Disk \/boot: 50% used$/)).toBeInTheDocument();
  expect(screen.getByText('1,234 of 262,144 tracked')).toBeInTheDocument();
  expect(bar(/^Connections: 0% used$/)).toBeInTheDocument();
  expect(screen.getByText(/45\.5 °C/)).toBeInTheDocument();
});

it('leaves out what the box does not have, and does not guess the processors\' share from one reading', () => {
  render(<ResourcesView usage={{ ...usage, load: null, connections: null, temperatures: [], fileSystems: [], memory: { ...usage.memory, swapTotalBytes: 0, swapFreeBytes: 0 } }} />);
  expect(bar(/^CPU: not known yet$/)).toBeInTheDocument();
  expect(screen.queryByText(/load/)).not.toBeInTheDocument();
  for (const name of [/^Swap/, /^Disk/, /^Connections/]) expect(screen.queryByRole('progressbar', { name })).not.toBeInTheDocument();
  expect(screen.queryByText(/°C/)).not.toBeInTheDocument();
});

it('reads the usage every 5 seconds and works out the processors\' share from the last two readings', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  let cpu = { total: 1000, idle: 800 };
  fetchRoutes({ '/api/v1/status/usage': () => jsonResponse(200, { ...usage, cpu }) });
  render(<ResourcesWidget api={new ApiClient('csrf')} onSessionLost={vi.fn()} />);
  expect(await screen.findByRole('progressbar', { name: 'CPU: not known yet' })).toBeInTheDocument();
  cpu = { total: 2000, idle: 1000 };
  act(() => { vi.advanceTimersByTime(5_000); });
  await waitFor(() => expect(bar(/^CPU: 80% used$/)).toBeInTheDocument());
});
