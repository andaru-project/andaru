import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen, within } from '../test/render';
import { InterfacesScreen } from './InterfacesScreen';

const lanPort = 'lnk_11111111-1111-4111-8111-111111111111';
const guestVlan = 'lnk_22222222-2222-4222-8222-222222222222';
const gone = 'lnk_33333333-3333-4333-8333-333333333333';
const live = (ifIndex: number, name: string, linkAddress: string, addresses: string[] = [], kind?: string) => ({
  ifIndex, name, linkType: 'ether', mtu: 1500, operState: 'UP', flags: ['UP'], linkAddress, permanentAddress: linkAddress, ...(kind ? { kind } : {}),
  addresses: addresses.map((address) => ({ family: 'ipv4', address, prefixLength: 24, scope: 'global' })),
});
const ethernet = (id: string, name: string, permanentMac: string) => ({ id, name, kind: 'ethernet', mtu: 1500, enabled: true, permanentMac });
const missingFinding = { severity: 'error', code: 'HARDWARE_MISSING', path: '/links/2/permanentMac', message: 'No interface with this permanent MAC is present.' };

function routes({ links, findings = [missingFinding], resolved = [{ link: lanPort, interface: 'enp1s0' }], config, extra = {} }: { links?: unknown[]; findings?: unknown[]; resolved?: unknown[]; config?: unknown; extra?: Parameters<typeof fetchRoutes>[0] } = {}) {
  return fetchRoutes({
    '/api/v1/status/links': { items: [
      live(2, 'enp1s0', '52:54:00:aa:bb:01', ['192.168.1.1']),
      live(3, 'enp2s0', '52:54:00:aa:bb:02'),
      live(4, 'vl222222222222', '52:54:00:aa:bb:01', [], 'vlan'),
      live(5, 'docker0', '02:42:ac:11:00:01', [], 'bridge'),
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
    ...extra,
  });
}

const admin = session(['status.read', 'configuration.read']);
const row = (text: string) => screen.getByText(text).closest('tr') as HTMLElement;

async function showInterfaces(who = admin) {
  render(<InterfacesScreen api={new ApiClient()} session={who} onSaved={vi.fn()} onSessionLost={vi.fn()} />);
  await screen.findAllByText('enp1s0');
}

it('joins live links with the saved links, segments and zones on them', async () => {
  routes();
  await showInterfaces();

  const lan = row('LAN port');
  expect(lan).toHaveTextContent('enp1s0');
  expect(within(lan).getAllByText(/zone Trusted/)).toHaveLength(1);
  expect(within(lan).getByText('Management')).toBeInTheDocument();
  // Each family's method with its live addresses; a family with neither is left out.
  expect(lan).toHaveTextContent('IPv4 · Static192.168.1.1/24');
  expect(lan).not.toHaveTextContent('IPv6');
  // A plain Ethernet port says nothing of its kind, and a segment named apart from its link says its name.
  expect(lan).not.toHaveTextContent('ethernet');
  expect(lan).toHaveTextContent('Segment LAN');

  const guest = row('Guest VLAN');
  expect(guest).toHaveTextContent('vl222222222222');
  expect(guest).toHaveTextContent('Guests');
  expect(guest).toHaveTextContent('VLAN 20 on LAN port');
  expect(guest).toHaveTextContent('IPv4 · DHCP');
  expect(guest).toHaveTextContent('IPv6 · SLAAC');
  expect(within(guest).queryByText('Management')).not.toBeInTheDocument();
});

it('lists a saved link with no live device as missing and an unused live device as unused', async () => {
  routes();
  await showInterfaces();

  // A port the configuration does not name is listed apart from the configured interfaces.
  expect(screen.getByRole('heading', { name: 'Not in the configuration' })).toBeInTheDocument();
  expect(row('enp2s0').closest('table')).not.toBe(row('LAN port').closest('table'));
  const missing = row('Old port');
  expect(missing).toHaveTextContent('No device');
  expect(within(missing).getByText('Missing')).toBeInTheDocument();
  expect(missing).toHaveTextContent('No interface with this permanent MAC is present.');
  expect(missing).toHaveTextContent('52:54:00:aa:bb:09');
});

it('does not join a link the preview will not realize to the device with its name', async () => {
  routes({ findings: [missingFinding, { severity: 'error', code: 'NAME_COLLISION', path: '/links/1', message: 'The interface name vl222222222222 derived for this link is already taken.' }] });
  await showInterfaces();

  expect(row('vl222222222222').closest('table')).not.toBe(row('Guest VLAN').closest('table'));
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

it('lets each of two conflicting saved links be edited, so either can be removed', async () => {
  const second = 'lnk_44444444-4444-4444-8444-444444444444';
  routes({ links: [ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'), ethernet(second, 'Spare port', '52:54:00:aa:bb:01')], findings: [], resolved: [{ link: lanPort, interface: 'enp1s0' }, { link: second, interface: 'enp1s0' }] });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);

  expect(await screen.findByRole('button', { name: 'Edit LAN port' })).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Edit Spare port' })).toBeInTheDocument();
});

it('opens no panel on a segment that moved to another link after the page loaded', async () => {
  const user = userEvent.setup();
  routes({ extra: {
    [`/api/v1/config/links/${lanPort}`]: { version: '4', value: ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01') },
    '/api/v1/config/segments/seg_lan': { version: '7', value: { id: 'seg_lan', name: 'LAN', link: guestVlan, zone: 'zon_lan', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } } } },
  } });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);
  await user.click(await screen.findByRole('button', { name: 'Edit LAN port' }));

  expect(await screen.findByRole('alert', { name: 'The interface could not be opened' })).toHaveTextContent('changed by someone else');
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
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

  expect(row('Loopback')).toHaveTextContent('lo');
  // Disabled in the configuration while the device is up: the change waits for an apply.
  expect(within(row('LAN port')).getByText('Pending: off')).toBeInTheDocument();
});

it('says nothing pending once a disabled link is down', async () => {
  fetchRoutes({
    '/api/v1/status/links': { items: [{ ...live(2, 'enp1s0', '52:54:00:aa:bb:01'), operState: 'DOWN', flags: ['BROADCAST'] }] },
    '/api/v1/config': { state: 'saved', savedVersion: '7', unusedSecrets: [], value: { links: [{ ...ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'), enabled: false }], segments: [], zones: [] } },
    '/api/v1/preview/network?savedVersion=7': { state: 'preview', savedVersion: '7', realizable: true, resolved: [{ link: lanPort, interface: 'enp1s0' }], files: [], findings: [] },
  });
  await showInterfaces();

  expect(within(row('LAN port')).queryByText('Pending: off')).not.toBeInTheDocument();
});

const writer = session(['status.read', 'configuration.read', 'configuration.write']);

it('offers no edits to an account that cannot write the configuration', async () => {
  routes();
  await showInterfaces();
  expect(screen.queryByRole('button', { name: /^(Edit|Add) / })).not.toBeInTheDocument();
});

it('edits an interface against the versions read when the panel opened, and says the change is pending', async () => {
  const user = userEvent.setup();
  const sent = vi.fn<(init: RequestInit | undefined) => Response>(() => jsonResponse(200, { savedVersion: '8' }));
  const onSaved = vi.fn();
  routes({ extra: {
    [`/api/v1/config/links/${lanPort}`]: { version: '4', value: ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01') },
    '/api/v1/config/segments/seg_lan': { version: '6', value: { id: 'seg_lan', name: 'LAN', link: lanPort, zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } } } },
    'POST /api/v1/config/changes': sent,
  } });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={onSaved} onSessionLost={vi.fn()} />);
  await user.click(await screen.findByRole('button', { name: 'Edit LAN port' }));

  const panel = await screen.findByRole('dialog');
  expect(panel).toHaveTextContent('You manage Andaru through this interface');
  const mtu = within(panel).getByLabelText(/^MTU/);
  await user.clear(mtu);
  await user.type(mtu, '9000');
  await user.click(within(panel).getByRole('button', { name: 'Save interface' }));

  await vi.waitFor(() => expect(onSaved).toHaveBeenCalled());
  expect(JSON.parse(String(sent.mock.calls[0][0]?.body))).toEqual({ operations: [
    { op: 'replace', collection: 'links', id: lanPort, expectedVersion: '4', value: { ...ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'), mtu: 9000 } },
  ] });
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
});

it('offers to add a hardware port the configuration does not have, and no virtual link', async () => {
  routes();
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);
  expect(await screen.findByRole('button', { name: 'Add enp2s0 to the configuration' })).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Edit LAN port' })).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Add docker0 to the configuration' })).not.toBeInTheDocument();
});

it('offers no edits while the saved configuration cannot be read', async () => {
  routes({ extra: { '/api/v1/config': () => jsonResponse(503, { error: { code: 'UNAVAILABLE', message: 'The store is busy.' } }) } });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);
  expect(await screen.findByText('The saved configuration could not be read')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /^(Edit|Add) / })).not.toBeInTheDocument();
});

it('removes an interface and its network after a second, explicit press', async () => {
  const user = userEvent.setup();
  const sent = vi.fn<(init: RequestInit | undefined) => Response>(() => jsonResponse(200, { savedVersion: '8' }));
  routes({ extra: {
    [`/api/v1/config/links/${guestVlan}`]: { version: '2', value: { id: guestVlan, name: 'Guest VLAN', kind: 'vlan', mtu: 1500, enabled: true, parent: lanPort, vlanId: 20 } },
    '/api/v1/config/segments/seg_guest': { version: '3', value: { id: 'seg_guest', name: 'Guest', link: guestVlan, zone: 'zon_guest', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'slaac' } } } },
    'POST /api/v1/config/changes': sent,
  } });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);
  await user.click(await screen.findByRole('button', { name: 'Edit Guest VLAN' }));
  const panel = await screen.findByRole('dialog');
  await user.click(within(panel).getByRole('button', { name: 'Remove…' }));
  expect(sent).not.toHaveBeenCalled();
  await user.click(within(panel).getByRole('button', { name: 'Remove it' }));

  await vi.waitFor(() => expect(sent).toHaveBeenCalled());
  expect(JSON.parse(String(sent.mock.calls[0][0]?.body))).toEqual({ operations: [
    { op: 'delete', collection: 'segments', id: 'seg_guest', expectedVersion: '3' },
    { op: 'delete', collection: 'links', id: guestVlan, expectedVersion: '2' },
  ] });
});

