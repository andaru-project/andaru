import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { act, render, screen } from '../test/render';
import type { SystemInfo } from '../types';
import { SystemWidget } from './SystemWidget';

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
  render(<SystemWidget info={hardware} loadedAt={Date.now()} />);
  expect(value('Name')).toBe('edge');
  expect(value('Andaru')).toBe('0.5.0-a.4');
  expect(value('Kernel')).toBe('6.17.8-300.fc43.x86_64 (x86_64)');
  expect(value('Up for')).toBe('3 days 4 hours');
  expect(value('Machine')).toBe('Protectli VP2420');
  expect(value('Serial')).toBe('ABC123');
  expect(value('BIOS')).toBe('coreboot v1.2.1 03/14/2026');
  expect(value('CPU')).toBe('Intel(R) Core(TM) i5-1235U, 10 cores, 12 threads');
  expect(value('Level')).toBe('x86-64-v3');
  expect(value('Microcode')).toBe('0x437');
  expect(value('Memory')).toBe('16 GiB');
  expect(value('Cryptography')).toBe('AES-NI, SHA-NI, AVX2');
  expect(value('Security')).toBe('NX, SMEP');
  expect(value('Virtualisation')).toBe('VT-x, IOMMU active');
  expect(value('CPU vulnerabilities')).toBe('all mitigated');
  // The board repeats the machine here, so it has no line of its own; hardware has no "Runs as".
  for (const label of ['Board', 'Runs as']) expect(screen.queryByRole('rowheader', { name: label })).not.toBeInTheDocument();
});

it('leaves out what a machine does not report, and says a guest is one', () => {
  render(<SystemWidget loadedAt={Date.now()} info={{
    ...hardware, hostname: '', bootedAt: null, guest: 'QEMU', iommu: false, architecture: 'aarch64',
    cpu: { ...hardware.cpu, model: '', cores: 4, threads: 4, microcode: '', level: '', features: { cryptography: [], security: [], virtualization: [] }, vulnerabilities: [] },
    hardware: { vendor: 'QEMU', product: 'KVM Virtual Machine', serial: '', boardVendor: '', board: '', biosVendor: '', biosVersion: '', biosDate: '' },
  }} />);
  expect(value('Name')).toBe('—');
  expect(value('Machine')).toBe('QEMU KVM Virtual Machine');
  expect(value('Runs as')).toBe('a virtual machine (QEMU)');
  expect(value('CPU')).toBe('aarch64, 4 cores');
  expect(value('Cryptography')).toBe('none');
  expect(value('Virtualisation')).toBe('none');
  expect(value('CPU vulnerabilities')).toBe('no report');
  for (const label of ['Up for', 'Serial', 'BIOS', 'Level', 'Microcode', 'Board']) expect(screen.queryByRole('rowheader', { name: label })).not.toBeInTheDocument();
});

it('names the vulnerabilities that are not mitigated, and those the kernel is unsure of', () => {
  const vulnerabilities: SystemInfo['cpu']['vulnerabilities'] = [
    { name: 'tsa', status: 'Vulnerable: No microcode', state: 'vulnerable' },
    { name: 'srbds', status: 'Unknown: Dependent on hypervisor status', state: 'unknown' },
    { name: 'meltdown', status: 'Mitigation: PTI', state: 'mitigated' },
  ];
  render(<SystemWidget info={{ ...hardware, cpu: { ...hardware.cpu, vulnerabilities } }} loadedAt={Date.now()} />);
  expect(value('CPU vulnerabilities')).toBe('not mitigated: tsa; unknown: srbds');
});

it('lets the uptime run on by the browser\'s clock, whatever the box\'s clock says', () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date('2030-01-01T00:00:00Z'));
  // The box was up 40 minutes when it answered; its clock is years from the browser's.
  render(<SystemWidget info={{ ...hardware, time: '2026-11-21T12:00:00Z', bootedAt: '2026-11-21T11:20:00Z' }} loadedAt={Date.now()} />);
  expect(value('Up for')).toBe('40 minutes');
  act(() => { vi.advanceTimersByTime(25 * 60_000); });
  expect(value('Up for')).toBe('1 hour 5 minutes');
});

it('says why the CPU features matter when asked, by a click or the keyboard', async () => {
  render(<SystemWidget info={hardware} loadedAt={Date.now()} />);
  expect(screen.queryByText(/AES-GCM in hardware: IPsec/)).not.toBeInTheDocument();
  screen.getByRole('button', { name: 'Why these CPU features matter' }).focus();
  await userEvent.keyboard('{Enter}');
  expect(await screen.findByText(/AES-GCM in hardware: IPsec, OpenVPN and TLS/)).toBeInTheDocument();
  expect(screen.getByText(/WireGuard and TLS handshakes/)).toBeInTheDocument();
});
