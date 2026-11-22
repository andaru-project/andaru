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
  nat: { portForwards: [{ id: 'pfw_a' }, { id: 'pfw_b', filterRule: 'none' }] },
  ...value,
} });
const list = (items = saved) => ({ state: 'saved', savedVersion: '9', version: '7', items: items.map((value) => ({ value, version: '5' })) });

function show(routes: Record<string, unknown> = {}, capabilities = ['configuration.read', 'configuration.write']) {
  const onSaved = vi.fn();
  const fetch = fetchRoutes({ '/api/v1/config/rules': list(), '/api/v1/config': config(), ...routes } as Parameters<typeof fetchRoutes>[0]);
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
  expect(fixed).toHaveTextContent('Port forwards let their own traffic in (1).');
});

it('says the filter is off without a management segment, rather than showing rules that look live', async () => {
  show({ '/api/v1/config': config({ management: undefined }) });
  expect(await screen.findByText('The packet filter is off')).toBeInTheDocument();
  expect(screen.getByRole('region', { name: 'Before any rule' })).not.toHaveTextContent('Anti-lockout');
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
  expect(screen.getByRole('button', { name: 'Move Old rule' })).toBeInTheDocument();
  await user.type(screen.getByLabelText('Filter rules'), 'firewall');
  expect(rows()).toHaveLength(1);
  expect(within(rows()[0]).getAllByRole('cell')[0]).toHaveTextContent('2');
  // Typing in the filter locks the order again, and the button says it cannot be unlocked now.
  expect(screen.queryByRole('button', { name: /^Move / })).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Edit order' })).toBeDisabled();
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
  await user.click(await screen.findByRole('switch', { name: 'Old rule enabled' }));
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
  screen.getByRole('button', { name: 'Move Old rule' }).focus();
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

it('reloads and says so when someone else reordered, and never guesses', async () => {
  const user = userEvent.setup();
  const { fetch } = show({ 'PUT /api/v1/config/rules/rul_c': jsonResponse(412, { error: { code: 'PRECONDITION_FAILED', message: 'The object changed.' } }) });
  await user.click(await screen.findByRole('switch', { name: 'Old rule enabled' }));
  expect(await screen.findByText(/changed by someone else/)).toBeInTheDocument();
  expect(fetch.mock.calls.filter(([path, init]) => path === '/api/v1/config/rules' && !init?.method).length).toBeGreaterThan(1);
});

it('asks once before deleting a rule, naming it', async () => {
  const user = userEvent.setup();
  const { fetch, onSaved } = show({ 'DELETE /api/v1/config/rules/rul_b': new Response(null, { status: 204 }) });
  await user.click(await screen.findByRole('button', { name: 'Delete DNS to the firewall' }));
  const dialog = await screen.findByRole('dialog', { name: 'Delete rule' });
  expect(dialog).toHaveTextContent('DNS to the firewall');
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
  // What was typed is kept after a refusal.
  expect(within(panel).getByLabelText(/^Name/)).toHaveValue('Guests out');
  await user.click(within(panel).getByRole('button', { name: 'Save rule' }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  expect(JSON.parse(String(sent(fetch, 'POST')[1][1]?.body))).toEqual({ name: 'Guests out', enabled: true, action: 'reject', from: side(), to: side(), ports: [], log: false });
});

it('opens a saved rule in the panel and saves it against its version, dropping "not" with no address to leave out', async () => {
  const user = userEvent.setup();
  const { fetch } = show({
    '/api/v1/config/rules': list([rule('rul_a', 'LAN out', { from: { zones: [lan], addresses: [], not: true } })]),
    'PUT /api/v1/config/rules/rul_a': { value: rule('rul_a', 'LAN out'), version: '10' },
  });
  await user.click(await screen.findByRole('button', { name: 'Edit LAN out' }));
  const panel = await screen.findByRole('dialog', { name: 'Edit LAN out' });
  await user.click(within(panel).getByRole('switch', { name: 'Log what this rule matches' }));
  await user.click(within(panel).getByRole('button', { name: 'Save rule' }));
  await waitFor(() => expect(sent(fetch, 'PUT')).toHaveLength(1));
  const [[, init]] = sent(fetch, 'PUT');
  expect(new Headers(init?.headers).get('If-Match')).toBe('"rul_a.5"');
  expect(JSON.parse(String(init?.body))).toMatchObject({ from: { zones: [lan], addresses: [] }, log: true });
  expect(JSON.parse(String(init?.body)).from).not.toHaveProperty('not');
});