it('shows a refusal beside the field it names and keeps the panel open', async () => {
  const user = userEvent.setup();
  routes({ extra: {
    [`/api/v1/config/links/${lanPort}`]: { version: '4', value: ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01') },
    '/api/v1/config/segments/seg_lan': { version: '6', value: { id: 'seg_lan', name: 'LAN', link: lanPort, zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } } } },
    'POST /api/v1/config/changes': jsonResponse(422, { error: { code: 'VALIDATION_FAILED', message: 'Refused.', details: [{ path: '/operations/0/value/addressing/ipv4/addresses/0', message: 'Not an IPv4 prefix.' }] } }),
  } });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);
  await user.click(await screen.findByRole('button', { name: 'Edit LAN port' }));
  const panel = await screen.findByRole('dialog');
  const addresses = within(panel).getByLabelText('IPv4 addresses', { exact: false });
  await user.clear(addresses);
  await user.type(addresses, 'nonsense');
  await user.click(within(panel).getByRole('button', { name: 'Save interface' }));

  expect(await within(panel).findByText('Not an IPv4 prefix.')).toBeInTheDocument();
  expect(addresses).toHaveAttribute('aria-invalid', 'true');
  expect(within(panel).getByText('Some of the fields below were refused.')).toBeInTheDocument();
});

