import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen, waitFor, within } from '../test/render';
import { AliasesScreen } from './AliasesScreen';

const addresses = [
  { id: 'adr_nas', name: 'nas', kind: 'host', address: '192.168.1.10', description: 'The file server' },
  { id: 'adr_lab', name: 'lab', kind: 'network', prefix: '10.9.0.0/24' },
  { id: 'adr_pool', name: 'pool', kind: 'range', first: '192.168.1.50', last: '192.168.1.99' },
  { id: 'adr_servers', name: 'servers', kind: 'group', members: ['adr_nas', 'adr_lab'] },
];
const ports = [
  { id: 'svc_web', name: 'web', protocol: 'tcp', ports: ['80', '443'] },
  { id: 'svc_ping', name: 'ping', protocol: 'icmp', types: ['echo-request'] },
  { id: 'svc_any6', name: 'any ICMPv6', protocol: 'icmpv6' },
  { id: 'svc_usual', name: 'usual', protocol: 'group', members: ['svc_web', 'svc_ping'] },
];
const side = (zones: string[] = [], list: string[] = []) => ({ zones, addresses: list });
const rules = [
  { id: 'rul_a', name: 'Web to the NAS', enabled: true, action: 'accept', from: side(), to: side([], ['adr_nas']), ports: ['svc_web'], log: false },
  { id: 'rul_b', name: 'Lab out', enabled: true, action: 'accept', from: side([], ['adr_lab']), to: side(), ports: [], log: false },
];
const list = (items: unknown[]) => ({ state: 'saved', savedVersion: '9', items: items.map((value) => ({ value, version: '4' })) });
const config = { state: 'saved', savedVersion: '9', unusedSecrets: [], value: {
  links: [], segments: [], zones: [], management: { segment: 'seg_lan', allowedSources: ['adr_pool'] },
  nat: { portForwards: [{ id: 'pfw_a', name: 'web in', enabled: true, from: ['adr_lab'], to: { address: 'adr_nas' } }], oneToOne: [{ id: 'oto_a', name: 'mail', destination: ['adr_lab'] }] },
} };

function show(routes: Parameters<typeof fetchRoutes>[0] = {}, capabilities = ['configuration.read', 'configuration.write']) {
  const onSaved = vi.fn();
  const fetch = fetchRoutes({
    '/api/v1/config/aliases/addresses': list(addresses), '/api/v1/config/aliases/ports': list(ports),
    '/api/v1/config/rules': { ...list(rules), version: '3' }, '/api/v1/config': config, ...routes,
  });
  render(<AliasesScreen api={new ApiClient('csrf')} session={session(capabilities)} onSaved={onSaved} onSessionLost={vi.fn()} />);
  return { fetch, onSaved };
}
const rows = () => screen.getAllByRole('row').slice(1);
const cells = (name: string) => within(screen.getByText(name, { selector: 'p' }).closest('tr') as HTMLElement).getAllByRole('cell');
const sent = (fetch: ReturnType<typeof fetchRoutes>, method: string) => fetch.mock.calls.filter(([, init]) => init?.method === method);

it('lists address and port aliases in one table by name, with what each holds', async () => {
  show();
  await screen.findByText('nas');
  expect(rows().map((row) => within(row).getAllByRole('cell')[0].textContent)).toEqual(['any ICMPv6', 'lab', 'nasThe file server', 'ping', 'pool', 'servers', 'usual', 'web']);
  expect(cells('nas').slice(1, 3).map((cell) => cell.textContent)).toEqual(['address: host', '192.168.1.10']);
  expect(cells('lab')[2]).toHaveTextContent('10.9.0.0/24');
  expect(cells('pool')[2]).toHaveTextContent('192.168.1.50 to 192.168.1.99');
  expect(cells('servers').slice(1, 3).map((cell) => cell.textContent)).toEqual(['address: group', 'nas, lab']);
  expect(cells('web').slice(1, 3).map((cell) => cell.textContent)).toEqual(['port', 'TCP 80, 443']);
  expect(cells('ping')[2]).toHaveTextContent('ICMP echo-request');
  expect(cells('any ICMPv6')[2]).toHaveTextContent('ICMPv6 any type');
  expect(cells('usual').slice(1, 3).map((cell) => cell.textContent)).toEqual(['port: group', 'web, ping']);
});

it('says where each alias is used: rules by their place, groups, the management sources and NAT', async () => {
  show();
  await screen.findByText('nas');
  expect(cells('nas')[3]).toHaveTextContent('3 uses');
  expect(within(cells('nas')[3]).getAllByRole('listitem', { hidden: true }).map((item) => item.textContent)).toEqual(['Rule 1, Web to the NAS', 'Alias servers', 'Port forward web in']);
  expect(within(cells('lab')[3]).getAllByRole('listitem', { hidden: true }).map((item) => item.textContent)).toEqual(['Rule 2, Lab out', 'Alias servers', 'Port forward web in', 'One-to-one NAT mail']);
  expect(cells('pool')[3]).toHaveTextContent('1 use');
  expect(cells('web')[3]).toHaveTextContent('2 uses');
  expect(cells('usual')[3]).toHaveTextContent('nothing');
});

it('narrows the table to one kind', async () => {
  const user = userEvent.setup();
  show();
  await screen.findByText('nas');
  await user.click(screen.getByRole('radio', { name: 'Ports' }));
  expect(rows().map((row) => within(row).getAllByRole('cell')[0].textContent)).toEqual(['any ICMPv6', 'ping', 'usual', 'web']);
  await user.click(screen.getByRole('radio', { name: 'Addresses' }));
  expect(rows()).toHaveLength(4);
});

