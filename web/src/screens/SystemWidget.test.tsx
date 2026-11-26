import { fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { act, render, screen, waitFor } from '../test/render';
import type { SystemInfo } from '../types';
import type { Resource } from '../useResource';
import { SystemDetailsScreen, SystemFacts as Both, SystemWidget } from './SystemWidget';

const hardware: SystemInfo = {
  version: '0.5.0-a.4', hostname: 'edge', kernel: '6.17.8-300.fc43.x86_64', architecture: 'x86_64',
  time: '2026-11-21T12:00:00Z', bootedAt: '2026-11-18T07:30:00Z', memoryBytes: 16 * 1024 ** 3, guest: '', iommu: true,
  cpu: {
    model: 'Intel(R) Core(TM) i5-1235U', cores: 10, threads: 12, microcode: '0x437', level: 'x86-64-v3',
    features: { cryptography: ['AES-NI', 'SHA-NI', 'AVX2'], security: ['NX', 'SMEP'], virtualization: ['VT-x'] },
    vulnerabilities: [{ name: 'meltdown', status: 'Not affected', state: 'not_affected' }, { name: 'spectre_v2', status: 'Mitigation: IBRS', state: 'mitigated' }],
  },
  hardware: { vendor: 'Protectli', product: 'VP2420', serial: 'ABC123', boardVendor: 'Protectli', board: 'VP2420', biosVendor: 'coreboot', biosVersion: 'v1.2.1', biosDate: '03/14/2026' },
};

const value = (label: string) => screen.getByRole('rowheader', { name: label }).nextElementSibling?.textContent;

afterEach(() => vi.useRealTimers());

it('shows what the box is, a line for each fact it reports', () => {
  render(<Both info={hardware} loadedAt={Date.now()} />);
  expect(value('Hostname')).toBe('edge');
  expect(value('Version')).toBe('0.5.0-a.4');
  expect(value('Kernel')).toBe('6.17.8-300.fc43.x86_64');
  expect(value('Clock')).toBe('2026-11-21 12:00 UTC');
  expect(value('Uptime')).toBe('3 days 4 hours');
  expect(value('Machine')).toBe('Protectli VP2420');
  expect(value('Serial')).toBe('ABC123');
  expect(value('BIOS')).toBe('coreboot v1.2.1 2026-03-14');
  expect(value('CPU')).toBe('Intel(R) Core(TM) i5-1235U, 10 cores, 12 threads');
  expect(value('Level')).toBe('x86-64-v3');
  expect(value('Microcode')).toBe('0x437');
  expect(value('Memory')).toBe('16 GiB');
  expect(value('Hardware crypto')).toBe('AES-NI, SHA-NI, AVX2');
  expect(value('CPU security')).toBe('NX, SMEP');
  expect(value('Virtualisation')).toBe('VT-x, IOMMU active');
  expect(value('CPU vulnerabilities')).toBe('all mitigated');
  // The board repeats the machine here, so it has no line of its own; hardware is not called a virtual machine.
  expect(screen.queryByRole('rowheader', { name: 'Board' })).not.toBeInTheDocument();
});

it('leaves out what a machine does not report, and says a guest is one', () => {
  render(<Both loadedAt={Date.now()} info={{
    ...hardware, hostname: '', bootedAt: null, guest: 'QEMU', iommu: false, architecture: 'aarch64',
    memoryBytes: 0,
    cpu: { ...hardware.cpu, model: '', cores: 1, threads: 1, microcode: '', level: '', features: { cryptography: [], security: [], virtualization: [] }, vulnerabilities: [] },
    hardware: { vendor: 'QEMU', product: 'KVM Virtual Machine', serial: '', boardVendor: '', board: '', biosVendor: '', biosVersion: '', biosDate: '' },
  }} />);
  expect(value('Hostname')).toBe('—');
  expect(value('Machine')).toBe('QEMU KVM Virtual Machine · virtual machine (QEMU)');
  expect(value('CPU')).toBe('aarch64, 1 core');
  expect(value('Hardware crypto')).toBe('none');
  expect(value('Virtualisation')).toBe('none');
  expect(value('CPU vulnerabilities')).toBe('no report');
  for (const label of ['Uptime', 'Serial', 'BIOS', 'Board', 'Memory', 'Level', 'Microcode']) expect(screen.queryByRole('rowheader', { name: label })).not.toBeInTheDocument();
});

it('names the vulnerabilities that are not mitigated, and those the kernel is unsure of', () => {
  const vulnerabilities: SystemInfo['cpu']['vulnerabilities'] = [
    { name: 'tsa', status: 'Vulnerable: No microcode', state: 'vulnerable' },
    { name: 'srbds', status: 'Unknown: Dependent on hypervisor status', state: 'unknown' },
    { name: 'meltdown', status: 'Mitigation: PTI', state: 'mitigated' },
  ];
  render(<Both info={{ ...hardware, cpu: { ...hardware.cpu, vulnerabilities } }} loadedAt={Date.now()} />);
  expect(value('CPU vulnerabilities')).toBe('not mitigated: tsa; unknown: srbds');
});

it('never calls a report good that it cannot read: unknown alone, or a state it does not know, is not "all mitigated"', () => {
  const vulnerabilities = [
    { name: 'srbds', status: 'Unknown: Dependent on hypervisor status', state: 'unknown' },
    { name: 'future', status: 'Partly', state: 'partly_exposed' },
    { name: 'meltdown', status: 'Mitigation: PTI', state: 'mitigated' },
  ] as SystemInfo['cpu']['vulnerabilities'];
  render(<Both info={{ ...hardware, cpu: { ...hardware.cpu, vulnerabilities } }} loadedAt={Date.now()} />);
  expect(value('CPU vulnerabilities')).toBe('unknown: srbds, future');
});

it('names a guest whose hypervisor the box cannot name without inventing one', () => {
  render(<Both info={{ ...hardware, guest: 'unknown' }} loadedAt={Date.now()} />);
  expect(value('Machine')).toBe('Protectli VP2420 · virtual machine');
});

it('lets the clock and the uptime run on by the browser\'s clock, whatever the box\'s clock says', () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date('2030-01-01T00:00:00Z'));
  // The box was up 40 minutes when it answered; its clock is years from the browser's.
  render(<Both info={{ ...hardware, time: '2026-11-21T12:00:00Z', bootedAt: '2026-11-21T11:20:00Z' }} loadedAt={Date.now()} />);
  expect(value('Uptime')).toBe('40 minutes');
  act(() => { vi.advanceTimersByTime(25 * 60_000); });
  expect(value('Uptime')).toBe('1 hour 5 minutes');
  expect(value('Clock')).toBe('2026-11-21 12:25 UTC');
});