const lanObjects = {
  [`/api/v1/config/links/${lanPort}`]: { version: '4', value: ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01') },
  '/api/v1/config/segments/seg_lan': { version: '6', value: { id: 'seg_lan', name: 'LAN', link: lanPort, zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } } } },
};

it('keeps the management segment, and says what keeps the management interface from being removed', async () => {
  const user = userEvent.setup();
  const sent = vi.fn<(init: RequestInit | undefined) => Response>(() => jsonResponse(200, { savedVersion: '8' }));
  routes({ extra: { ...lanObjects, 'POST /api/v1/config/changes': sent } });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);
  await user.click(await screen.findByRole('button', { name: 'Edit LAN port' }));
  const panel = await screen.findByRole('dialog');

  expect(within(panel).getByRole('switch', { name: /Carries a segment/ })).toBeDisabled();
  await user.click(within(panel).getByRole('button', { name: 'Remove…' }));
  expect(within(panel).getByText('This interface cannot be removed yet')).toBeInTheDocument();
  expect(within(panel).getByText('You manage Andaru through this interface.')).toBeInTheDocument();
  expect(within(panel).getByText('Guest VLAN is a VLAN on this interface.')).toBeInTheDocument();
  expect(within(panel).queryByRole('button', { name: 'Remove it' })).not.toBeInTheDocument();
  await vi.waitFor(() => expect(within(panel).getByRole('button', { name: 'Cancel' })).toHaveFocus());
  expect(within(panel).getByLabelText(/^Name/)).toBeDisabled();
  expect(sent).not.toHaveBeenCalled();
});

