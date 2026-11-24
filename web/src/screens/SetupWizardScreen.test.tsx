import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen } from '../test/render';
import { buildSetupOperations, defaultDhcpRange, type SetupNetworkDraft } from '../setup';
import { SetupWizardScreen } from './SetupWizardScreen';

const sampleLinks = [
  { name: 'lo', index: 1, operState: 'unknown', linkType: 'loopback', linkAddress: '00:00:00:00:00:00', mtu: 65536, addresses: [] },
  { name: 'enp1s0', index: 2, operState: 'up', linkType: 'ether', linkAddress: '52:54:00:aa:bb:01', mtu: 1500, addresses: [{ family: 'ipv4', address: '192.168.1.1', prefixLength: 24 }] },
  { name: 'enp2s0', index: 3, operState: 'up', linkType: 'ether', linkAddress: '52:54:00:aa:bb:02', mtu: 1500, addresses: [] },
];

describe('SetupWizardScreen', () => {
  it('validates passwords and bootstraps the initial admin with setup code and SSH key', async () => {
    const bootstrapped = session(['status.read', 'configuration.read', 'configuration.write', 'configuration.apply']);
    const fetchMock = fetchRoutes({
      'POST /api/v1/setup/bootstrap': () => new Response(JSON.stringify(bootstrapped), { status: 201, headers: { 'Content-Type': 'application/json' } }),
    });
    const onBootstrapped = vi.fn();
    render(<SetupWizardScreen api={new ApiClient('')} onBootstrapped={onBootstrapped} />);

    expect(screen.getByText(/andaru ctl setup-code/i)).toBeInTheDocument();

    await userEvent.type(screen.getByRole('textbox', { name: /One-time setup code/i }), 'ABCD-EFGH-JKMN-PQRS');
    await userEvent.type(screen.getByLabelText(/^Password/i), 'correct-horse-battery-9');
    await userEvent.type(screen.getByLabelText(/Confirm password/i), 'wrong-password');
    await userEvent.click(screen.getByRole('button', { name: /Create administrator and continue/i }));
    expect(screen.getByText('Passwords do not match.')).toBeInTheDocument();

    const confirm = screen.getByLabelText(/Confirm password/i);
    await userEvent.clear(confirm);
    await userEvent.type(confirm, 'correct-horse-battery-9');
    await userEvent.type(screen.getByLabelText(/SSH public key/i), 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest admin@laptop');
    await userEvent.click(screen.getByRole('button', { name: /Create administrator and continue/i }));

    await vi.waitFor(() => expect(onBootstrapped).toHaveBeenCalledWith(bootstrapped));
    const call = fetchMock.mock.calls.find(([url]) => url === '/api/v1/setup/bootstrap');
    expect(call).toBeDefined();
    const sent = JSON.parse(String(call?.[1]?.body)) as Record<string, string>;
    expect(sent.setupCode).toBe('ABCD-EFGH-JKMN-PQRS');
    expect(sent.login).toBe('admin');
    expect(sent.sshPublicKey).toContain('ssh-ed25519');
  });

  it('walks through system, management, WAN, review, apply, and confirmation', async () => {
    const fetchMock = fetchRoutes({
      '/api/v1/status/system': { version: '0.5.0-a.5', hostname: 'localhost', time: '2026-11-24T17:00:00Z', bootedAt: null },
      '/api/v1/status/links': { items: sampleLinks },
      '/api/v1/config': { savedVersion: '0', value: { links: [], segments: [], zones: [], roles: [] } },
      'POST /api/v1/system/hostname': () => new Response(JSON.stringify({ hostname: 'andaru-gw' }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
      'POST /api/v1/config/changes': () => new Response(JSON.stringify({ savedVersion: '1' }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
      '/api/v1/preview/network?savedVersion=1': { savedVersion: '1', resolved: [], findings: [] },
      'POST /api/v1/applies': () => new Response(JSON.stringify({ id: '1', state: 'awaiting_confirmation', savedVersion: '1', deadline: '2026-11-24T17:03:00Z' }), { status: 201, headers: { 'Content-Type': 'application/json' } }),
      'POST /api/v1/applies/1/confirmation': () => new Response(JSON.stringify({ id: '1', state: 'confirmed', savedVersion: '1' }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    });
    const onComplete = vi.fn();
    const admin = session(['status.read', 'configuration.read', 'configuration.write', 'configuration.apply']);
    render(<SetupWizardScreen api={new ApiClient('csrf-tok')} session={admin} onComplete={onComplete} />);

    const hostInput = await screen.findByLabelText(/Appliance hostname/i);
    await userEvent.clear(hostInput);
    await userEvent.type(hostInput, 'andaru-gw');
    await userEvent.click(screen.getByRole('button', { name: /Next: Management LAN/i }));

    expect(await screen.findByLabelText(/Management IPv4 address/i)).toHaveValue('192.168.1.1/24');
    await userEvent.click(screen.getByRole('button', { name: /Next: WAN/i }));

    await userEvent.click(await screen.findByRole('button', { name: /Next: Review & Apply/i }));
    expect(await screen.findByText('andaru-gw')).toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: /Save and apply/i }));
    expect(await screen.findByText(/Configuration applied — confirm connectivity/i)).toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: /Confirm apply/i }));
    await vi.waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));

    expect(fetchMock.mock.calls.some(([url]) => url === '/api/v1/system/hostname')).toBe(true);
    expect(fetchMock.mock.calls.some(([url]) => url === '/api/v1/config/changes')).toBe(true);
    expect(fetchMock.mock.calls.some(([url]) => url === '/api/v1/applies')).toBe(true);
    expect(fetchMock.mock.calls.some(([url]) => url === '/api/v1/applies/1/confirmation')).toBe(true);
  });

  it('builds atomic config operations for static LAN + DHCP + DNS + WAN and omits WAN/DHCP when disabled', () => {
    expect(defaultDhcpRange('10.20.30.1/24')).toEqual({ start: '10.20.30.100', end: '10.20.30.199' });
    expect(defaultDhcpRange('10.20.30.1/16')).toBeNull();

    const base: SetupNetworkDraft = {
      mgmtMac: '52:54:00:AA:BB:01',
      mgmtName: 'enp1s0',
      mgmtIpv4Mode: 'static',
      mgmtIpv4Address: '192.168.1.1/24',
      mgmtIpv6Mode: 'delegated',
      mgmtIpv6Address: '',
      mgmtIpv6RaMode: 'unmanaged',
      serveDhcp: true,
      dhcpStart: '192.168.1.100',
      dhcpEnd: '192.168.1.199',
      serveDns: true,
      wanMac: '52:54:00:AA:BB:02',
      wanName: 'enp2s0',
      wanIpv4Mode: 'dhcp',
      wanIpv4Address: '',
      wanIpv4Gateway: '',
      wanIpv6Mode: 'dhcp6',
      wanIpv6Address: '',
      wanIpv6Gateway: '',
      wanDhcp6PrefixLen: '56',
      wanDnsServers: '',
    };
    const full = buildSetupOperations(base);
    expect(full.map((o) => `${o.op}:${o.collection}`)).toEqual([
      'create:zones',
      'create:links',
      'create:zones',
      'create:links',
      'create:segments',
      'create:segments',
      'replace:management',
      'create:dhcp',
      'replace:dns',
      'replace:nat.outbound',
    ]);

    const minimal = buildSetupOperations({ ...base, mgmtIpv4Mode: 'dhcp', mgmtIpv6Mode: 'none', wanMac: '', serveDhcp: false, serveDns: false });
    expect(minimal.map((o) => `${o.op}:${o.collection}`)).toEqual([
      'create:zones',
      'create:links',
      'create:segments',
      'replace:management',
    ]);
  });
});
