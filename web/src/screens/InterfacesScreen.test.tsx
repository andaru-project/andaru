import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes } from '../test/fetch';
import { render, screen, within } from '../test/render';
import type { Session } from '../types';
import { InterfacesScreen } from './InterfacesScreen';

const session = (capabilities: string[]): Session => ({
  account: { id: '0657cce4-d4f9-4d1e-9cae-53a99855bb10', login: 'admin', displayName: 'Administrator' },
  capabilities, csrfToken: 'csrf', expiresAt: '2026-11-16T12:00:00Z', idleTimeoutSeconds: 1800,
});

const wan = 'lnk_11111111-1111-4111-8111-111111111111';
const guestVlan = 'lnk_22222222-2222-4222-8222-222222222222';
const gone = 'lnk_33333333-3333-4333-8333-333333333333';
const live = (ifIndex: number, name: string, linkType: string, linkAddress: string, addresses: string[] = []) => ({
  ifIndex, name, linkType, mtu: 1500, operState: 'UP', flags: ['UP'], linkAddress,
  addresses: addresses.map((address) => ({ family: 'ipv4', address, prefixLength: 24, scope: 'global' })),
});

function routes() {
  return fetchRoutes({
    '/api/v1/status/links': { items: [
      live(2, 'enp1s0', 'ether', '52:54:00:aa:bb:01', ['192.168.1.1']),
      live(3, 'enp2s0', 'ether', '52:54:00:aa:bb:02'),
      live(4, 'vl222222222222', 'ether', '52:54:00:aa:bb:01'),
    ] },
    '/api/v1/config': { state: 'saved', savedVersion: '7', unusedSecrets: [], value: {
      links: [
        { id: wan, name: 'LAN port', kind: 'ethernet', mtu: 1500, enabled: true, permanentMac: '52:54:00:aa:bb:01' },
        { id: guestVlan, name: 'Guest VLAN', kind: 'vlan', mtu: 1500, enabled: true, parent: wan, vlanId: 20 },
        { id: gone, name: 'Old port', kind: 'ethernet', mtu: 1500, enabled: true, permanentMac: '52:54:00:aa:bb:09' },
      ],
      segments: [
        { id: 'seg_lan', name: 'LAN', link: wan, zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } } },
        { id: 'seg_guest', name: 'Guest', link: guestVlan, zone: 'zon_guest', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'slaac' } } },
      ],
      zones: [{ id: 'zon_lan', name: 'Trusted' }, { id: 'zon_guest', name: 'Guests' }],
      management: { segment: 'seg_lan' },
    } },
    '/api/v1/preview/network?savedVersion=7': { state: 'preview', savedVersion: '7', realizable: false, resolved: [{ link: wan, interface: 'enp1s0' }], files: [], findings: [] },
  });
}

const row = (text: string) => screen.getByText(text).closest('tr') as HTMLElement;

it('joins live links with the saved links, segments and zones on them', async () => {
  routes();
  render(<InterfacesScreen api={new ApiClient()} session={session(['status.read', 'configuration.read'])} onSessionLost={vi.fn()} />);

  await screen.findByText('enp1s0');
  const lan = row('enp1s0');
  expect(lan).toHaveTextContent('LAN port');
  expect(lan).toHaveTextContent('Trusted');
  expect(lan).toHaveTextContent('IPv4 static, IPv6 none');
  expect(lan).toHaveTextContent('192.168.1.1/24');
  expect(within(lan).getByText('Management')).toBeInTheDocument();

  const guest = row('vl222222222222');
  expect(guest).toHaveTextContent('Guest VLAN');
  expect(guest).toHaveTextContent('Guests');
  expect(guest).toHaveTextContent('IPv4 dhcp, IPv6 slaac');
  expect(within(guest).queryByText('Management')).not.toBeInTheDocument();
});

it('lists a saved link with no live device as missing and an unused live device as unused', async () => {
  routes();
  render(<InterfacesScreen api={new ApiClient()} session={session(['status.read', 'configuration.read'])} onSessionLost={vi.fn()} />);

  await screen.findByText('enp1s0');
  expect(within(row('enp2s0')).getByText('Unused')).toBeInTheDocument();
  const missing = row('Old port');
  expect(within(missing).getByText('Missing')).toBeInTheDocument();
  expect(missing).toHaveTextContent('52:54:00:aa:bb:09');
});

it('shows only live links without configuration.read', async () => {
  const fetchMock = routes();
  render(<InterfacesScreen api={new ApiClient()} session={session(['status.read'])} onSessionLost={vi.fn()} />);

  await screen.findByText('enp1s0');
  expect(screen.getByText('Your account cannot read the saved configuration, so only live links are shown.')).toBeInTheDocument();
  expect(screen.queryByText('Unused')).not.toBeInTheDocument();
  expect(fetchMock).toHaveBeenCalledTimes(1);
});