it('warns before a save removes a segment', async () => {
  const user = userEvent.setup();
  routes({ extra: {
    [`/api/v1/config/links/${guestVlan}`]: { version: '2', value: { id: guestVlan, name: 'Guest VLAN', kind: 'vlan', mtu: 1500, enabled: true, parent: lanPort, vlanId: 20 } },
    '/api/v1/config/segments/seg_guest': { version: '3', value: { id: 'seg_guest', name: 'Guest', link: guestVlan, zone: 'zon_guest', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'slaac' } } } },
  } });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);
  await user.click(await screen.findByRole('button', { name: 'Edit Guest VLAN' }));
  const panel = await screen.findByRole('dialog');
  await user.click(within(panel).getByRole('switch', { name: /Carries a segment/ }));

  expect(within(panel).getByRole('alert', { name: 'Saving removes the segment Guest' })).toBeInTheDocument();
});

it('brings the first refused field into focus', async () => {
  const user = userEvent.setup();
  routes({ extra: lanObjects });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);
  await user.click(await screen.findByRole('button', { name: 'Edit LAN port' }));
  const panel = await screen.findByRole('dialog');
  const addresses = within(panel).getByLabelText(/^IPv4 addresses/);
  await user.clear(addresses);
  await user.click(within(panel).getByRole('button', { name: 'Save interface' }));

  expect(addresses).toHaveFocus();
  expect(within(panel).getByText('A static address needs at least one address, such as 192.168.2.1/24.')).toBeInTheDocument();
});

it('reads which segment is the management one when the panel opens, not when the page loaded', async () => {
  const user = userEvent.setup();
  let reads = 0;
  const value = (management: string) => ({ state: 'saved', savedVersion: '7', unusedSecrets: [], value: {
    links: [ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01')],
    segments: [{ id: 'seg_lan', name: 'LAN', link: lanPort, zone: 'zon_lan', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } } }],
    zones: [{ id: 'zon_lan', name: 'Trusted' }],
    management: { segment: management },
  } });
  routes({ extra: { ...lanObjects, '/api/v1/config': () => jsonResponse(200, value(reads++ === 0 ? 'seg_other' : 'seg_lan')) } });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);
  await user.click(await screen.findByRole('button', { name: 'Edit LAN port' }));

  expect(await within(await screen.findByRole('dialog')).findByText('You manage Andaru through this interface')).toBeInTheDocument();
});

it('creates a VLAN with a segment from the Add virtual interface menu', async () => {
  const user = userEvent.setup();
  const sent = vi.fn<(init: RequestInit | undefined) => Response>(() => jsonResponse(200, { savedVersion: '8' }));
  const onSaved = vi.fn();
  routes({ extra: { 'POST /api/v1/config/changes': sent } });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={onSaved} onSessionLost={vi.fn()} />);

  await user.click(await screen.findByRole('button', { name: 'Add virtual interface' }));
  await user.click(await screen.findByRole('menuitem', { name: 'VLAN' }));

  const panel = await screen.findByRole('dialog');
  expect(panel).toHaveTextContent('Add VLAN');
  await user.type(within(panel).getByLabelText(/^Name/), 'IoT VLAN');
  const parentInput = within(panel).getByLabelText(/^Parent interface/, { selector: 'input' });
  await user.click(parentInput);
  await user.click(await screen.findByRole('option', { name: 'LAN port' }));
  await user.type(within(panel).getByLabelText(/^VLAN ID/), '30');
  const zoneInput = within(panel).getByLabelText(/^Zone/, { selector: 'input' });
  await user.click(zoneInput);
  await user.click(await screen.findByRole('option', { name: 'Guests' }));
  await user.selectOptions(within(panel).getByLabelText('IPv4 method'), 'dhcp');
  await user.click(within(panel).getByRole('button', { name: 'Save interface' }));

  await vi.waitFor(() => expect(onSaved).toHaveBeenCalled());
  expect(JSON.parse(String(sent.mock.calls[0][0]?.body))).toEqual({
    operations: [
      { op: 'create', collection: 'links', localId: 'link', value: { name: 'IoT VLAN', kind: 'vlan', parent: lanPort, vlanId: 30, mtu: 1500, enabled: true } },
      { op: 'create', collection: 'segments', value: { name: 'IoT VLAN', link: '@link', zone: 'zon_guest', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } } } },
    ],
  });
});

