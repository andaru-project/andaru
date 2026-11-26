import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
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
const config = { state: 'saved', savedVersion: '9', value: {
  rules, management: { segment: 'seg_lan', allowedSources: ['adr_pool'] },
  nat: { portForwards: [{ id: 'pfw_a', name: 'web in', enabled: true, from: ['adr_lab'], to: { address: 'adr_nas' } }], oneToOne: [{ id: 'oto_a', name: 'mail', destination: ['adr_lab'] }] },
} };

function show(routes: Parameters<typeof fetchRoutes>[0] = {}, capabilities = ['configuration.read', 'configuration.write']) {
  const onSaved = vi.fn();
  const fetch = fetchRoutes({
    '/api/v1/config/aliases/addresses': list(addresses), '/api/v1/config/aliases/ports': list(ports),
    '/api/v1/config': config, ...routes,
  });
  render(<MemoryRouter><AliasesScreen api={new ApiClient('csrf')} session={session(capabilities)} onSaved={onSaved} onSessionLost={vi.fn()} /></MemoryRouter>);
  return { fetch, onSaved };
}
const rows = () => screen.getAllByRole('row').slice(1);
const cells = (name: string) => within(screen.getByText(name, { selector: 'p' }).closest('tr') as HTMLElement).getAllByRole('cell');
const sent = (fetch: ReturnType<typeof fetchRoutes>, method: string) => fetch.mock.calls.filter(([, init]) => init?.method === method);