it('shows the uptime as of now when the description was read long before the widget opened', () => {
  // The shell read it two hours ago; the user has just come back to the dashboard.
  render(<Both info={{ ...hardware, time: '2026-11-21T12:00:00Z', bootedAt: '2026-11-21T11:20:00Z' }} loadedAt={Date.now() - 2 * 3600_000} />);
  expect(value('Uptime')).toBe('2 hours 40 minutes');
});

it('shows no uptime it cannot work out', () => {
  render(<Both info={{ ...hardware, time: 'not a time', bootedAt: '2026-11-21T11:20:00Z' }} loadedAt={Date.now()} />);
  for (const label of ['Uptime', 'Clock']) expect(screen.queryByRole('rowheader', { name: label })).not.toBeInTheDocument();
});

it('lists the three rows of CPU features and nothing to open', () => {
  render(<Both info={hardware} loadedAt={Date.now()} />);
  expect(screen.getAllByRole('rowheader').map((header) => header.textContent).slice(-4)).toEqual(['Hardware crypto', 'CPU security', 'Virtualisation', 'CPU vulnerabilities']);
  expect(screen.queryByRole('button')).not.toBeInTheDocument();
});

it('keeps the dashboard widget short: what changes, and a line each for the machine and its processor', () => {
  render(<SystemWidget info={hardware} loadedAt={Date.now()} />);
  expect(screen.getAllByRole('rowheader').map((header) => header.textContent)).toEqual(['Version', 'Clock', 'Uptime', 'Machine', 'CPU']);
  expect(value('CPU')).toBe('Intel(R) Core(TM) i5-1235U, 10 cores');
});

it('shows a clickable upgrade notification pill beside the version when an update is available or staged', () => {
  const { rerender } = render(
    <MemoryRouter>
      <SystemWidget
        info={hardware}
        loadedAt={Date.now()}
        update={{ supported: true, state: 'available', available: { version: '0.5.0-a.5' }, rebootNeeded: false }}
      />
    </MemoryRouter>,
  );
  const availPill = screen.getByRole('link', { name: 'Update available: 0.5.0-a.5' });
  expect(availPill).toHaveAttribute('href', '/system/updates');

  rerender(
    <MemoryRouter>
      <SystemWidget
        info={hardware}
        loadedAt={Date.now()}
        update={{ supported: true, state: 'staged', staged: { version: '0.5.0-a.5' }, rebootNeeded: true }}
      />
    </MemoryRouter>,
  );
  const rebootPill = screen.getByRole('link', { name: 'Reboot to apply 0.5.0-a.5' });
  expect(rebootPill).toHaveAttribute('href', '/system/updates');
});