it('gives an account that may not write no controls, and says so when nothing is saved', async () => {
  show({ '/api/v1/config/aliases/addresses': list([]), '/api/v1/config/aliases/ports': list([]) }, ['configuration.read']);
  expect(await screen.findByText('No aliases')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /alias/ })).not.toBeInTheDocument();
});

it('does not offer to delete an alias that is still named, and says by what', async () => {
  const user = userEvent.setup();
  const { fetch } = show();
  await user.click(await screen.findByRole('button', { name: 'Delete pool' }));
  const dialog = await screen.findByRole('dialog', { name: 'Delete alias' });
  expect(dialog).toHaveTextContent("The management segment's allowed sources");
  expect(within(dialog).queryByRole('button', { name: 'Delete alias' })).not.toBeInTheDocument();
  await user.click(within(dialog).getByRole('button', { name: 'Keep it' }));
  expect(sent(fetch, 'DELETE')).toHaveLength(0);
});

it('deletes an alias nothing names, against its version, after asking', async () => {
  const user = userEvent.setup();
  const { fetch, onSaved } = show({ 'DELETE /api/v1/config/aliases/ports/svc_usual': new Response(null, { status: 204 }) });
  await user.click(await screen.findByRole('button', { name: 'Delete usual' }));
  const dialog = await screen.findByRole('dialog', { name: 'Delete alias' });
  await user.click(within(dialog).getByRole('button', { name: 'Delete alias' }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  const [[path, init]] = sent(fetch, 'DELETE');
  expect(path).toBe('/api/v1/config/aliases/ports/svc_usual');
  expect(new Headers(init?.headers).get('If-Match')).toBe('"svc_usual.4"');
});

it('adds an address alias of each kind with only that kind\'s fields, and shows a refusal beside its field', async () => {
  const user = userEvent.setup();
  let refuse = true;
  const { fetch, onSaved } = show({ 'POST /api/v1/config/aliases/addresses': () => {
    if (!refuse) return jsonResponse(201, { value: { id: 'adr_new', name: 'guests', kind: 'network', prefix: '10.20.0.0/24' }, version: '10' });
    refuse = false;
    return jsonResponse(422, { error: { code: 'VALIDATION_FAILED', message: 'Not valid.', details: [{ path: '/prefix', message: 'Expected a network prefix with its host bits zero.' }] } });
  } });
  await user.click(await screen.findByRole('button', { name: 'Add address alias' }));
  const panel = await screen.findByRole('dialog', { name: 'Add address alias' });
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  expect(within(panel).getByText('An alias needs a name.')).toBeInTheDocument();
  await user.type(within(panel).getByLabelText(/^Name/), 'guests');
  await user.selectOptions(within(panel).getByLabelText('Kind'), 'A network');
  await user.type(within(panel).getByLabelText('Network'), '10.20.0.1/24');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  expect(await within(panel).findByText('Expected a network prefix with its host bits zero.')).toBeInTheDocument();
  expect(within(panel).getByLabelText('Network')).toHaveAccessibleDescription(expect.stringContaining('host bits zero'));
  expect(JSON.parse(String(sent(fetch, 'POST')[0][1]?.body))).toEqual({ name: 'guests', kind: 'network', prefix: '10.20.0.1/24' });
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
});

it('adds a port alias from ports typed with commas and spaces', async () => {
  const user = userEvent.setup();
  const { fetch } = show({ 'POST /api/v1/config/aliases/ports': jsonResponse(201, { value: { id: 'svc_new', name: 'mail', protocol: 'tcp', ports: ['25'] }, version: '10' }) });
  await user.click(await screen.findByRole('button', { name: 'Add port alias' }));
  const panel = await screen.findByRole('dialog', { name: 'Add port alias' });
  await user.type(within(panel).getByLabelText(/^Name/), 'mail');
  await user.type(within(panel).getByLabelText('Ports'), '25, 465  587,8000-8100');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(sent(fetch, 'POST')).toHaveLength(1));
  expect(JSON.parse(String(sent(fetch, 'POST')[0][1]?.body))).toEqual({ name: 'mail', protocol: 'tcp', ports: ['25', '465', '587', '8000-8100'] });
});

it('opens a saved alias with its fields and saves it whole against its version', async () => {
  const user = userEvent.setup();
  const { fetch } = show({ 'PUT /api/v1/config/aliases/addresses/adr_pool': { value: addresses[2], version: '10' }, 'PUT /api/v1/config/aliases/ports/svc_usual': { value: ports[3], version: '10' } });
  await user.click(await screen.findByRole('button', { name: 'Edit pool' }));
  let panel = await screen.findByRole('dialog', { name: 'Edit pool' });
  await user.clear(within(panel).getByLabelText('Last address'));
  await user.type(within(panel).getByLabelText('Last address'), '192.168.1.80');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(sent(fetch, 'PUT')).toHaveLength(1));
  expect(new Headers(sent(fetch, 'PUT')[0][1]?.headers).get('If-Match')).toBe('"adr_pool.4"');
  expect(JSON.parse(String(sent(fetch, 'PUT')[0][1]?.body))).toEqual({ name: 'pool', kind: 'range', first: '192.168.1.50', last: '192.168.1.80' });
  // A group keeps its members when nothing about them is touched.
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Edit pool' })).not.toBeInTheDocument());
  await user.click(await screen.findByRole('button', { name: 'Edit usual' }));
  panel = await screen.findByRole('dialog', { name: 'Edit usual' });
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(sent(fetch, 'PUT')).toHaveLength(2));
  expect(JSON.parse(String(sent(fetch, 'PUT')[1][1]?.body))).toEqual({ name: 'usual', protocol: 'group', members: ['svc_web', 'svc_ping'] });
});