it('lists address and port aliases in one table by name, with what each holds', async () => {
  show();
  await screen.findByText('nas');
  expect(rows().map((row) => within(row).getAllByRole('cell')[0].textContent)).toEqual(['any ICMPv6', 'lab', 'nasThe file server', 'ping', 'pool', 'servers', 'usual', 'web']);
  expect(cells('nas').slice(1, 3).map((cell) => cell.textContent)).toEqual(['Host', '192.168.1.10']);
  expect(cells('lab')[2]).toHaveTextContent('10.9.0.0/24');
  expect(cells('pool')[2]).toHaveTextContent('192.168.1.50 to 192.168.1.99');
  expect(cells('servers').slice(1, 3).map((cell) => cell.textContent)).toEqual(['Address group', 'nas, lab']);
  expect(cells('web').slice(1, 3).map((cell) => cell.textContent)).toEqual(['Port', 'TCP 80, 443']);
  expect(cells('ping')[2]).toHaveTextContent('ICMP echo-request');
  expect(cells('any ICMPv6')[2]).toHaveTextContent('ICMPv6 any type');
  expect(cells('usual').slice(1, 3).map((cell) => cell.textContent)).toEqual(['Port group', 'web, ping']);
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

it('gives an account that may not write no controls', async () => {
  show({}, ['configuration.read']);
  await screen.findByText('nas');
  expect(screen.queryByRole('button')).not.toBeInTheDocument();
  expect(screen.queryByRole('columnheader', { name: 'Actions' })).not.toBeInTheDocument();
});

it('says so when nothing is saved', async () => {
  show({ '/api/v1/config/aliases/addresses': list([]), '/api/v1/config/aliases/ports': list([]) });
  expect(await screen.findByText('No aliases')).toBeInTheDocument();
});

it('does not offer to delete an alias that is still named, and says by what', async () => {
  const user = userEvent.setup();
  const { fetch } = show();
  await user.click(await screen.findByRole('button', { name: 'Delete address alias pool' }));
  const dialog = await screen.findByRole('dialog', { name: 'Delete alias' });
  expect(dialog).toHaveTextContent("The management segment's allowed sources");
  expect(within(dialog).queryByRole('button', { name: 'Delete alias' })).not.toBeInTheDocument();
  await user.click(within(dialog).getByRole('button', { name: 'Keep it' }));
  expect(sent(fetch, 'DELETE')).toHaveLength(0);
});

it('deletes an alias nothing names, against its version, after asking', async () => {
  const user = userEvent.setup();
  const { fetch, onSaved } = show({ 'DELETE /api/v1/config/aliases/ports/svc_usual': { state: 'saved', savedVersion: '10' } });
  await user.click(await screen.findByRole('button', { name: 'Delete port alias usual' }));
  const dialog = await screen.findByRole('dialog', { name: 'Delete alias' });
  await user.click(within(dialog).getByRole('button', { name: 'Delete alias' }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  const [[path, init]] = sent(fetch, 'DELETE');
  expect(path).toBe('/api/v1/config/aliases/ports/svc_usual');
  expect(new Headers(init?.headers).get('If-Match')).toBe('"svc_usual.4"');
});

it('adds a network alias with only its kind\'s fields, and shows a refusal beside its field', async () => {
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
  await user.type(within(panel).getByLabelText('Ports'), '25, 465  587,8000 - 8100,');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(sent(fetch, 'POST')).toHaveLength(1));
  expect(JSON.parse(String(sent(fetch, 'POST')[0][1]?.body))).toEqual({ name: 'mail', protocol: 'tcp', ports: ['25', '465', '587', '8000-8100'] });
});

it('opens a saved alias with its fields and saves it whole against its version', async () => {
  const user = userEvent.setup();
  const { fetch } = show({ 'PUT /api/v1/config/aliases/addresses/adr_pool': { value: addresses[2], version: '10' }, 'PUT /api/v1/config/aliases/ports/svc_usual': { value: ports[3], version: '10' } });
  await user.click(await screen.findByRole('button', { name: 'Edit address alias pool' }));
  let panel = await screen.findByRole('dialog', { name: 'Edit address alias: pool' });
  await user.clear(within(panel).getByLabelText('Last address'));
  await user.type(within(panel).getByLabelText('Last address'), '192.168.1.80');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(sent(fetch, 'PUT')).toHaveLength(1));
  expect(new Headers(sent(fetch, 'PUT')[0][1]?.headers).get('If-Match')).toBe('"adr_pool.4"');
  expect(JSON.parse(String(sent(fetch, 'PUT')[0][1]?.body))).toEqual({ name: 'pool', kind: 'range', first: '192.168.1.50', last: '192.168.1.80' });
  // A group keeps its members when nothing about them is touched.
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Edit address alias: pool' })).not.toBeInTheDocument());
  await user.click(await screen.findByRole('button', { name: 'Edit port alias usual' }));
  panel = await screen.findByRole('dialog', { name: 'Edit port alias: usual' });
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(sent(fetch, 'PUT')).toHaveLength(2));
  expect(JSON.parse(String(sent(fetch, 'PUT')[1][1]?.body))).toEqual({ name: 'usual', protocol: 'group', members: ['svc_web', 'svc_ping'] });
});

it('refuses a name another alias of the kind has before asking the engine, and shows an added alias whatever kind the table showed', async () => {
  const user = userEvent.setup();
  const { fetch } = show({ 'POST /api/v1/config/aliases/ports': jsonResponse(201, { value: { id: 'svc_new', name: 'nas', protocol: 'udp', ports: ['2049'] }, version: '10' }) });
  await screen.findByText('nas');
  await user.click(screen.getByRole('radio', { name: 'Addresses' }));
  await user.click(screen.getByRole('button', { name: 'Add port alias' }));
  const panel = await screen.findByRole('dialog', { name: 'Add port alias' });
  await user.type(within(panel).getByLabelText(/^Name/), 'web');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  expect(within(panel).getByText('Another port alias has this name.')).toBeInTheDocument();
  expect(sent(fetch, 'POST')).toHaveLength(0);
  // An address alias may have the name: the two kinds are named apart.
  await user.clear(within(panel).getByLabelText(/^Name/));
  await user.type(within(panel).getByLabelText(/^Name/), 'nas');
  await user.type(within(panel).getByLabelText('Ports'), '2049');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(sent(fetch, 'POST')).toHaveLength(1));
  await waitFor(() => expect(screen.getByRole('radio', { name: 'All' })).toBeChecked());
});