it('creates a bond and shows bridge/bond details in the table', async () => {
  const user = userEvent.setup();
  const free1 = 'lnk_77777777-7777-4777-8777-777777777777';
  const free2 = 'lnk_88888888-8888-4888-8888-888888888888';
  const brId = 'lnk_99999999-9999-4999-8999-999999999999';
  const sent = vi.fn<(init: RequestInit | undefined) => Response>(() => jsonResponse(200, { savedVersion: '8' }));
  routes({
    links: [
      ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'),
      ethernet(free1, 'Port A', '52:54:00:aa:bb:11'),
      ethernet(free2, 'Port B', '52:54:00:aa:bb:12'),
      { id: brId, name: 'Trunk bridge', kind: 'bridge', mtu: 1500, enabled: true, members: [] },
    ],
    findings: [],
    extra: { 'POST /api/v1/config/changes': sent },
  });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);

  expect(await screen.findByText(/Bridge \(no members\)/)).toBeInTheDocument();

  await user.click(screen.getByRole('button', { name: 'Add virtual interface' }));
  await user.click(await screen.findByRole('menuitem', { name: 'Bond' }));

  const panel = await screen.findByRole('dialog');
  expect(panel).toHaveTextContent('Add bond');
  await user.type(within(panel).getByLabelText(/^Name/), 'Uplink');
  const membersInput = within(panel).getByLabelText(/^Member interfaces/, { selector: 'input' });
  await user.click(membersInput);
  await user.click(await screen.findByRole('option', { name: 'Port A' }));
  await user.click(await screen.findByRole('option', { name: 'Port B' }));
  await user.click(within(panel).getByRole('button', { name: 'Save interface' }));

  await vi.waitFor(() => expect(sent).toHaveBeenCalled());
  expect(JSON.parse(String(sent.mock.calls[0][0]?.body))).toEqual({
    operations: [
      { op: 'create', collection: 'links', localId: 'link', value: { name: 'Uplink', kind: 'bond', mode: '802.3ad', members: [free1, free2], mtu: 1500, enabled: true, miiMonitorMs: 100 } },
    ],
  });
});