it('lets authorized operators change the appliance hostname from System details and hides the action when read-only', async () => {
  const user = userEvent.setup();
  const reload = vi.fn().mockResolvedValue(undefined);
  const onSessionLost = vi.fn();
  const system: Resource<{ info: SystemInfo; loadedAt: number } | undefined> = {
    data: { info: hardware, loadedAt: Date.now() },
    error: '',
    loading: false,
    reload,
    refresh: vi.fn(),
  };

  let mode: 'ok' | 'fail' | 'unauth' = 'ok';
  const bodies: string[] = [];
  const csrfHeaders: Array<string | null> = [];
  fetchRoutes({
    'POST /api/v1/system/hostname': (init) => {
      bodies.push(String(init?.body ?? ''));
      csrfHeaders.push(new Headers(init?.headers).get('X-Andaru-CSRF'));
      if (mode === 'fail') return jsonResponse(502, { error: { code: 'HOSTNAME_FAILED', message: 'Could not apply the new hostname.' } });
      if (mode === 'unauth') return jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Expired.' } });
      return jsonResponse(200, { hostname: 'edge-gw' });
    },
  });

  const api = new ApiClient('csrf-tok');
  const { rerender } = render(<SystemDetailsScreen system={system} api={api} canRename={false} onSessionLost={onSessionLost} />);
  expect(screen.queryByRole('button', { name: 'Change hostname' })).not.toBeInTheDocument();

  rerender(<SystemDetailsScreen system={system} api={api} canRename onSessionLost={onSessionLost} />);
  await user.click(screen.getByRole('button', { name: 'Change hostname' }));
  const input = await screen.findByLabelText(/Hostname/i, { selector: 'input' });
  expect((input as HTMLInputElement).value).toBe('edge');
  expect(screen.getByRole('button', { name: 'Save hostname' })).toBeDisabled();

  await user.clear(input);
  await user.type(input, '   ');
  await user.click(screen.getByRole('button', { name: 'Save hostname' }));
  expect(screen.getByText(/cannot start or end with a hyphen/i)).toBeInTheDocument();
  expect(bodies).toHaveLength(0);

  await user.clear(input);
  await user.type(input, 'a'.repeat(64));
  expect(screen.getByText(/cannot start or end with a hyphen/i)).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: 'Save hostname' }));
  expect(bodies).toHaveLength(0);

  const maxLen = 'a'.repeat(63);
  await user.clear(input);
  await user.type(input, maxLen);
  expect(screen.queryByText(/cannot start or end with a hyphen/i)).not.toBeInTheDocument();

  await user.clear(input);
  await user.type(input, '  Edge-GW  ');
  await user.click(screen.getByRole('button', { name: 'Save hostname' }));
  expect(await screen.findByText('Hostname updated to edge-gw.')).toBeInTheDocument();
  expect(bodies).toEqual([JSON.stringify({ hostname: 'edge-gw' })]);
  expect(csrfHeaders).toEqual(['csrf-tok']);
  expect(reload).toHaveBeenCalledTimes(1);

  mode = 'fail';
  await user.click(screen.getByRole('button', { name: 'Change hostname' }));
  const input2 = await screen.findByLabelText(/Hostname/i, { selector: 'input' });
  await user.clear(input2);
  await user.type(input2, 'core-gw');
  await user.click(screen.getByRole('button', { name: 'Save hostname' }));
  expect(await screen.findByText('Could not apply the new hostname.')).toBeInTheDocument();

  mode = 'unauth';
  await user.click(screen.getByRole('button', { name: 'Save hostname' }));
  await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
});

it('locks close controls while renaming, prevents double submit, allows normalizing mixed-case hostname, and reloads shell system state even if unmounted mid-flight', async () => {
  const user = userEvent.setup();
  const reload = vi.fn().mockResolvedValue(undefined);
  const system: Resource<{ info: SystemInfo; loadedAt: number } | undefined> = {
    data: { info: { ...hardware, hostname: 'Edge' }, loadedAt: Date.now() },
    error: '',
    loading: false,
    reload,
    refresh: vi.fn(),
  };

  let resolvePost!: (res: Response) => void;
  let postCount = 0;
  fetchRoutes({
    'POST /api/v1/system/hostname': () => {
      postCount += 1;
      return new Promise<Response>((resolve) => { resolvePost = resolve; });
    },
  });

  const { unmount } = render(<SystemDetailsScreen system={system} api={new ApiClient('csrf')} canRename />);
  await user.click(screen.getByRole('button', { name: 'Change hostname' }));
  const input = await screen.findByLabelText(/Hostname/i, { selector: 'input' });
  // Mixed-case reported hostname ("Edge") can be normalized to lowercase ("edge").
  expect(screen.getByRole('button', { name: 'Save hostname' })).not.toBeDisabled();
  await user.clear(input);
  await user.type(input, 'edge');
  await user.click(screen.getByRole('button', { name: 'Save hostname' }));

  expect(postCount).toBe(1);
  expect(screen.getByRole('button', { name: 'Save hostname' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Close' })).toBeDisabled();
  fireEvent.submit(input.closest('form')!);
  expect(postCount).toBe(1);

  // Unmount mid-flight; when POST resolves, shell-owned system.reload() must still run.
  unmount();
  resolvePost(jsonResponse(200, { hostname: 'edge' }));
  await waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
});
