import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen, within } from '../test/render';
import { InterfacesScreen } from './InterfacesScreen';

const lanPort = 'lnk_11111111-1111-4111-8111-111111111111';
const guestVlan = 'lnk_22222222-2222-4222-8222-222222222222';
const gone = 'lnk_33333333-3333-4333-8333-333333333333';
const live = (ifIndex: number, name: string, linkAddress: string, addresses: string[] = []) => ({
  ifIndex, name, linkType: 'ether', mtu: 1500, operState: 'UP', flags: ['UP'], linkAddress,
  addresses: addresses.map((address) => ({ family: 'ipv4', address, prefixLength: 24, scope: 'global' })),
});
const ethernet = (id: string, name: string, permanentMac: string) => ({ id, name, kind: 'ethernet', mtu: 1500, enabled: true, permanentMac });
const missingFinding = { severity: 'error', code: 'HARDWARE_MISSING', path: '/links/2/permanentMac', message: 'No interface with this permanent MAC is present.' };

function routes({ links, findings = [missingFinding], resolved = [{ link: lanPort, interface: 'enp1s0' }], config }: { links?: unknown[]; findings?: unknown[]; resolved?: unknown[]; config?: unknown } = {}) {
  return fetchRoutes({
    '/api/v1/status/links': { items: [
      live(2, 'enp1s0', '52:54:00:aa:bb:01', ['192.168.1.1']),
      live(3, 'enp2s0', '52:54:00:aa:bb:02'),
      live(4, 'vl222222222222', '52:54:00:aa:bb:01'),
    ] },
    '/api/v1/config': config ?? { state: 'saved', savedVersion: '7', unusedSecrets: [], value: {
      links: links ?? [
        ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'),
        { id: guestVlan, name: 'Guest VLAN', kind: 'vlan', mtu: 1500, enabled: true, parent: lanPort, vlanId: 20 },
        ethernet(gone, 'Old port', '52:54:00:aa:bb:09'),
      ],
      segments: [
        { id: 'seg_lan', name: 'LAN', link: lanPort, zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } } },
        { id: 'seg_guest', name: 'Guest', link: guestVlan, zone: 'zon_guest', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'slaac' } } },
      ],
      zones: [{ id: 'zon_lan', name: 'Trusted' }, { id: 'zon_guest', name: 'Guests' }],
      management: { segment: 'seg_lan' },
    } },
    '/api/v1/preview/network?savedVersion=7': { state: 'preview', savedVersion: '7', realizable: false, resolved, files: [], findings },
  });
}

const admin = session(['status.read', 'configuration.read']);
const row = (text: string) => screen.getByText(text).closest('tr') as HTMLElement;

async function showInterfaces(who = admin) {
  render(<InterfacesScreen api={new ApiClient()} session={who} onSessionLost={vi.fn()} />);
  await screen.findAllByText('enp1s0');
}

it('joins live links with the saved links, segments and zones on them', async () => {
  routes();
  await showInterfaces();

  const lan = row('enp1s0');
  expect(lan).toHaveTextContent('LAN port');
  expect(lan).toHaveTextContent('Trusted');
  expect(lan).toHaveTextContent('IPv4 static');
  expect(lan).toHaveTextContent('IPv6 none');
  expect(lan).toHaveTextContent('192.168.1.1/24');
  expect(within(lan).getByText('Management')).toBeInTheDocument();

  const guest = row('vl222222222222');
  expect(guest).toHaveTextContent('Guest VLAN');
  expect(guest).toHaveTextContent('Guests');
  expect(guest).toHaveTextContent('IPv4 dhcp');
  expect(guest).toHaveTextContent('IPv6 slaac');
  expect(within(guest).queryByText('Management')).not.toBeInTheDocument();
});

it('lists a saved link with no live device as missing and an unused live device as unused', async () => {
  routes();
  await showInterfaces();

  expect(within(row('enp2s0')).getByText('Not in the configuration')).toBeInTheDocument();
  const missing = row('Old port');
  expect(within(missing).getByText('Missing')).toBeInTheDocument();
  expect(missing).toHaveTextContent('No interface with this permanent MAC is present.');
  expect(missing).toHaveTextContent('52:54:00:aa:bb:09');
});

it('does not join a link the preview will not realize to the device with its name', async () => {
  routes({ findings: [missingFinding, { severity: 'error', code: 'NAME_COLLISION', path: '/links/1', message: 'The interface name vl222222222222 derived for this link is already taken.' }] });
  await showInterfaces();

  expect(within(row('vl222222222222')).getByText('Not in the configuration')).toBeInTheDocument();
  const guest = row('Guest VLAN');
  expect(within(guest).getByText('Not realized')).toBeInTheDocument();
  expect(guest).toHaveTextContent('already taken');
});