it('edits an existing VLAN tag and locks segment assignment on a bridge member', async () => {
  const user = userEvent.setup();
  const free1 = 'lnk_77777777-7777-4777-8777-777777777777';
  const brId = 'lnk_99999999-9999-4999-8999-999999999999';
  const sent = vi.fn<(init: RequestInit | undefined) => Response>(() => jsonResponse(200, { savedVersion: '8' }));
  routes({
    links: [
      ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'),
      { id: guestVlan, name: 'Guest VLAN', kind: 'vlan', mtu: 1500, enabled: true, parent: lanPort, vlanId: 20 },
      ethernet(free1, 'Member port', '52:54:00:aa:bb:11'),
      { id: brId, name: 'LAN bridge', kind: 'bridge', mtu: 1500, enabled: true, members: [free1] },
    ],
    findings: [],
    extra: {
      [`/api/v1/config/links/${guestVlan}`]: { version: '2', value: { id: guestVlan, name: 'Guest VLAN', kind: 'vlan', mtu: 1500, enabled: true, parent: lanPort, vlanId: 20 } },
      '/api/v1/config/segments/seg_guest': { version: '3', value: { id: 'seg_guest', name: 'Guest', link: guestVlan, zone: 'zon_guest', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'slaac' } } } },
      [`/api/v1/config/links/${free1}`]: { version: '5', value: ethernet(free1, 'Member port', '52:54:00:aa:bb:11') },
      'POST /api/v1/config/changes': sent,
    },
  });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);

  expect(await screen.findByText(/Bridge \(Member port\)/)).toBeInTheDocument();

  // Edit Guest VLAN tag.
  await user.click(screen.getByRole('button', { name: 'Edit Guest VLAN' }));
  let panel = await screen.findByRole('dialog');
  const vlanInput = within(panel).getByLabelText(/^VLAN ID/);
  await user.clear(vlanInput);
  await user.type(vlanInput, '40');
  await user.click(within(panel).getByRole('button', { name: 'Save interface' }));
  await vi.waitFor(() => expect(sent).toHaveBeenCalled());
  expect(JSON.parse(String(sent.mock.calls[0][0]?.body))).toEqual({
    operations: [
      { op: 'replace', collection: 'links', id: guestVlan, expectedVersion: '2', value: { id: guestVlan, name: 'Guest VLAN', kind: 'vlan', mtu: 1500, enabled: true, parent: lanPort, vlanId: 40 } },
    ],
  });

  // Member port cannot carry a segment while it belongs to LAN bridge.
  await user.click(screen.getByRole('button', { name: 'Edit Member port' }));
  panel = await screen.findByRole('dialog');
  expect(within(panel).getByRole('switch', { name: /Carries a segment/ })).toBeDisabled();
  expect(panel).toHaveTextContent('this interface is a member of LAN bridge');
});

it('validates duplicate VLAN IDs and parent MTU ceiling, and clamps default MTU for sub-1500 parents', async () => {
  const user = userEvent.setup();
  const smallParent = 'lnk_77777777-7777-4777-8777-777777777777';
  const sent = vi.fn<(init: RequestInit | undefined) => Response>(() => jsonResponse(200, { savedVersion: '8' }));
  routes({
    links: [
      ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'),
      { id: guestVlan, name: 'Guest VLAN', kind: 'vlan', mtu: 1500, enabled: true, parent: lanPort, vlanId: 20 },
      { ...ethernet(smallParent, 'Small port', '52:54:00:aa:bb:11'), mtu: 1400 },
    ],
    findings: [],
    extra: { 'POST /api/v1/config/changes': sent },
  });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);

  await user.click(await screen.findByRole('button', { name: 'Add virtual interface' }));
  await user.click(await screen.findByRole('menuitem', { name: 'VLAN' }));
  const panel = await screen.findByRole('dialog');
  await user.type(within(panel).getByLabelText(/^Name/), 'Dup VLAN');
  await user.click(within(panel).getByRole('switch', { name: /Carries a segment/ }));

  // Selecting sub-1500 parent clamps default 1500 MTU, and switching back to 1500 restores it before manual edits.
  const parentInput = within(panel).getByLabelText(/^Parent interface/, { selector: 'input' });
  await user.click(parentInput);
  await user.click(await screen.findByRole('option', { name: 'Small port' }));
  const mtuInput = within(panel).getByLabelText(/^MTU/);
  expect(mtuInput).toHaveValue('1400');

  await user.click(parentInput);
  await user.click(await screen.findByRole('option', { name: 'LAN port' }));
  expect(mtuInput).toHaveValue('1500');

  // Manually entering 1400 on LAN port, switching to Small port and back preserves the manual 1400 MTU.
  await user.clear(mtuInput);
  await user.type(mtuInput, '1400');
  await user.click(parentInput);
  await user.click(await screen.findByRole('option', { name: 'Small port' }));
  await user.click(parentInput);
  await user.click(await screen.findByRole('option', { name: 'LAN port' }));
  expect(mtuInput).toHaveValue('1400');

  // Raising MTU above parent MTU is rejected client-side.
  await user.click(parentInput);
  await user.click(await screen.findByRole('option', { name: 'Small port' }));
  await user.clear(mtuInput);
  await user.type(mtuInput, '1450');
  await user.type(within(panel).getByLabelText(/^VLAN ID/), '20');
  await user.click(within(panel).getByRole('button', { name: 'Save interface' }));
  expect(await within(panel).findByText('Cannot exceed parent MTU (1400).')).toBeInTheDocument();
  expect(sent).not.toHaveBeenCalled();

  // Duplicate (parent, vlanId) on LAN port is rejected client-side.
  await user.click(parentInput);
  await user.click(await screen.findByRole('option', { name: 'LAN port' }));
  await user.click(within(panel).getByRole('button', { name: 'Save interface' }));
  expect(await within(panel).findByText('VLAN 20 already exists on this parent (Guest VLAN).')).toBeInTheDocument();
  expect(sent).not.toHaveBeenCalled();
});