it('says the alias changed underneath when its save is refused for that, and opens it next on what is saved', async () => {
  const user = userEvent.setup();
  let version = '4';
  const { fetch } = show({
    '/api/v1/config/aliases/ports': () => jsonResponse(200, { state: 'saved', savedVersion: '9', items: ports.map((value) => ({ value, version })) }),
    'PUT /api/v1/config/aliases/ports/svc_web': () => {
      if (version !== '4') return jsonResponse(200, { value: ports[0], version: '6' });
      version = '5';
      return jsonResponse(412, { error: { code: 'PRECONDITION_FAILED', message: 'The object changed.' } });
    },
  });
  await user.click(await screen.findByRole('button', { name: 'Edit port alias web' }));
  let panel = await screen.findByRole('dialog', { name: 'Edit port alias: web' });
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  expect(await within(panel).findByText(/This port alias was changed by someone else/)).toBeInTheDocument();
  await user.click(within(panel).getByRole('button', { name: 'Cancel' }));
  await user.click(await screen.findByRole('button', { name: 'Edit port alias web' }));
  panel = await screen.findByRole('dialog', { name: 'Edit port alias: web' });
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(sent(fetch, 'PUT')).toHaveLength(2));
  expect(new Headers(sent(fetch, 'PUT')[1][1]?.headers).get('If-Match')).toBe('"svc_web.5"');
});

it('sends each kind of alias with that kind\'s fields alone, whatever the form held before', async () => {
  const user = userEvent.setup();
  const bodies: unknown[] = [];
  show({
    'POST /api/v1/config/aliases/addresses': (init) => { bodies.push(JSON.parse(String(init?.body))); return jsonResponse(201, { value: addresses[0], version: '10' }); },
    'POST /api/v1/config/aliases/ports': (init) => { bodies.push(JSON.parse(String(init?.body))); return jsonResponse(201, { value: ports[0], version: '10' }); },
  });
  const add = async (kind: 'address' | 'port', fill: (panel: HTMLElement) => Promise<void>) => {
    await user.click(await screen.findByRole('button', { name: `Add ${kind} alias` }));
    const panel = await screen.findByRole('dialog', { name: `Add ${kind} alias` });
    await fill(panel);
    await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: `Add ${kind} alias` })).not.toBeInTheDocument());
  };
  // A range typed first, then the kind changed to one address: the last address does not travel.
  await add('address', async (panel) => {
    await user.type(within(panel).getByLabelText(/^Name/), '  one host ');
    await user.type(within(panel).getByLabelText('Description'), 'Why');
    await user.selectOptions(within(panel).getByLabelText('Kind'), 'A range of addresses');
    await user.type(within(panel).getByLabelText('First address'), '10.0.0.1');
    await user.type(within(panel).getByLabelText('Last address'), '10.0.0.9');
    await user.selectOptions(within(panel).getByLabelText('Kind'), 'One address');
  });
  await add('address', async (panel) => {
    await user.type(within(panel).getByLabelText(/^Name/), 'a range');
    await user.selectOptions(within(panel).getByLabelText('Kind'), 'A range of addresses');
    await user.type(within(panel).getByLabelText('First address'), '10.0.0.1');
    await user.type(within(panel).getByLabelText('Last address'), '10.0.0.9');
  });
  await add('port', async (panel) => {
    await user.type(within(panel).getByLabelText(/^Name/), 'any ping');
    await user.type(within(panel).getByLabelText('Ports'), '80');
    await user.selectOptions(within(panel).getByLabelText('Protocol'), 'ICMP (IPv4)');
  });
  expect(bodies).toEqual([
    { name: 'one host', description: 'Why', kind: 'host', address: '10.0.0.1' },
    { name: 'a range', kind: 'range', first: '10.0.0.1', last: '10.0.0.9' },
    { name: 'any ping', protocol: 'icmp' },
  ]);
});

