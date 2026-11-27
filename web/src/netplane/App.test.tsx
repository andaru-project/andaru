// SPDX-License-Identifier: AGPL-3.0-or-later
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, it } from 'vitest';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { render, screen, waitFor, within } from '../test/render';
import { configureNetplaneApi, signOut } from './api';
import { App } from './App';
import { setRoutePrefix } from './routing';

const bodies: Record<string, object> = {
  '/api/v1/state': { devices: [], links: [], lag_groups: [], segments: [], zones: [], gateways: [], routes: [], vpns: [], firewalls: [], radios: [], wireless_uplinks: [], clients: [] },
  '/api/v1/topology': { generated_at: '2026-01-01T00:00:00Z', root_ids: [], nodes: [], edges: [] },
  '/api/v1/vlans/flow': { generated_at: '2026-01-01T00:00:00Z', vlans: [], findings: [] },
  '/api/v1/targets': { items: [] },
  '/api/v1/diagnostics': { items: [] },
};

// A server started with a token answers only a browser holding it in the cookie.
function server(token: { current: string | null }) {
  return fetchRoutes(Object.fromEntries(Object.entries(bodies).map(([path, body]) => [
    path,
    () => token.current === null || document.cookie.split('; ').includes(`netplane_token=${token.current}`)
      ? jsonResponse(200, body)
      : jsonResponse(401, { error: { code: 'UNAUTHORIZED', message: 'valid bearer token required' } }),
  ])));
}

afterEach(() => {
  signOut();
  configureNetplaneApi(null);
  setRoutePrefix('');
});

async function openAccount() {
  await userEvent.click(screen.getByRole('button', { name: 'Account' }));
  return screen.findByRole('menu');
}

it('asks for the token on a 401, keeps it in the cookie, and signs out to the sign-in page', async () => {
  const token = { current: 'secret' as string | null };
  server(token);
  render(<MemoryRouter><App /></MemoryRouter>);

  const input = await screen.findByLabelText(/^API token/);
  expect(input).toHaveFocus();
  await userEvent.type(input, 'wrong');
  await userEvent.click(screen.getByRole('button', { name: 'Sign in' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('The server did not accept this token.');
  await waitFor(() => expect(input).toHaveValue(''));
  expect(document.cookie).not.toContain('netplane_token=');

  await userEvent.type(input, 'secret');
  await userEvent.click(screen.getByRole('button', { name: 'Sign in' }));
  expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument();
  expect(document.cookie).toContain('netplane_token=secret');

  await userEvent.click(within(await openAccount()).getByRole('menuitem', { name: 'Sign out' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('You signed out.');
  expect(screen.getByLabelText(/^API token/)).toBeInTheDocument();
  expect(document.cookie).not.toContain('netplane_token=');
});

it('returns to the sign-in page when the server stops accepting the token', async () => {
  const token = { current: 'secret' as string | null };
  server(token);
  render(<MemoryRouter><App /></MemoryRouter>);
  await userEvent.type(await screen.findByLabelText(/^API token/), 'secret');
  await userEvent.click(screen.getByRole('button', { name: 'Sign in' }));
  await screen.findByRole('heading', { name: 'Overview' });

  token.current = 'rotated';
  await userEvent.click(screen.getByRole('button', { name: 'Refresh' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('The server no longer accepts the stored token.');
  expect(document.cookie).not.toContain('netplane_token=');
});

it('shows no sign-in and no sign out when the server has no token', async () => {
  server({ current: null });
  render(<MemoryRouter><App /></MemoryRouter>);
  expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument();
  expect(screen.queryByLabelText(/^API token/)).not.toBeInTheDocument();
  expect(within(await openAccount()).queryByRole('menuitem', { name: 'Sign out' })).not.toBeInTheDocument();
});
