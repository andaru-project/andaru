import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { buildSetupOperations, defaultDhcpRange, effectiveMgmtIpv6Mode, type SetupNetworkDraft } from '../setup';
import { fetchRoutes } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen } from '../test/render';
import { SetupBootstrapScreen } from './SetupBootstrapScreen';
import { SetupWizardScreen } from './SetupWizardScreen';

const sampleLinks = [
  { name: 'lo', index: 1, operState: 'unknown', linkType: 'loopback', linkAddress: '00:00:00:00:00:00', mtu: 65536, addresses: [] },
  { name: 'docker0', index: 4, operState: 'up', linkType: 'ether', kind: 'bridge', linkAddress: '02:42:ac:11:00:01', permanentAddress: '02:42:ac:11:00:01', mtu: 1500, addresses: [] },
  { name: 'enp1s0', index: 2, operState: 'up', linkType: 'ether', linkAddress: '52:54:00:aa:bb:01', permanentAddress: '52:54:00:aa:bb:01', mtu: 1500, addresses: [{ family: 'ipv4', address: '192.168.1.1', prefixLength: 24 }] },
  { name: 'enp2s0', index: 3, operState: 'up', linkType: 'ether', linkAddress: '02:00:00:00:00:99', permanentAddress: '52:54:00:aa:bb:02', mtu: 1500, addresses: [] },
];

