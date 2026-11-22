import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen, waitFor, within } from '../test/render';
import { RulesScreen } from './RulesScreen';

const lan = 'zon_lan';
const wan = 'zon_wan';
const side = (zones: string[] = [], addresses: string[] = [], not?: boolean) => ({ zones, addresses, ...(not ? { not } : {}) });
const rule = (id: string, name: string, more: Record<string, unknown> = {}) => ({ id, name, enabled: true, action: 'accept', from: side([lan]), to: side(), ports: [], log: false, ...more });
const saved = [
  rule('rul_a', 'LAN out, except the printers', { from: side([lan], ['adr_printers'], true), to: side([wan]), ports: ['svc_https'] }),
  rule('rul_b', 'DNS to the firewall', { to: side(['zon_firewall']), ports: ['svc_dns'], log: true }),
  rule('rul_c', 'Old rule', { enabled: false, action: 'drop', from: side(), description: 'Kept for reference' }),
];
const config = (value: Record<string, unknown> = {}) => ({ state: 'saved', savedVersion: '9', unusedSecrets: [], value: {
  links: [], zones: [{ id: lan, name: 'lan' }, { id: wan, name: 'wan' }],
  segments: [{ id: 'seg_lan', name: 'LAN', link: 'lnk_a', zone: lan, addressing: { ipv4: { mode: 'static' }, ipv6: { mode: 'none' } } }],
  management: { segment: 'seg_lan' },
  aliases: { addresses: [{ id: 'adr_printers', name: 'printers', kind: 'group' }], ports: [{ id: 'svc_https', name: 'HTTPS', protocol: 'tcp' }, { id: 'svc_dns', name: 'DNS', protocol: 'udp' }] },
  nat: { portForwards: [{ id: 'pfw_a', enabled: true, filterRule: 'auto' }, { id: 'pfw_b', enabled: true, filterRule: 'none' }, { id: 'pfw_c', enabled: false, filterRule: 'auto' }, { id: 'pfw_d', enabled: true, noRedirect: true }] },
  ...value,
} });
const list = (items = saved) => ({ state: 'saved', savedVersion: '9', version: '7', items: items.map((value) => ({ value, version: '5' })) });

function show(routes: Parameters<typeof fetchRoutes>[0] = {}, capabilities = ['configuration.read', 'configuration.write']) {
  const onSaved = vi.fn();
  const fetch = fetchRoutes({ '/api/v1/config/rules': list(), '/api/v1/config': config(), ...routes });
  render(<MemoryRouter><RulesScreen api={new ApiClient('csrf')} session={session(capabilities)} onSaved={onSaved} onSessionLost={vi.fn()} /></MemoryRouter>);
  return { fetch, onSaved };
}
const rows = () => screen.getAllByRole('row').slice(1);
const sent = (fetch: ReturnType<typeof fetchRoutes>, method: string) => fetch.mock.calls.filter(([, init]) => init?.method === method);

it('lists the rules in execution order, each as a sentence of names', async () => {
  show();
  await screen.findByText('LAN out, except the printers');
  expect(rows().map((row) => within(row).getAllByRole('cell')[0].textContent)).toEqual(['1', '2', '3']);
  const first = within(rows()[0]).getAllByRole('cell');
  expect(first[2]).toHaveTextContent('accept');
  // "From lan, not the printers, to wan, HTTPS": the word "not", never a colour alone.
  expect(first[3]).toHaveTextContent(/zone lan.*not.*printers/);
  expect(first[4]).toHaveTextContent('wan');
  expect(first[5]).toHaveTextContent('HTTPS');
  const second = within(rows()[1]).getAllByRole('cell');
  expect(second[4]).toHaveTextContent('This firewall');
  expect(second[2]).toHaveTextContent('logs');
  const third = within(rows()[2]).getAllByRole('cell');
  expect(third[3]).toHaveTextContent('any');
  expect(third[6]).toHaveTextContent('Kept for reference');
  expect(screen.getByText('Everything else is dropped.')).toBeInTheDocument();
});