it('creates a bridge with STP, timers, and L2 options, enforces IEEE 802.1D timer relations, and shows STP in the table', async () => {
  const user = userEvent.setup();
  const free1 = 'lnk_77777777-7777-4777-8777-777777777777';
  const brId = 'lnk_99999999-9999-4999-8999-999999999999';
  const sent = vi.fn<(init: RequestInit | undefined) => Response>(() => jsonResponse(200, { savedVersion: '8' }));
  routes({
    links: [
      ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'),
      ethernet(free1, 'Port A', '52:54:00:aa:bb:11'),
      { id: brId, name: 'Existing bridge', kind: 'bridge', mtu: 1500, enabled: true, members: [free1], stp: true },
    ],
    findings: [],
    extra: { 'POST /api/v1/config/changes': sent },
  });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);

  expect(await screen.findByText(/Bridge \(STP · Port A\)/)).toBeInTheDocument();

  await user.click(screen.getByRole('button', { name: 'Add virtual interface' }));
  await user.click(await screen.findByRole('menuitem', { name: 'Bridge' }));
  const panel = await screen.findByRole('dialog');
  await user.type(within(panel).getByLabelText(/^Name/), 'Loop-safe bridge');
  await user.click(within(panel).getByRole('switch', { name: /Carries a segment/ }));
  await user.click(within(panel).getByRole('switch', { name: /Spanning Tree Protocol \(STP\)/ }));
  await user.type(within(panel).getByLabelText(/^STP priority/), '4096');
  const fwdInput = within(panel).getByLabelText(/^Forward delay/);
  await user.type(fwdInput, '10');
  await user.click(within(panel).getByRole('button', { name: 'Save interface' }));
  expect(await within(panel).findByText(/2 × \(forward delay − 1\) ≥ max age/)).toBeInTheDocument();
  expect(sent).not.toHaveBeenCalled();

  await user.clear(fwdInput);
  await user.type(fwdInput, '12');
  await user.click(within(panel).getByRole('switch', { name: /IGMP\/MLD multicast snooping/ }));
  await user.click(within(panel).getByRole('button', { name: 'Save interface' }));

  await vi.waitFor(() => expect(sent).toHaveBeenCalled());
  expect(JSON.parse(String(sent.mock.calls[0][0]?.body))).toEqual({
    operations: [
      {
        op: 'create',
        collection: 'links',
        localId: 'link',
        value: {
          name: 'Loop-safe bridge',
          kind: 'bridge',
          members: [],
          mtu: 1500,
          enabled: true,
          stp: true,
          stpPriority: 4096,
          forwardDelay: 12,
          multicastSnooping: false,
        },
      },
    ],
  });
});