it('offers a group the aliases of its kind that are not groups, and never itself', async () => {
  const user = userEvent.setup();
  show();
  await user.click(await screen.findByRole('button', { name: 'Edit address alias servers' }));
  const panel = await screen.findByRole('dialog', { name: 'Edit address alias: servers' });
  const members = within(panel).getByLabelText('Members', { selector: 'input' });
  await user.click(members);
  expect(within(document.getElementById(members.getAttribute('aria-controls')!)!).getAllByRole('option', { hidden: true }).map((option) => option.textContent)).toEqual(['lab', 'nas', 'pool']);
});

it('does not ask the engine to make a group of an alias that is in one', async () => {
  const user = userEvent.setup();
  const { fetch } = show();
  await user.click(await screen.findByRole('button', { name: 'Edit address alias nas' }));
  const panel = await screen.findByRole('dialog', { name: 'Edit address alias: nas' });
  await user.selectOptions(within(panel).getByLabelText('Kind'), 'A group of address aliases');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  expect(within(panel).getByText('This alias is in the group servers, and a group cannot hold a group.')).toBeInTheDocument();
  expect(sent(fetch, 'PUT')).toHaveLength(0);
  // Changing the kind again takes the refusal with it.
  await user.selectOptions(within(panel).getByLabelText('Kind'), 'One address');
  expect(within(panel).queryByText(/cannot hold a group/)).not.toBeInTheDocument();
});

it('reads the aliases again when the panel refuses a save from what it holds, so a stale list does not block for good', async () => {
  const user = userEvent.setup();
  let renamed = false;
  const { fetch } = show({
    '/api/v1/config/aliases/ports': () => jsonResponse(200, list(renamed ? ports.map((port) => (port.id === 'svc_web' ? { ...port, name: 'www' } : port)) : ports)),
    'POST /api/v1/config/aliases/ports': jsonResponse(201, { value: { id: 'svc_new', name: 'web', protocol: 'udp', ports: ['80'] }, version: '10' }),
  });
  await user.click(await screen.findByRole('button', { name: 'Add port alias' }));
  const panel = await screen.findByRole('dialog', { name: 'Add port alias' });
  await user.type(within(panel).getByLabelText(/^Name/), 'web');
  await user.type(within(panel).getByLabelText('Ports'), '80');
  // Someone else renamed the alias that held the name; this page has not heard.
  renamed = true;
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  expect(within(panel).getByText('Another port alias has this name.')).toBeInTheDocument();
  expect(await screen.findByText('www')).toBeInTheDocument();
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(sent(fetch, 'POST')).toHaveLength(1));
});