it('says what Andaru always does before any rule, and counts the port forwards that let their traffic in', async () => {
  show();
  const fixed = await screen.findByRole('region', { name: 'Before any rule' });
  expect(fixed).toHaveTextContent('SSH to this firewall from the management segment, LAN, is always accepted');
  expect(fixed).toHaveTextContent('Port forwards that let their own traffic in: 1.');
});

it('says the filter is off without a management segment, rather than showing rules that look live', async () => {
  show({ '/api/v1/config': config({ management: undefined }) });
  expect(await screen.findByText('No management segment is set')).toBeInTheDocument();
  // No filter is loaded, so the page does not describe one.
  await screen.findByText('Old rule');
  expect(screen.queryByRole('region', { name: 'Before any rule' })).not.toBeInTheDocument();
  expect(screen.queryByText('Everything else is dropped.')).not.toBeInTheDocument();
});

it('says why nothing passes when there are no rules', async () => {
  show({ '/api/v1/config/rules': list([]) });
  expect(await screen.findByText('No rules')).toBeInTheDocument();
  expect(screen.queryByRole('table')).not.toBeInTheDocument();
});

it('filters without renumbering, and keeps the order locked while rows are hidden', async () => {
  const user = userEvent.setup();
  show();
  await screen.findByText('Old rule');
  await user.click(screen.getByRole('button', { name: 'Edit order' }));
  expect(screen.getByRole('button', { name: 'Move rule 3, Old rule' })).toBeInTheDocument();
  await user.type(screen.getByLabelText('Filter rules'), 'firewall');
  expect(rows()).toHaveLength(1);
  expect(within(rows()[0]).getAllByRole('cell')[0]).toHaveTextContent('2');
  // Typing in the filter locks the order again, and the page says why it cannot be unlocked now.
  expect(screen.queryByRole('button', { name: /^Move / })).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Edit order' })).toBeDisabled();
  expect(screen.getByText(/The order is locked while the filter is in use/)).toBeInTheDocument();
  // Every word must be in the row, and the words the row shows count: "logs", "not".
  const filter = screen.getByLabelText('Filter rules');
  for (const [text, positions] of [['lan wan', ['1']], ['logs', ['2']], ['not printers', ['1']], ['wan dns', []], ['  ', ['1', '2', '3']]] as const) {
    await user.clear(filter);
    await user.type(filter, text);
    expect(screen.queryAllByRole('row').slice(1).map((row) => within(row).getAllByRole('cell')[0].textContent)).toEqual(positions);
  }
  // A filter of spaces hides nothing, so it locks nothing.
  expect(screen.getByRole('button', { name: 'Edit order' })).toBeEnabled();
});

it('gives an account that may not write no controls', async () => {
  show({}, ['configuration.read']);
  await screen.findByText('Old rule');
  expect(screen.queryByRole('button', { name: 'Add rule' })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Edit order' })).not.toBeInTheDocument();
  expect(screen.queryByRole('switch')).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /^(Edit|Delete) / })).not.toBeInTheDocument();
  expect(within(rows()[2]).getAllByRole('cell')[1]).toHaveTextContent('off');
});

it('saves the enabled switch at once, as one version-checked edit', async () => {
  const user = userEvent.setup();
  const { fetch, onSaved } = show({ 'PUT /api/v1/config/rules/rul_c': { value: { ...saved[2], enabled: true }, version: '10' } });
  await user.click(await screen.findByRole('switch', { name: 'Enable rule 3, Old rule' }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  const [[path, init]] = sent(fetch, 'PUT');
  expect(path).toBe('/api/v1/config/rules/rul_c');
  expect(new Headers(init?.headers).get('If-Match')).toBe('"rul_c.5"');
  expect(JSON.parse(String(init?.body))).toMatchObject({ name: 'Old rule', enabled: true, action: 'drop' });
});

it('moves a rule by its grip with the keyboard, and sends the whole order with the list version', async () => {
  const user = userEvent.setup();
  const { fetch, onSaved } = show({ 'PUT /api/v1/config/rules/order': { order: [], version: '10' } });
  await screen.findByText('Old rule');
  expect(screen.queryByRole('button', { name: /^Move / })).not.toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: 'Edit order' }));
  screen.getByRole('button', { name: 'Move rule 3, Old rule' }).focus();
  // jsdom lays nothing out, so every row sits at the origin: the test gives each a place.
  rows().forEach((row, index) => { row.getBoundingClientRect = () => new DOMRect(0, index * 40, 600, 40); });
  await user.keyboard('[Space]');
  await user.keyboard('[ArrowUp]');
  await user.keyboard('[Space]');
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  const [[path, init]] = sent(fetch, 'PUT');
  expect(path).toBe('/api/v1/config/rules/order');
  expect(new Headers(init?.headers).get('If-Match')).toBe('"order.7"');
  expect(JSON.parse(String(init?.body))).toEqual(['rul_a', 'rul_c', 'rul_b']);
});

