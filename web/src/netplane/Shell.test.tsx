// SPDX-License-Identifier: AGPL-3.0-or-later
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { fetchRoutes } from '../test/fetch';
import { render, screen, within } from '../test/render';
import { configureNetplaneApi } from './api';
import { setRoutePrefix } from './routing';
import { Shell } from './Shell';

const fleet = {
  '/api/v1/state': { devices: [], links: [], lag_groups: [], segments: [], zones: [], gateways: [], routes: [], vpns: [], firewalls: [], radios: [], wireless_uplinks: [], clients: [] },
  '/api/v1/topology': { generated_at: '2026-01-01T00:00:00Z', root_ids: [], nodes: [], edges: [] },
  '/api/v1/vlans/flow': { generated_at: '2026-01-01T00:00:00Z', vlans: [], findings: [] },
  '/api/v1/targets': { items: [] },
  '/api/v1/diagnostics': { items: [] },
};

afterEach(() => {
  configureNetplaneApi(null);
  setRoutePrefix('');
});

function mount(path: string, onSignOut?: () => void) {
  const fetchMock = fetchRoutes(fleet);
  render(<MemoryRouter initialEntries={[path]}><Shell onSignOut={onSignOut} onSessionLost={vi.fn()} /></MemoryRouter>);
  return fetchMock;
}

const navLinks = (nav: HTMLElement) => within(nav).queryAllByRole('link').map((link) => link.textContent);

it('offers Andaru\'s network and diagnostics pages, and none of its firewall, changes or system pages', async () => {
  mount('/');
  expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument();
  expect(screen.getByRole('heading', { name: 'Netplane', level: 1 })).toBeInTheDocument();
  const nav = screen.getByRole('navigation', { name: 'Main navigation' });
  expect(within(nav).getByRole('link', { name: 'Overview' })).toHaveAttribute('aria-current', 'page');
  expect(within(nav).getAllByRole('button').map((button) => button.textContent)).toEqual(['Network', 'Diagnostics']);
  expect(navLinks(nav)).toEqual(['Overview']);

  await userEvent.click(within(nav).getByRole('button', { name: 'Network' }));
  expect(navLinks(nav)).toEqual(['Overview', 'Topology', 'VLANs & Segments', 'VLAN Flow', 'Devices', 'Clients']);
  // One branch is open at a time.
  await userEvent.click(within(nav).getByRole('button', { name: 'Diagnostics' }));
  expect(navLinks(nav)).toEqual(['Overview', 'Tools & events']);
  expect(within(nav).getByRole('link', { name: 'Tools & events' })).toHaveAttribute('href', '/network/diagnostics');
});

it.each([
  ['/network/topology', 'Topology', 'Topology'],
  ['/network/vlans', 'VLANs & Segments', 'VLANs & Segments'],
  ['/network/vlans/flow', 'VLAN Flow', 'VLAN Flow'],
  ['/network/devices', 'Devices', 'Devices'],
  ['/network/clients', 'Clients', 'Clients'],
  ['/network/diagnostics', 'Tools & events', 'Diagnostics & Events'],
])('routes %s as Andaru does, on the standalone API', async (path, link, heading) => {
  const fetchMock = mount(path);
  expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument();
  expect(screen.getByRole('link', { name: link })).toHaveAttribute('aria-current', 'page');
  expect(document.title).toMatch(/ — Netplane$/);
  const urls = fetchMock.mock.calls.map(([url]) => String(url));
  expect(urls).toContain('/api/v1/state');
  expect(urls.some((url) => url.includes('/fleet/'))).toBe(false);
});

it.each(['/topology', '/firewall/rules', '/changes', '/system/users'])('sends %s, which it does not serve, to the Overview', async (path) => {
  mount(path);
  expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument();
  expect(document.title).toBe('Overview — Netplane');
});

it('keeps the account menu to appearance and time zone without a token', async () => {
  mount('/');
  await userEvent.click(screen.getByRole('button', { name: 'Account' }));
  const menu = await screen.findByRole('menu');
  expect([...menu.querySelectorAll('[role^="menuitem"]')].map((item) => item.textContent?.replace('✓', ''))).toEqual(['System', 'Light', 'Dark', 'Time zone…']);

  await userEvent.click(within(menu).getByRole('menuitem', { name: 'Time zone…' }));
  expect(await screen.findByRole('dialog', { name: 'Display time zone' })).toHaveTextContent('across Netplane in this browser');
});

it('offers sign out when signed in with a token', async () => {
  const onSignOut = vi.fn();
  mount('/', onSignOut);
  await userEvent.click(screen.getByRole('button', { name: 'Account' }));
  const menu = await screen.findByRole('menu');
  expect(within(menu).queryByRole('menuitem', { name: 'Session details' })).not.toBeInTheDocument();
  expect(within(menu).queryByRole('menuitem', { name: 'Change password' })).not.toBeInTheDocument();
  await userEvent.click(within(menu).getByRole('menuitem', { name: 'Sign out' }));
  expect(onSignOut).toHaveBeenCalledTimes(1);
});