it('creates an 802.3ad bond with LACP rate, hash policy, and MII monitor interval', async () => {
  const user = userEvent.setup();
  const free1 = 'lnk_77777777-7777-4777-8777-777777777777';
  const sent = vi.fn<(init: RequestInit | undefined) => Response>(() => jsonResponse(200, { savedVersion: '8' }));
  routes({
    links: [
      ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'),
      ethernet(free1, 'Port A', '52:54:00:aa:bb:11'),
    ],
    findings: [],
    extra: { 'POST /api/v1/config/changes': sent },
  });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);

  await user.click(await screen.findByRole('button', { name: 'Add virtual interface' }));
  await user.click(await screen.findByRole('menuitem', { name: 'Bond' }));
  const panel = await screen.findByRole('dialog');
  await user.type(within(panel).getByLabelText(/^Name/), 'LACP Uplink');
  const membersInput = within(panel).getByLabelText(/^Member interfaces/, { selector: 'input' });
  await user.click(membersInput);
  await user.click(await screen.findByRole('option', { name: 'Port A' }));
  await user.selectOptions(within(panel).getByLabelText(/^LACP rate/), 'fast');
  await user.selectOptions(within(panel).getByLabelText(/^Transmit hash policy/), 'layer3+4');
  const miiInput = within(panel).getByLabelText(/^MII monitor interval/);
  expect(miiInput).toHaveValue('100');
  await user.clear(miiInput);
  await user.type(miiInput, '250');
  await user.click(within(panel).getByRole('button', { name: 'Save interface' }));

  await vi.waitFor(() => expect(sent).toHaveBeenCalled());
  expect(JSON.parse(String(sent.mock.calls[0][0]?.body))).toEqual({
    operations: [
      {
        op: 'create',
        collection: 'links',
        localId: 'link',
        value: {
          name: 'LACP Uplink',
          kind: 'bond',
          mode: '802.3ad',
          members: [free1],
          mtu: 1500,
          enabled: true,
          lacpRate: 'fast',
          transmitHashPolicy: 'layer3+4',
          miiMonitorMs: 250,
        },
      },
    ],
  });
});

it('configures per-link LLDP options and displays live LLDP neighbors on the LLDP tab', async () => {
  const user = userEvent.setup();
  const sent = vi.fn<(init: RequestInit | undefined) => Response>(() => jsonResponse(200, { savedVersion: '8' }));
  routes({
    links: [
      { ...ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'), lldp: 'receive', emitLldp: 'nearest-bridge' },
      { id: guestVlan, name: 'Guest VLAN', kind: 'vlan', mtu: 1500, enabled: true, parent: lanPort, vlanId: 20 },
    ],
    findings: [],
    extra: {
      [`/api/v1/config/links/${lanPort}`]: { version: '4', value: { ...ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'), lldp: 'receive', emitLldp: 'nearest-bridge' } },
      '/api/v1/config/segments/seg_lan': { version: '6', value: { id: 'seg_lan', name: 'LAN', link: lanPort, zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } } } },
      '/api/v1/status/lldp': {
        items: [{
          ifIndex: 2,
          interface: 'enp1s0',
          chassisId: 'aa:bb:cc:11:22:33',
          portId: 'gi1/0/24',
          portDescription: 'Uplink to router',
          systemName: 'core-sw-1',
          systemDescription: 'Managed L2/L3 Switch',
          capabilities: ['bridge', 'router'],
        }],
      },
      'POST /api/v1/config/changes': sent,
    },
  });
  render(<InterfacesScreen api={new ApiClient()} session={writer} onSaved={vi.fn()} onSessionLost={vi.fn()} />);

  expect(await screen.findByText(/LLDP rx\/tx/)).toBeInTheDocument();

  // Edit LLDP settings on LAN port.
  await user.click(screen.getByRole('button', { name: 'Edit LAN port' }));
  const panel = await screen.findByRole('dialog');
  await user.selectOptions(within(panel).getByLabelText(/^LLDP reception/), 'routers-only');
  await user.selectOptions(within(panel).getByLabelText(/^LLDP emission/), 'off');
  await user.click(within(panel).getByRole('button', { name: 'Save interface' }));
  await vi.waitFor(() => expect(sent).toHaveBeenCalled());
  expect(JSON.parse(String(sent.mock.calls[0][0]?.body))).toEqual({
    operations: [
      { op: 'replace', collection: 'links', id: lanPort, expectedVersion: '4', value: { ...ethernet(lanPort, 'LAN port', '52:54:00:aa:bb:01'), lldp: 'routers-only' } },
    ],
  });

  // Switch to LLDP neighbors tab and verify discovered neighbor details.
  await user.click(screen.getByRole('tab', { name: 'LLDP neighbors' }));
  expect(await screen.findByText('core-sw-1')).toBeInTheDocument();
  expect(screen.getByText('Managed L2/L3 Switch')).toBeInTheDocument();
  expect(screen.getByText('aa:bb:cc:11:22:33')).toBeInTheDocument();
  expect(screen.getByText('gi1/0/24')).toBeInTheDocument();
  expect(screen.getByText('Uplink to router')).toBeInTheDocument();
  expect(screen.getByText('Bridge')).toBeInTheDocument();
  expect(screen.getByText('Router')).toBeInTheDocument();
});
