import { MemoryRouter } from 'react-router';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import type { ChangeState } from '../changes';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { apply, pointers, session } from '../test/fixtures';
import { render, screen, within } from '../test/render';
import { DashboardScreen } from './DashboardScreen';

const link = (name: string, operState: string, addresses: Array<[string, number, string]>) => ({
  ifIndex: 2, name, linkType: name === 'lo' ? 'loopback' : 'ether', mtu: 1500, operState, flags: [],
  addresses: addresses.map(([address, prefixLength, scope]) => ({ family: 'ipv4', address, prefixLength, scope })),
});

function mount(capabilities: string[], changes: ChangeState = { applies: { pointers: { ...pointers, live: '1' }, items: [apply({})] }, head: '3' }) {
  render(<MemoryRouter><DashboardScreen api={new ApiClient('csrf')} session={session(capabilities)} changes={changes} changesError="" onSessionLost={vi.fn()} /></MemoryRouter>);
}

it('shows each widget its own data, and a widget that fails does not blank the others', async () => {
  fetchRoutes({
    '/api/v1/status/links': { items: [link('lo', 'UNKNOWN', []), link('ens18', 'UP', [['10.30.0.100', 24, 'global'], ['fe80::1', 64, 'link']])] },
    '/api/v1/status/prefixes': () => jsonResponse(503, { error: { code: 'INVENTORY_UNAVAILABLE', message: 'The delegated prefixes are unavailable.', details: [] } }),
    '/api/v1/audit/events': { items: [{ id: 1, at: '2026-11-21T10:00:00Z', actor: { kind: 'user', id: 'admin' }, source: '', action: 'session.login', target: '', result: 'success', category: 'auth', count: 1, metadata: {} }] },
  });
  mount(['configuration.read', 'status.read', 'audit.read']);

  const interfaces = screen.getByRole('region', { name: 'Interfaces' });
  expect(await within(interfaces).findByText('ens18')).toBeInTheDocument();
  expect(within(interfaces).getByText('10.30.0.100/24')).toBeInTheDocument();
  // The loopback and link-local addresses are noise on a dashboard.
  expect(within(interfaces).queryByText('lo')).not.toBeInTheDocument();
  expect(within(interfaces).queryByText('fe80::1/64')).not.toBeInTheDocument();

  expect(await within(screen.getByRole('region', { name: 'IPv6 prefixes' })).findByRole('alert')).toHaveTextContent('The delegated prefixes are unavailable.');
  expect(await within(screen.getByRole('region', { name: 'Recent activity' })).findByText(/session\.login/)).toBeInTheDocument();
  expect(within(screen.getByRole('region', { name: 'Changes' })).getByText(/Nothing is pending/)).toBeInTheDocument();
  // A widget's title leads to the page with the full view.
  expect(screen.getByRole('link', { name: 'Recent activity' })).toHaveAttribute('href', '/system/audit');
});

it('shows only the widgets the capabilities allow', () => {
  fetchRoutes({ '/api/v1/audit/events': { items: [] } });
  mount(['audit.read']);
  expect(screen.getAllByRole('region').map((region) => region.getAttribute('aria-labelledby'))).toEqual(['widget-activity']);
});

it('says so when the account can see no widget', () => {
  fetchRoutes({});
  mount(['tokens.manageSelf']);
  expect(screen.getByRole('status')).toHaveTextContent('none of the dashboard');
});

it('reports saved changes that are not applied', () => {
  fetchRoutes({});
  mount(['configuration.read'], { applies: { pointers, items: [] }, head: '4' });
  expect(screen.getByText('Saved changes are not applied yet.')).toBeInTheDocument();
});

it('lists a delegated prefix and where it went', async () => {
  fetchRoutes({
    '/api/v1/status/links': { items: [] },
    '/api/v1/status/prefixes': { wans: [{ interface: 'ens18', current: { prefix: '2001:db8:30::/56' }, segments: [{ interface: 'ens19', subnetId: '1', prefix: '2001:db8:30:1::/64' }], pools: [], notFitting: ['guest'] }] },
  });
  mount(['status.read']);
  const prefixes = screen.getByRole('region', { name: 'IPv6 prefixes' });
  expect(await within(prefixes).findByText('2001:db8:30::/56')).toBeInTheDocument();
  expect(within(prefixes).getByText('2001:db8:30:1::/64 on ens19')).toBeInTheDocument();
  expect(within(prefixes).getByText('No room for: guest')).toBeInTheDocument();
});