it('shows an Ethernet link the preview could not resolve as unresolved', async () => {
  routes({ findings: [{ severity: 'error', code: 'HARDWARE_AMBIGUOUS', path: '/links/2/permanentMac', message: 'Several interfaces share this permanent MAC: enp3s0, enp4s0.' }] });
  await showInterfaces();

  const old = row('Old port');
  expect(within(old).getByText('Unresolved')).toBeInTheDocument();
  expect(old).toHaveTextContent('Several interfaces share this permanent MAC');
});

it('shows a disabled saved link as disabled, and a link waiting on its parent with the reason', async () => {
  const child = 'lnk_55555555-5555-4555-8555-555555555555';
  routes({
    links: [
      ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'),
      { ...ethernet(gone, 'Old port', '52:54:00:aa:bb:09'), enabled: false },
      { id: child, name: 'Old VLAN', kind: 'vlan', mtu: 1500, enabled: true, parent: gone, vlanId: 30 },
    ],
    findings: [
      { ...missingFinding, path: '/links/1/permanentMac' },
      { severity: 'warning', code: 'DEPENDENCY_UNAVAILABLE', path: '/links/2/parent', message: 'Not rendered: the parent link is unavailable.' },
    ],
  });
  await showInterfaces();

  expect(within(row('Old port')).getByText('Disabled')).toBeInTheDocument();
  expect(within(row('Old port')).queryByText('Missing')).not.toBeInTheDocument();
  const vlan = row('Old VLAN');
  expect(within(vlan).getByText('Not realized')).toBeInTheDocument();
  expect(vlan).toHaveTextContent('Not rendered: the parent link is unavailable.');
});

it('shows both saved links that land on one device', async () => {
  const second = 'lnk_44444444-4444-4444-8444-444444444444';
  routes({ links: [ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'), ethernet(second, 'Spare port', '52:54:00:aa:bb:01')], findings: [], resolved: [{ link: lanPort, interface: 'enp1s0' }, { link: second, interface: 'enp1s0' }] });
  await showInterfaces();

  expect(screen.getAllByText('enp1s0')).toHaveLength(2);
  expect(row('LAN port')).toHaveTextContent('Conflict');
  expect(row('Spare port')).toHaveTextContent('Conflict');
});

it('keeps the live links when the saved configuration cannot be read', async () => {
  fetchRoutes({
    '/api/v1/status/links': { items: [live(2, 'enp1s0', '52:54:00:aa:bb:01')] },
    '/api/v1/config': () => jsonResponse(503, { error: { code: 'UNAVAILABLE', message: 'The store is busy.' } }),
  });
  await showInterfaces();

  expect(screen.getByRole('alert', { name: 'The saved configuration could not be read' })).toHaveTextContent('The store is busy.');
  expect(screen.queryByText('Saved configuration')).not.toBeInTheDocument();
});

it('shows only live links without configuration.read', async () => {
  const fetchMock = routes();
  await showInterfaces(session(['status.read']));

  expect(screen.getByText('Your account cannot read the saved configuration, so only live links are shown.')).toBeInTheDocument();
  expect(screen.queryByText('Not in the configuration')).not.toBeInTheDocument();
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

it('leaves the loopback out unless the configuration names it', async () => {
  fetchRoutes({
    '/api/v1/status/links': { items: [{ ...live(1, 'lo', '00:00:00:00:00:00'), linkType: 'loopback' }, live(2, 'enp1s0', '52:54:00:aa:bb:01')] },
    '/api/v1/config': () => jsonResponse(503, { error: { code: 'UNAVAILABLE', message: 'The store is busy.' } }),
  });
  await showInterfaces();

  expect(screen.queryByText('lo')).not.toBeInTheDocument();
});

it('keeps a loopback the configuration names, and marks a disabled link that has its device', async () => {
  const loop = 'lnk_66666666-6666-4666-8666-666666666666';
  fetchRoutes({
    '/api/v1/status/links': { items: [{ ...live(1, 'lo', '00:00:00:00:00:00'), linkType: 'loopback' }, live(2, 'enp1s0', '52:54:00:aa:bb:01')] },
    '/api/v1/config': { state: 'saved', savedVersion: '7', unusedSecrets: [], value: {
      links: [{ ...ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'), enabled: false }, ethernet(loop, 'Loopback', '00:00:00:00:00:00')],
      segments: [], zones: [],
    } },
    '/api/v1/preview/network?savedVersion=7': { state: 'preview', savedVersion: '7', realizable: true, resolved: [{ link: lanPort, interface: 'enp1s0' }, { link: loop, interface: 'lo' }], files: [], findings: [] },
  });
  await showInterfaces();

  expect(row('lo')).toHaveTextContent('Loopback');
  expect(within(row('enp1s0')).getByText('Disabled')).toBeInTheDocument();
});