describe('SetupBootstrapScreen & SetupWizardScreen', () => {
  it('validates passwords and bootstraps the initial admin with setup code', async () => {
    const bootstrapped = session(['status.read', 'configuration.read', 'configuration.write', 'configuration.apply']);
    const fetchMock = fetchRoutes({
      'POST /api/v1/setup/bootstrap': () => new Response(JSON.stringify(bootstrapped), { status: 201, headers: { 'Content-Type': 'application/json' } }),
    });
    const onBootstrapped = vi.fn();
    render(<SetupBootstrapScreen api={new ApiClient('')} onBootstrapped={onBootstrapped} onAlreadyConfigured={vi.fn()} />);

    expect(screen.getByText(/andaru ctl setup-code/i)).toBeInTheDocument();

    await userEvent.type(screen.getByRole('textbox', { name: /One-time setup code/i }), 'ABCD-EFGH-JKMN-PQRS');
    await userEvent.type(screen.getByLabelText(/^Password/i), 'correct-horse-battery-9');
    await userEvent.type(screen.getByLabelText(/Confirm password/i), 'wrong-password');
    await userEvent.click(screen.getByRole('button', { name: /Create administrator and continue/i }));
    expect(screen.getByText('Passwords do not match.')).toBeInTheDocument();

    const confirm = screen.getByLabelText(/Confirm password/i);
    await userEvent.clear(confirm);
    await userEvent.type(confirm, 'correct-horse-battery-9');
    await userEvent.click(screen.getByRole('button', { name: /Create administrator and continue/i }));

    await vi.waitFor(() => expect(onBootstrapped).toHaveBeenCalledWith(bootstrapped));
    const call = fetchMock.mock.calls.find(([url]) => url === '/api/v1/setup/bootstrap');
    const sent = JSON.parse(String(call?.[1]?.body)) as Record<string, string>;
    expect(sent.setupCode).toBe('ABCD-EFGH-JKMN-PQRS');
    expect(sent.login).toBe('admin');
  });

  it('transitions to login when bootstrap returns 409 ALREADY_CONFIGURED', async () => {
    fetchRoutes({
      'POST /api/v1/setup/bootstrap': () => new Response(JSON.stringify({ error: { code: 'ALREADY_CONFIGURED', message: 'An administrator account already exists.' } }), { status: 409, headers: { 'Content-Type': 'application/json' } }),
    });
    const onAlreadyConfigured = vi.fn();
    render(<SetupBootstrapScreen api={new ApiClient('')} onBootstrapped={vi.fn()} onAlreadyConfigured={onAlreadyConfigured} />);

    await userEvent.type(screen.getByRole('textbox', { name: /One-time setup code/i }), 'ABCD-EFGH-JKMN-PQRS');
    await userEvent.type(screen.getByLabelText(/^Password/i), 'correct-horse-battery-9');
    await userEvent.type(screen.getByLabelText(/Confirm password/i), 'correct-horse-battery-9');
    await userEvent.click(screen.getByRole('button', { name: /Create administrator and continue/i }));

    await vi.waitFor(() => expect(onAlreadyConfigured).toHaveBeenCalledWith('An administrator account already exists.'));
  });

  it('walks through system, management, WAN, review, retry-safe apply, hostname, and confirmation', async () => {
    let previewCalls = 0;
    const fetchMock = fetchRoutes({
      '/api/v1/status/system': { version: '0.5.0-a.5', hostname: 'localhost', time: '2026-11-24T17:00:00Z', bootedAt: null },
      '/api/v1/status/links': { items: sampleLinks },
      '/api/v1/config': { savedVersion: '0', value: { links: [], segments: [], zones: [], roles: [] } },
      'POST /api/v1/config/changes': () => new Response(JSON.stringify({ savedVersion: '1' }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
      '/api/v1/preview/network?savedVersion=1': () => {
        previewCalls++;
        if (previewCalls === 1) {
          return new Response(JSON.stringify({ savedVersion: '1', resolved: [], findings: [{ severity: 'error', code: 'TEST', message: 'Transient check error' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } });
        }
        return new Response(JSON.stringify({ savedVersion: '1', resolved: [], findings: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } });
      },
      'POST /api/v1/applies': () => new Response(JSON.stringify({ id: '1', state: 'awaiting_confirmation', savedVersion: '1', deadline: '2026-11-24T17:03:00Z' }), { status: 201, headers: { 'Content-Type': 'application/json' } }),
      'POST /api/v1/system/hostname': () => new Response(JSON.stringify({ hostname: 'andaru-gw' }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
      'POST /api/v1/applies/1/confirmation': () => new Response(JSON.stringify({ id: '1', state: 'confirmed', savedVersion: '1' }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    });
    const onComplete = vi.fn();
    const admin = session(['status.read', 'configuration.read', 'configuration.write', 'configuration.apply']);
    render(<SetupWizardScreen api={new ApiClient('csrf-tok')} session={admin} onComplete={onComplete} />);

    const hostInput = await screen.findByLabelText(/Appliance hostname/i);
    await userEvent.clear(hostInput);
    await userEvent.type(hostInput, 'andaru-gw');
    await userEvent.click(screen.getByRole('button', { name: /Next: Management LAN/i }));
    await userEvent.click(await screen.findByRole('button', { name: /Next: WAN/i }));
    await userEvent.click(await screen.findByRole('button', { name: /Next: Review & Apply/i }));

    // First click hits preview error; hostname must NOT be changed yet.
    await userEvent.click(screen.getByRole('button', { name: /Save and apply/i }));
    expect(await screen.findByText('Transient check error')).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([url]) => url === '/api/v1/system/hostname')).toHaveLength(0);

    // Retry reuses committedVersion without posting /api/v1/config/changes a second time.
    await userEvent.click(screen.getByRole('button', { name: /Retry apply/i }));
    expect(await screen.findByText(/Configuration applied — confirm connectivity/i)).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([url]) => url === '/api/v1/config/changes')).toHaveLength(1);
    expect(fetchMock.mock.calls.filter(([url]) => url === '/api/v1/system/hostname')).toHaveLength(1);
    // Ports are saved by their permanent address, and a bridge is no port to assign.
    const changes = JSON.parse(String(fetchMock.mock.calls.find(([url]) => url === '/api/v1/config/changes')?.[1]?.body)) as { operations: Array<{ collection: string; value: { permanentMac?: string } }> };
    expect(changes.operations.filter((op) => op.collection === 'links').map((op) => op.value.permanentMac)).toEqual(['52:54:00:aa:bb:01', '52:54:00:aa:bb:02']);

    await userEvent.click(screen.getByRole('button', { name: /Confirm apply/i }));
    await vi.waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
  });

  it('retains confirmation controls if setHostname fails and rejects terminal failed applies', async () => {
    let applyCount = 0;
    fetchRoutes({
      '/api/v1/status/system': { version: '0.5.0-a.5', hostname: 'localhost', time: '2026-11-24T17:00:00Z', bootedAt: null },
      '/api/v1/status/links': { items: sampleLinks },
      '/api/v1/config': { savedVersion: '0', value: { links: [], segments: [], zones: [], roles: [] } },
      'POST /api/v1/config/changes': () => new Response(JSON.stringify({ savedVersion: '1' }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
      '/api/v1/preview/network?savedVersion=1': { savedVersion: '1', resolved: [], findings: [] },
      'POST /api/v1/applies': () => {
        applyCount++;
        if (applyCount === 1) {
          return new Response(JSON.stringify({ id: '1', state: 'failed', savedVersion: '1', reason: 'nftables syntax error' }), { status: 200, headers: { 'Content-Type': 'application/json' } });
        }
        return new Response(JSON.stringify({ id: '2', state: 'awaiting_confirmation', savedVersion: '1', deadline: '2026-11-24T17:03:00Z' }), { status: 201, headers: { 'Content-Type': 'application/json' } });
      },
      'POST /api/v1/system/hostname': () => new Response(JSON.stringify({ error: { code: 'HOSTNAME_FAILED', message: 'hostnamectl failed' } }), { status: 502, headers: { 'Content-Type': 'application/json' } }),
    });
    const admin = session(['status.read', 'configuration.read', 'configuration.write', 'configuration.apply']);
    render(<SetupWizardScreen api={new ApiClient('csrf-tok')} session={admin} onComplete={vi.fn()} />);

    const hostInput = await screen.findByLabelText(/Appliance hostname/i);
    await userEvent.clear(hostInput);
    await userEvent.type(hostInput, 'andaru-gw');
    await userEvent.click(screen.getByRole('button', { name: /Next: Management LAN/i }));
    await userEvent.click(await screen.findByRole('button', { name: /Next: WAN/i }));
    await userEvent.click(await screen.findByRole('button', { name: /Next: Review & Apply/i }));

    // First apply returns state='failed' -> shows error, not confirmation banner.
    await userEvent.click(screen.getByRole('button', { name: /Save and apply/i }));
    expect(await screen.findByText('nftables syntax error')).toBeInTheDocument();
    expect(screen.queryByText(/Configuration applied — confirm connectivity/i)).not.toBeInTheDocument();

    // Second apply succeeds, setHostname fails -> confirmation banner STILL shown with warning.
    await userEvent.click(screen.getByRole('button', { name: /Retry apply/i }));
    expect(await screen.findByText(/Configuration applied — confirm connectivity/i)).toBeInTheDocument();
    expect(await screen.findByText(/hostname could not be updated/i)).toBeInTheDocument();
  });

  it('builds singleton expectedVersion="0", forward DNS upstreams for static WAN, and effectiveMgmtIpv6Mode', () => {
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
      wanIpv4Mode: 'static',
      wanIpv4Address: '203.0.113.2/30',
      wanIpv4Gateway: '203.0.113.1',
      wanPppoeUsername: '',
      wanPppoePassword: '',
      wanPppoeVlanId: '',
      wanIpv6Mode: 'none',
      wanIpv6Address: '',
      wanIpv6Gateway: '',
      wanDhcp6PrefixLen: '56',
      wanDnsServers: '1.1.1.1, 9.9.9.9',
    };
    expect(effectiveMgmtIpv6Mode(base)).toBe('none');

    const ops = buildSetupOperations(base);
    const mgmtOp = ops.find((o) => o.collection === 'management');
    const dnsOp = ops.find((o) => o.collection === 'dns');
    const natOp = ops.find((o) => o.collection === 'nat.outbound');
    const wanSeg = ops.find((o) => o.localId === 'wan_seg');

    expect(mgmtOp?.expectedVersion).toBe('0');
    expect(natOp?.expectedVersion).toBe('0');
    expect(dnsOp?.expectedVersion).toBe('0');
    expect(dnsOp?.value).toEqual({
      mode: 'forward',
      upstreams: [{ address: '1.1.1.1', protocol: 'do53' }, { address: '9.9.9.9', protocol: 'do53' }],
      segments: ['@mgmt_seg'],
    });
    expect((wanSeg?.value?.addressing as Record<string, unknown>).dns).toBeUndefined();
    const ruleNames = ops.filter((o) => o.collection === 'rules').map((o) => o.value?.name);
    expect(ruleNames).toEqual(['Allow WebUI on LAN', 'Allow DNS on LAN', 'Allow LAN to WAN']);
  });

  it('builds PPPoE WAN operations (untagged and VLAN-tagged with IPv6 delegation) and completes the wizard PPPoE flow', async () => {
    const basePppoe: SetupNetworkDraft = {
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
      wanIpv4Mode: 'pppoe',
      wanIpv4Address: '',
      wanIpv4Gateway: '',
      wanPppoeUsername: 'kpn@internet',
      wanPppoePassword: 'secret-pw',
      wanPppoeVlanId: '6',
      wanIpv6Mode: 'dhcp6',
      wanIpv6Address: '',
      wanIpv6Gateway: '',
      wanDhcp6PrefixLen: '56',
      wanDnsServers: '',
    };
    const taggedOps = buildSetupOperations(basePppoe, { existingSecrets: ['PPPOE_WAN_PASSWORD'] });
    expect(taggedOps.find((o) => o.localId === 'wan_port')?.value).toEqual({
      name: 'enp2s0', kind: 'ethernet', permanentMac: '52:54:00:aa:bb:02', mtu: 1500, enabled: true,
    });
    expect(taggedOps.find((o) => o.localId === 'wan_vlan')?.value).toEqual({
      name: 'enp2s0.6', kind: 'vlan', parent: '@wan_port', vlanId: 6, mtu: 1500, enabled: true,
    });
    expect(taggedOps.find((o) => o.localId === 'wan_link')?.value).toEqual({
      name: 'WAN PPPoE',
      kind: 'pppoe',
      parent: '@wan_vlan',
      mtu: 1492,
      enabled: true,
      username: 'kpn@internet',
      password: { secret: 'PPPOE_WAN_PASSWORD_2', set: 'secret-pw', expectedVersion: '0' },
    });
    expect(taggedOps.find((o) => o.localId === 'wan_seg')?.value).toEqual({
      name: 'WAN',
      link: '@wan_link',
      zone: '@wan_zone',
      addressing: { ipv4: { mode: 'ppp' }, ipv6: { mode: 'dhcp6', requestPrefixLength: 56 } },
    });
    expect((taggedOps.find((o) => o.localId === 'mgmt_seg')?.value?.addressing as Record<string, unknown>).ipv6).toEqual({
      mode: 'delegated', from: '@wan_seg', subnetId: '0', ra: { mode: 'unmanaged' },
    });

    // Untagged PPPoE omits wan_vlan, attaches directly to @wan_port, and ignores stale static IPv6 / wanDnsServers.
    const untaggedOps = buildSetupOperations({ ...basePppoe, wanPppoeVlanId: '', wanIpv6Mode: 'static', wanDnsServers: '1.1.1.1' });
    expect(untaggedOps.find((o) => o.localId === 'wan_vlan')).toBeUndefined();
    expect(untaggedOps.find((o) => o.localId === 'wan_link')?.value?.parent).toBe('@wan_port');
    expect((untaggedOps.find((o) => o.localId === 'wan_seg')?.value?.addressing as Record<string, unknown>).ipv6).toEqual({ mode: 'none' });
    expect(untaggedOps.find((o) => o.collection === 'dns')?.value).toEqual({ mode: 'recursive', segments: ['@mgmt_seg'] });

    // Interactive wizard PPPoE flow (including existing secret collision avoidance, static IPv6 reset, and VLAN ID validation).
    const fetchMock = fetchRoutes({
      '/api/v1/status/system': { version: '0.5.0-a.5', hostname: 'andaru-gw', time: '2026-11-24T17:00:00Z', bootedAt: null },
      '/api/v1/status/links': { items: sampleLinks },
      '/api/v1/config': { savedVersion: '0', unusedSecrets: ['PPPOE_WAN_PASSWORD'], value: { links: [], segments: [], zones: [], roles: [] } },
      'POST /api/v1/config/changes': () => new Response(JSON.stringify({ savedVersion: '1' }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
      '/api/v1/preview/network?savedVersion=1': { savedVersion: '1', resolved: [], findings: [] },
      'POST /api/v1/applies': () => new Response(JSON.stringify({ id: '1', state: 'awaiting_confirmation', savedVersion: '1', deadline: '2026-11-24T17:03:00Z' }), { status: 201, headers: { 'Content-Type': 'application/json' } }),
    });
    const admin = session(['status.read', 'configuration.read', 'configuration.write', 'configuration.apply']);
    render(<SetupWizardScreen api={new ApiClient('csrf-tok')} session={admin} onComplete={vi.fn()} />);

    await screen.findByLabelText(/Appliance hostname/i);
    await userEvent.click(screen.getByRole('button', { name: /Next: Management LAN/i }));
    await userEvent.click(await screen.findByRole('button', { name: /Next: WAN/i }));

    // Selecting Static IPv6 then switching WAN IPv4 to PPPoE resets IPv6 to None and disables Static IPv6.
    const staticRadios = screen.getAllByRole('radio', { name: 'Static' });
    await userEvent.click(staticRadios[1]);
    await userEvent.click(screen.getByRole('radio', { name: 'PPPoE' }));
    expect(staticRadios[1]).toBeDisabled();
    const nextReview = screen.getByRole('button', { name: /Next: Review & Apply/i });
    expect(nextReview).toBeDisabled();

    await userEvent.type(screen.getByLabelText(/^PPPoE username/i), 'fiber@isp.example');
    await userEvent.type(screen.getByLabelText(/^PPPoE password/i), 'super-secret');
    const vlanInput = screen.getByLabelText(/^WAN VLAN ID/i);
    await userEvent.type(vlanInput, '006');
    expect(screen.getByText('A VLAN ID is a whole number from 1 to 4094.')).toBeInTheDocument();
    expect(nextReview).toBeDisabled();
    await userEvent.clear(vlanInput);
    await userEvent.type(vlanInput, '35');
    expect(nextReview).toBeEnabled();

    await userEvent.click(nextReview);
    expect(screen.getByText(/PPPoE \(fiber@isp\.example, VLAN 35\)/)).toBeInTheDocument();
    expect(screen.queryByText(/super-secret/)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: /Save and apply/i }));
    expect(await screen.findByText(/Configuration applied — confirm connectivity/i)).toBeInTheDocument();
    const sentBody = JSON.parse(String(fetchMock.mock.calls.find(([url]) => url === '/api/v1/config/changes')?.[1]?.body)) as { operations: Array<{ localId?: string; value?: Record<string, unknown> }> };
    expect(sentBody.operations.find((op) => op.localId === 'wan_link')?.value).toMatchObject({
      kind: 'pppoe',
      parent: '@wan_vlan',
      username: 'fiber@isp.example',
      password: { secret: 'PPPOE_WAN_PASSWORD_2', set: 'super-secret', expectedVersion: '0' },
    });
  });
});
