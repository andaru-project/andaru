// SPDX-License-Identifier: AGPL-3.0-or-later
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { emptyFleet, emptyState } from '../test/fleet';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { render, screen, within } from '../test/render';
import { configureNetplaneApi } from './api';
import { setRoutePrefix } from './routing';
import { Shell } from './Shell';

afterEach(() => {
  configureNetplaneApi(null);
  setRoutePrefix('');
});

function Where() {
  const { pathname, search } = useLocation();
  return <output data-testid="where">{pathname + search}</output>;
}

function mount(path: string, onSignOut?: () => void, routes: Parameters<typeof fetchRoutes>[0] = emptyFleet) {
  const fetchMock = fetchRoutes(routes);
  render(<MemoryRouter initialEntries={[path]}><Shell onSignOut={onSignOut} onSessionLost={vi.fn()} /><Where /></MemoryRouter>);
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

it.each(['/firewall/rules', '/changes', '/system/users', '/network/interfaces'])('sends %s, an Andaru page it does not serve, to the Overview', async (path) => {
  mount(path);
  expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument();
  expect(document.title).toBe('Overview — Netplane');
});

it.each([
  ['/topology', '/network/topology'],
  ['/vlans', '/network/vlans'],
  ['/vlans/flow?vlan=20', '/network/vlans/flow?vlan=20'],
  ['/devices', '/network/devices'],
  ['/devices/sw%2F1?port=ge-0', '/network/devices/sw%2F1?port=ge-0'],
  ['/clients', '/network/clients'],
  ['/diagnostics', '/network/diagnostics'],
])('sends the old standalone path %s to %s', async (path, to) => {
  mount(path);
  await vi.waitFor(() => expect(screen.getByTestId('where')).toHaveTextContent(to));
});

it('says when the server stops answering, and when it answers again', async () => {
  let reachable = false;
  mount('/', undefined, { ...emptyFleet, '/api/v1/state': () => (reachable ? jsonResponse(200, emptyState) : new TypeError('Failed to fetch')) });
  expect(await screen.findByText('Connection lost')).toBeInTheDocument();
  reachable = true;
  await userEvent.click(screen.getByRole('button', { name: 'Refresh' }));
  expect(await screen.findByText('Connected')).toBeInTheDocument();
  expect(screen.queryByText('Connection lost')).not.toBeInTheDocument();
});

it('keeps the account menu to appearance and time zone', async () => {
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