it('says what happened when a save or a delete is refused, and reads the list again', async () => {
  const user = userEvent.setup();
  const onSessionLost = vi.fn();
  const refuse = (status: number, code: string) => jsonResponse(status, { error: { code, message: 'Refused.' } });
  const fetch = fetchRoutes({
    '/api/v1/config/aliases/addresses': list(addresses), '/api/v1/config/aliases/ports': list(ports), '/api/v1/config': config,
    'PUT /api/v1/config/aliases/addresses/adr_pool': refuse(404, 'NOT_FOUND'),
    'PUT /api/v1/config/aliases/ports/svc_web': refuse(401, 'UNAUTHENTICATED'),
    'DELETE /api/v1/config/aliases/ports/svc_usual': refuse(422, 'VALIDATION_FAILED'),
    'DELETE /api/v1/config/aliases/ports/svc_any6': refuse(412, 'PRECONDITION_FAILED'),
  });
  render(<MemoryRouter><AliasesScreen api={new ApiClient('csrf')} session={session(['configuration.read', 'configuration.write'])} onSaved={vi.fn()} onSessionLost={onSessionLost} /></MemoryRouter>);
  const reads = () => fetch.mock.calls.filter(([path, init]) => path === '/api/v1/config' && !init?.method).length;
  const remove = async (name: string) => {
    await user.click(await screen.findByRole('button', { name: `Delete port alias ${name}` }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Delete alias' })).getByRole('button', { name: 'Delete alias' }));
  };
  await remove('usual');
  expect(await screen.findByText(/Something began to name this alias/)).toBeInTheDocument();
  await waitFor(() => expect(reads()).toBe(2));
  await remove('any ICMPv6');
  expect(await screen.findByText(/changed by someone else. The list now shows what is saved; nothing was deleted/)).toBeInTheDocument();
  await waitFor(() => expect(reads()).toBe(3));

  await user.click(await screen.findByRole('button', { name: 'Edit address alias pool' }));
  let panel = await screen.findByRole('dialog', { name: 'Edit address alias: pool' });
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  expect(await within(panel).findByText(/This address alias was deleted by someone else/)).toBeInTheDocument();
  await waitFor(() => expect(reads()).toBe(4));
  await user.click(within(panel).getByRole('button', { name: 'Cancel' }));

  await user.click(await screen.findByRole('button', { name: 'Edit port alias web' }));
  panel = await screen.findByRole('dialog', { name: 'Edit port alias: web' });
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
  expect(reads()).toBe(4);
});

it('shows the table again as saved after a save', async () => {
  const user = userEvent.setup();
  let saved = false;
  show({
    '/api/v1/config/aliases/ports': () => jsonResponse(200, list(saved ? [...ports, { id: 'svc_new', name: 'mail', protocol: 'tcp', ports: ['25'] }] : ports)),
    'POST /api/v1/config/aliases/ports': () => { saved = true; return jsonResponse(201, { value: { id: 'svc_new', name: 'mail', protocol: 'tcp', ports: ['25'] }, version: '10' }); },
  });
  await user.click(await screen.findByRole('button', { name: 'Add port alias' }));
  const panel = await screen.findByRole('dialog', { name: 'Add port alias' });
  await user.type(within(panel).getByLabelText(/^Name/), 'mail');
  await user.type(within(panel).getByLabelText('Ports'), '25');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  expect(await screen.findByText('TCP 25')).toBeInTheDocument();
});

it('says on the page what became of a save whose panel was closed before it answered', async () => {
  const user = userEvent.setup();
  let answer: (response: Response) => void = () => {};
  show({ 'POST /api/v1/config/aliases/ports': () => new Promise<Response>((resolve) => { answer = resolve; }) });
  await user.click(await screen.findByRole('button', { name: 'Add port alias' }));
  const panel = await screen.findByRole('dialog', { name: 'Add port alias' });
  await user.type(within(panel).getByLabelText(/^Name/), 'mail');
  await user.type(within(panel).getByLabelText('Ports'), '25');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await user.click(within(panel).getByRole('button', { name: 'Cancel' }));
  // Another panel is open when the first save is refused: it stays, and the page says what happened.
  await user.click(await screen.findByRole('button', { name: 'Add address alias' }));
  answer(jsonResponse(503, { error: { code: 'UNAVAILABLE', message: 'The store is busy.' } }));
  expect(await screen.findByText('The store is busy.')).toBeInTheDocument();
  expect(screen.getByRole('dialog', { name: 'Add address alias' })).toBeInTheDocument();
});

it('filters by every word of what a row shows, beside the kind', async () => {
  const user = userEvent.setup();
  show();
  await screen.findByText('nas');
  const filter = screen.getByLabelText('Filter aliases');
  for (const [text, names] of [['192.168', ['nasThe file server', 'pool']], ['tcp 443', ['web']], ['address group', ['servers']], ['file server', ['nasThe file server']]] as const) {
    await user.clear(filter);
    await user.type(filter, text);
    expect(rows().map((row) => within(row).getAllByRole('cell')[0].textContent)).toEqual(names);
  }
  await user.clear(filter);
  await user.type(filter, 'nothing like it');
  expect(screen.getByText('No alias matches the filter.')).toBeInTheDocument();
});

it('displays URL list (with HTTP warning) and host-name aliases, and excludes them from groups', async () => {
  const user = userEvent.setup();
  const dynamicAddresses = [
    ...addresses,
    { id: 'adr_http', name: 'unencrypted-feed', kind: 'url', url: 'http://lists.example/ips.txt', refreshSeconds: 3600 },
    { id: 'adr_vpn', name: 'vpn-peers', kind: 'hostnames', names: ['vpn.example.com', 'backup.example.com'], refreshSeconds: 300 },
  ];

  show({ '/api/v1/config/aliases/addresses': list(dynamicAddresses) });

  // Row shows URL, refresh cadence, and HTTP warning; hostnames row shows names and refresh cadence.
  expect(await screen.findByText('http://lists.example/ips.txt')).toBeInTheDocument();
  expect(screen.getByText(/Not secure: anyone on the path can change the list/i)).toBeInTheDocument();
  expect(screen.getByText('Refreshes every 1 hour')).toBeInTheDocument();
  expect(screen.getByText('vpn.example.com, backup.example.com')).toBeInTheDocument();
  expect(screen.getByText('Refreshes every 5 minutes')).toBeInTheDocument();

  // Group Members picker excludes both groups and dynamic (url/hostnames) aliases.
  await user.click(screen.getByRole('button', { name: 'Edit address alias servers' }));
  const panel = await screen.findByRole('dialog', { name: 'Edit address alias: servers' });
  const membersInput = within(panel).getByLabelText('Members', { selector: 'input' });
  await user.click(membersInput);
  expect(within(document.getElementById(membersInput.getAttribute('aria-controls')!)!).getAllByRole('option', { hidden: true }).map((o) => o.textContent))
    .toEqual(['lab', 'nas', 'pool']);
});

it('creates URL list and host-name aliases, validates locally, and maps 422 field errors', async () => {
  const user = userEvent.setup();
  const created: unknown[] = [];
  let refuseFirstUrl = true;
  show({
    'POST /api/v1/config/aliases/addresses': (init) => {
      const body = JSON.parse(String(init?.body ?? '{}'));
      if (refuseFirstUrl && body.kind === 'url') {
        refuseFirstUrl = false;
        return jsonResponse(422, {
          error: {
            code: 'VALIDATION_FAILED',
            message: 'Invalid alias.',
            details: [
              { path: '/url', message: 'Server refused this URL.' },
              { path: '/refreshSeconds', message: 'Interval rejected.' },
            ],
          },
        });
      }
      created.push(body);
      return jsonResponse(201, { value: { id: `adr_new_${created.length}`, ...body }, version: '11' });
    },
  });

  const pasteInto = async (el: HTMLElement, text: string) => { await user.click(el); await user.paste(text); };

  // Create a URL list alias: local validation blocks bad URL and focuses field; http:// shows warning; 422 maps to fields.
  await user.click(await screen.findByRole('button', { name: 'Add address alias' }));
  let panel = await screen.findByRole('dialog', { name: 'Add address alias' });
  await user.selectOptions(within(panel).getByLabelText('Kind'), 'url');
  await pasteInto(within(panel).getByLabelText(/^Name/), 'cf-ips');
  const urlInput = within(panel).getByLabelText(/^URL/);
  await pasteInto(urlInput, 'ftp://bad.example/ips');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  expect(within(panel).getByText(/Expected an https:\/\/ or http:\/\//)).toBeInTheDocument();
  expect(urlInput).toHaveFocus();
  expect(created).toHaveLength(0);

  await user.clear(urlInput);
  await pasteInto(urlInput, 'http://lists.example/ips');
  expect(within(panel).getByText('HTTP is not secure')).toBeInTheDocument();
  await user.clear(urlInput);
  await pasteInto(urlInput, 'https://www.cloudflare.com/ips-v4');
  expect(within(panel).queryByText('HTTP is not secure')).not.toBeInTheDocument();
  expect(within(panel).getByLabelText(/^Refresh interval/)).toHaveValue('3600');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  expect(await within(panel).findByText('Server refused this URL.')).toBeInTheDocument();
  expect(within(panel).getByText('Interval rejected.')).toBeInTheDocument();
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(created).toHaveLength(1));
  expect(created[0]).toEqual({ name: 'cf-ips', kind: 'url', url: 'https://www.cloudflare.com/ips-v4', refreshSeconds: 3600 });
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Add address alias' })).not.toBeInTheDocument());

  // Create a host-name alias: switching kind prefills 300s default and normalizes/dedupes pasted hostnames.
  await user.click(screen.getByRole('button', { name: 'Add address alias' }));
  panel = await screen.findByRole('dialog', { name: 'Add address alias' });
  await user.selectOptions(within(panel).getByLabelText('Kind'), 'hostnames');
  expect(within(panel).getByLabelText(/^Refresh interval/)).toHaveValue('300');
  await pasteInto(within(panel).getByLabelText(/^Name/), 'remote-office');
  await pasteInto(within(panel).getByLabelText(/^Host names/, { selector: 'input' }), 'Office.Example.COM, office.example.com');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(created).toHaveLength(2));
  expect(created[1]).toEqual({ name: 'remote-office', kind: 'hostnames', names: ['office.example.com'], refreshSeconds: 300 });
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Add address alias' })).not.toBeInTheDocument());
});

it('edits existing dynamic aliases and blocks switching an alias in a forbidden placement to dynamic', async () => {
  const user = userEvent.setup();
  let updatedBody: unknown;
  const dynamicAddresses = [
    ...addresses,
    { id: 'adr_vpn', name: 'vpn-peers', kind: 'hostnames', names: ['vpn.example.com'], refreshSeconds: 900 },
  ];

  const { fetch } = show({
    '/api/v1/config/aliases/addresses': list(dynamicAddresses),
    'PUT /api/v1/config/aliases/addresses/adr_vpn': (init) => {
      updatedBody = JSON.parse(String(init?.body ?? '{}'));
      return jsonResponse(200, { value: { id: 'adr_vpn', ...(updatedBody as Record<string, unknown>) }, version: '5' });
    },
  });

  // Edit existing hostnames alias: preserves saved custom refreshSeconds (900) and saves cleanly.
  await user.click(await screen.findByRole('button', { name: 'Edit address alias vpn-peers' }));
  let panel = await screen.findByRole('dialog', { name: 'Edit address alias: vpn-peers' });
  expect(within(panel).getByLabelText(/^Refresh interval/)).toHaveValue('900');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  await waitFor(() => expect(updatedBody).toEqual({
    name: 'vpn-peers',
    kind: 'hostnames',
    names: ['vpn.example.com'],
    refreshSeconds: 900,
  }));
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Edit address alias: vpn-peers' })).not.toBeInTheDocument());

  // Trying to switch an alias used in management.allowedSources ('pool') to a URL list is blocked locally.
  await user.click(screen.getByRole('button', { name: 'Edit address alias pool' }));
  panel = await screen.findByRole('dialog', { name: 'Edit address alias: pool' });
  await user.selectOptions(within(panel).getByLabelText('Kind'), 'url');
  const urlInput = within(panel).getByLabelText(/^URL/);
  await user.click(urlInput);
  await user.paste('https://lists.example/ips');
  await user.click(within(panel).getByRole('button', { name: 'Save alias' }));
  expect(within(panel).getByText(/management segment's allowed sources/i)).toBeInTheDocument();
  expect(sent(fetch, 'PUT')).toHaveLength(1);
});