it('reloads and says so when a rule changed underneath, and never guesses', async () => {
  const user = userEvent.setup();
  const { fetch } = show({ 'PUT /api/v1/config/rules/rul_c': jsonResponse(412, { error: { code: 'PRECONDITION_FAILED', message: 'The object changed.' } }) });
  await user.click(await screen.findByRole('switch', { name: 'Enable rule 3, Old rule' }));
  expect(await screen.findByText(/changed by someone else/)).toBeInTheDocument();
  expect(fetch.mock.calls.filter(([path, init]) => path === '/api/v1/config/rules' && !init?.method).length).toBeGreaterThan(1);
});

it('asks once before deleting a rule, naming it', async () => {
  const user = userEvent.setup();
  const { fetch, onSaved } = show({ 'DELETE /api/v1/config/rules/rul_b': new Response(null, { status: 204 }) });
  await user.click(await screen.findByRole('button', { name: 'Delete rule 2, DNS to the firewall' }));
  const dialog = await screen.findByRole('dialog', { name: 'Delete rule' });
  expect(dialog).toHaveTextContent('Delete rule 2, DNS to the firewall?');
  expect(sent(fetch, 'DELETE')).toHaveLength(0);
  await user.click(within(dialog).getByRole('button', { name: 'Delete rule' }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  expect(new Headers(sent(fetch, 'DELETE')[0][1]?.headers).get('If-Match')).toBe('"rul_b.5"');
});

it('creates a rule from the panel, and shows the engine\'s refusals beside their fields', async () => {
  const user = userEvent.setup();
  let refuse = true;
  const { fetch, onSaved } = show({ 'POST /api/v1/config/rules': () => {
    if (!refuse) return jsonResponse(201, { value: rule('rul_new', 'Guests out'), version: '10' });
    refuse = false;
    return jsonResponse(422, { error: { code: 'VALIDATION_FAILED', message: 'The configuration is not valid.', details: [{ path: '/to/zones', message: 'The firewall stands alone as a destination.' }] } });
  } });
  await user.click(await screen.findByRole('button', { name: 'Add rule' }));
  const panel = await screen.findByRole('dialog', { name: 'Add rule' });
  await user.click(within(panel).getByRole('button', { name: 'Save rule' }));
  expect(within(panel).getByText('A rule needs a name.')).toBeInTheDocument();
  expect(sent(fetch, 'POST')).toHaveLength(0);
  await user.type(within(panel).getByLabelText(/^Name/), 'Guests out');
  await user.click(within(panel).getByRole('radio', { name: 'Reject' }));
  await user.click(within(panel).getByRole('button', { name: 'Save rule' }));
  expect(await within(panel).findByText('The firewall stands alone as a destination.')).toBeInTheDocument();
  expect(within(within(panel).getByRole('group', { name: 'To' })).getByText('The firewall stands alone as a destination.')).toBeInTheDocument();
  expect(within(within(panel).getByRole('group', { name: 'From' })).queryByText('The firewall stands alone as a destination.')).not.toBeInTheDocument();
  expect(within(panel).getByRole('alert')).toHaveTextContent('Some of the fields below were refused.');
  // What was typed is kept after a refusal.
  expect(within(panel).getByLabelText(/^Name/)).toHaveValue('Guests out');
  await user.click(within(panel).getByRole('button', { name: 'Save rule' }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  expect(JSON.parse(String(sent(fetch, 'POST')[1][1]?.body))).toEqual({ name: 'Guests out', enabled: true, action: 'reject', from: side(), to: side(), ports: [], log: false });
  expect(new Headers(sent(fetch, 'POST')[1][1]?.headers).get('X-Andaru-CSRF')).toBe('csrf');
  // The panel closes and the list is read again.
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Add rule' })).not.toBeInTheDocument());
  expect(fetch.mock.calls.filter(([path, init]) => path === '/api/v1/config/rules' && !init?.method).length).toBeGreaterThan(1);
});

it('opens a saved rule in the panel with every field as saved, and saves it whole against its version', async () => {
  const user = userEvent.setup();
  const full = rule('rul_a', 'LAN out', { description: 'Why it is here', enabled: false, action: 'drop', from: side([lan], ['adr_printers'], true), to: side([wan]), ports: ['svc_https'], log: false });
  const { fetch } = show({ '/api/v1/config/rules': list([full]), 'PUT /api/v1/config/rules/rul_a': { value: full, version: '10' } });
  await user.click(await screen.findByRole('button', { name: 'Edit rule 1, LAN out' }));
  const panel = await screen.findByRole('dialog', { name: 'Edit rule: LAN out' });
  await user.click(within(panel).getByRole('switch', { name: 'Log what this rule matches' }));
  await user.click(within(panel).getByRole('button', { name: 'Save rule' }));
  await waitFor(() => expect(sent(fetch, 'PUT')).toHaveLength(1));
  const [[, init]] = sent(fetch, 'PUT');
  expect(new Headers(init?.headers).get('If-Match')).toBe('"rul_a.5"');
  // Nothing the form did not touch is lost or changed.
  expect(JSON.parse(String(init?.body))).toEqual({ ...full, id: undefined, log: true });
});

it('drops "not" from a side that names no address to leave out', async () => {
  const user = userEvent.setup();
  const { fetch } = show({
    '/api/v1/config/rules': list([rule('rul_a', 'LAN out', { from: { zones: [lan], addresses: [], not: true } })]),
    'PUT /api/v1/config/rules/rul_a': { value: rule('rul_a', 'LAN out'), version: '10' },
  });
  await user.click(await screen.findByRole('button', { name: 'Edit rule 1, LAN out' }));
  await user.click(within(await screen.findByRole('dialog', { name: 'Edit rule: LAN out' })).getByRole('button', { name: 'Save rule' }));
  await waitFor(() => expect(sent(fetch, 'PUT')).toHaveLength(1));
  expect(JSON.parse(String(sent(fetch, 'PUT')[0][1]?.body)).from).toEqual({ zones: [lan], addresses: [] });
});

it('lets the firewall stand alone as a destination: choosing it leaves the other zones, and choosing another leaves it', async () => {
  const user = userEvent.setup();
  const { fetch } = show({ 'POST /api/v1/config/rules': jsonResponse(201, { value: rule('rul_new', 'To the box'), version: '10' }) });
  await user.click(await screen.findByRole('button', { name: 'Add rule' }));
  const panel = await screen.findByRole('dialog', { name: 'Add rule' });
  await user.type(within(panel).getByLabelText(/^Name/), 'To the box');
  const zones = within(panel).getByLabelText('To: zones');
  // The picker's list belongs to its field; jsdom does not lay it out, so it is asked for whether shown or not.
  const options = (field: HTMLElement) => within(document.getElementById(field.getAttribute('aria-controls')!)!).getAllByRole('option', { hidden: true });
  const pick = async (name: string) => { await user.click(zones); await user.click(options(zones).find((option) => option.textContent === name)!); };
  // It is offered to a rule's destination, and never to its source.
  const from = within(panel).getByLabelText('From: zones');
  await user.click(from);
  expect(options(from).map((option) => option.textContent)).toEqual(['lan', 'wan']);
  await user.click(zones);
  expect(options(zones).map((option) => option.textContent)).toEqual(['This firewall', 'lan', 'wan']);
  await pick('wan');
  await pick('This firewall');
  await user.click(within(panel).getByRole('button', { name: 'Save rule' }));
  await waitFor(() => expect(sent(fetch, 'POST')).toHaveLength(1));
  expect(JSON.parse(String(sent(fetch, 'POST')[0][1]?.body)).to).toEqual({ zones: ['zon_firewall'], addresses: [] });
});

it('signs the session out when the panel\'s save finds it gone', async () => {
  const user = userEvent.setup();
  const onSessionLost = vi.fn();
  fetchRoutes({ '/api/v1/config/rules': list(), '/api/v1/config': config(), 'POST /api/v1/config/rules': jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Sign in.' } }) });
  render(<MemoryRouter><RulesScreen api={new ApiClient('csrf')} session={session(['configuration.read', 'configuration.write'])} onSaved={vi.fn()} onSessionLost={onSessionLost} /></MemoryRouter>);
  await user.click(await screen.findByRole('button', { name: 'Add rule' }));
  const panel = await screen.findByRole('dialog', { name: 'Add rule' });
  await user.type(within(panel).getByLabelText(/^Name/), 'A rule');
  await user.click(within(panel).getByRole('button', { name: 'Save rule' }));
  await waitFor(() => expect(onSessionLost).toHaveBeenCalled());
});

it('puts a rule back and says so when someone else reordered first', async () => {
  const user = userEvent.setup();
  show({ 'PUT /api/v1/config/rules/order': jsonResponse(412, { error: { code: 'PRECONDITION_FAILED', message: 'The list changed.' } }) });
  await screen.findByText('Old rule');
  await user.click(screen.getByRole('button', { name: 'Edit order' }));
  screen.getByRole('button', { name: 'Move rule 3, Old rule' }).focus();
  rows().forEach((row, index) => { row.getBoundingClientRect = () => new DOMRect(0, index * 40, 600, 40); });
  await user.keyboard('[Space]');
  await user.keyboard('[ArrowUp]');
  await user.keyboard('[Space]');
  expect(await screen.findByText(/changed by someone else/)).toBeInTheDocument();
  await waitFor(() => expect(rows().map((row) => within(row).getAllByRole('cell')[6].textContent)).toEqual(['LAN out, except the printers', 'DNS to the firewall', 'Old ruleKept for reference']));
});

it('reads the list again when the panel\'s save is refused, so the rule opens next on what is saved', async () => {
  const user = userEvent.setup();
  let version = '5';
  const { fetch } = show({
    '/api/v1/config/rules': () => jsonResponse(200, { state: 'saved', savedVersion: '9', version: '7', items: [{ value: saved[1], version }] }),
    'PUT /api/v1/config/rules/rul_b': () => {
      if (version !== '5') return jsonResponse(200, { value: saved[1], version: '7' });
      version = '6';
      return jsonResponse(412, { error: { code: 'PRECONDITION_FAILED', message: 'The object changed.' } });
    },
  });
  await user.click(await screen.findByRole('button', { name: 'Edit rule 1, DNS to the firewall' }));
  let panel = await screen.findByRole('dialog', { name: 'Edit rule: DNS to the firewall' });
  await user.click(within(panel).getByRole('button', { name: 'Save rule' }));
  expect(await within(panel).findByText(/changed by someone else/)).toBeInTheDocument();
  await user.click(within(panel).getByRole('button', { name: 'Cancel' }));
  await user.click(await screen.findByRole('button', { name: 'Edit rule 1, DNS to the firewall' }));
  panel = await screen.findByRole('dialog', { name: 'Edit rule: DNS to the firewall' });
  await user.click(within(panel).getByRole('button', { name: 'Save rule' }));
  await waitFor(() => expect(sent(fetch, 'PUT')).toHaveLength(2));
  expect(new Headers(sent(fetch, 'PUT')[1][1]?.headers).get('If-Match')).toBe('"rul_b.6"');
});

it('reads the rules and the configuration again when a save landed between the two reads', async () => {
  let reads = 0;
  show({ '/api/v1/config': () => jsonResponse(200, reads++ === 0 ? { ...config({ aliases: { addresses: [], ports: [] } }), savedVersion: '8' } : config()) });
  await screen.findByText('LAN out, except the printers');
  // The first configuration was older than the rules and did not hold the alias the first rule names.
  expect(within(rows()[0]).getAllByRole('cell')[3]).toHaveTextContent('printers');
  expect(screen.queryByText('unknown alias')).not.toBeInTheDocument();
});

it('keeps the grips while a move is being saved, so the keyboard stays on the one that moved', async () => {
  const user = userEvent.setup();
  let answer: (response: Response) => void = () => {};
  const { fetch, onSaved } = show({ 'PUT /api/v1/config/rules/order': () => new Promise<Response>((resolve) => { answer = resolve; }) });
  await screen.findByText('Old rule');
  await user.click(screen.getByRole('button', { name: 'Edit order' }));
  const grip = screen.getByRole('button', { name: 'Move rule 3, Old rule' });
  grip.focus();
  rows().forEach((row, index) => { row.getBoundingClientRect = () => new DOMRect(0, index * 40, 600, 40); });
  await user.keyboard('[Space]');
  await user.keyboard('[ArrowUp]');
  await user.keyboard('[Space]');
  // The save is out: the same element is still there, marked idle, and nothing else can be moved.
  await waitFor(() => expect(sent(fetch, 'PUT')).toHaveLength(1));
  expect(grip).toBeInTheDocument();
  expect(grip).toHaveAttribute('data-disabled');
  expect(screen.getAllByRole('button', { name: /^Move rule/ })).toHaveLength(3);
  answer(jsonResponse(200, { order: [], version: '10' }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  await waitFor(() => expect(grip).not.toHaveAttribute('data-disabled'));
  expect(grip).toHaveFocus();
});

it('shows the rules after three tries even when the two reads never agree', async () => {
  const { fetch } = show({ '/api/v1/config': { ...config(), savedVersion: '8' } });
  await screen.findByText('LAN out, except the printers');
  expect(fetch.mock.calls.filter(([path]) => path === '/api/v1/config')).toHaveLength(3);
});

it('lets only the grips act while the order is unlocked, and says each rule\'s state in words', async () => {
  const user = userEvent.setup();
  show();
  await screen.findByText('Old rule');
  // A side that names nothing is any network; ports that name nothing are any.
  const third = within(rows()[2]).getAllByRole('cell');
  expect(third[3]).toHaveTextContent('any network');
  expect(third[5]).toHaveTextContent(/^any$/);
  await user.click(screen.getByRole('button', { name: 'Edit order' }));
  expect(screen.queryByRole('switch')).not.toBeInTheDocument();
  expect(within(rows()[0]).getAllByRole('cell')[1]).toHaveTextContent('on');
  expect(within(rows()[2]).getAllByRole('cell')[1]).toHaveTextContent('off');
  for (const name of ['Edit rule 1, LAN out, except the printers', 'Delete rule 1, LAN out, except the printers', 'Add rule']) expect(screen.getByRole('button', { name })).toBeDisabled();
  expect(screen.getByText(/Drag a rule by its grip to move it/)).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: 'Done' }));
  expect(screen.getByRole('switch', { name: 'Enable rule 1, LAN out, except the printers' })).toBeEnabled();
  expect(screen.getByRole('button', { name: 'Add rule' })).toBeEnabled();
});

it('names each side\'s except switch by its side, and says where a new rule goes', async () => {
  const user = userEvent.setup();
  show();
  await user.click(await screen.findByRole('button', { name: 'Add rule' }));
  const panel = await screen.findByRole('dialog', { name: 'Add rule' });
  expect(within(panel).getByRole('switch', { name: /^From: except these addresses/ })).toBeDisabled();
  expect(within(panel).getByRole('switch', { name: /^To: except these addresses/ })).toBeDisabled();
  expect(within(panel).getByText(/A new rule is added at the bottom of the list/)).toBeInTheDocument();
});
