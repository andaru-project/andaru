// SPDX-License-Identifier: AGPL-3.0-or-later
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, it } from 'vitest';
import { emptyFleet } from '../test/fleet';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { render, screen, waitFor, within } from '../test/render';
import { configureNetplaneApi, signOut } from './api';
import { App } from './App';
import { setRoutePrefix } from './routing';

// A server started with a token answers only a browser that sends it in the cookie.
function server(token: { current: string | null }, root = '') {
  return fetchRoutes(Object.fromEntries(Object.entries(emptyFleet).map(([path, body]) => [
    `${root}${path}`,
    (init: RequestInit | undefined) => token.current === null || (init?.credentials !== 'omit' && document.cookie.split('; ').includes(`netplane_token=${token.current}`))
      ? jsonResponse(200, body)
      : jsonResponse(401, { error: { code: 'UNAUTHORIZED', message: 'valid bearer token required' } }),
  ])));
}

afterEach(() => {
  signOut();
  document.cookie = 'netplane_token=; path=/; Max-Age=0';
  document.head.querySelectorAll('base').forEach((el) => el.remove());
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

it('asks for the token when the server starts wanting one, without blaming a stored token', async () => {
  const token = { current: null as string | null };
  server(token);
  render(<MemoryRouter><App /></MemoryRouter>);
  await screen.findByRole('heading', { name: 'Overview' });

  token.current = 'secret';
  await userEvent.click(screen.getByRole('button', { name: 'Refresh' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('This Netplane server requires an API token.');
});

it('clears a stored token the server does not need, and offers no sign out', async () => {
  document.cookie = 'netplane_token=old; path=/; SameSite=Strict';
  server({ current: null });
  render(<MemoryRouter><App /></MemoryRouter>);
  expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument();
  expect(document.cookie).not.toContain('netplane_token=');
  expect(within(await openAccount()).queryByRole('menuitem', { name: 'Sign out' })).not.toBeInTheDocument();
});

it('offers no sign out for a token at a parent path, which signing out could not clear', async () => {
  const base = document.createElement('base');
  base.setAttribute('href', '/netplane/');
  document.head.appendChild(base);
  document.cookie = 'netplane_token=old; path=/; SameSite=Strict';
  server({ current: null }, '/netplane');
  render(<MemoryRouter><App /></MemoryRouter>);
  expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument();
  expect(within(await openAccount()).queryByRole('menuitem', { name: 'Sign out' })).not.toBeInTheDocument();
});

it('opens the shell when the server cannot be reached at start-up', async () => {
  fetchRoutes({ '/api/v1': new TypeError('Failed to fetch') });
  render(<MemoryRouter><App /></MemoryRouter>);
  expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument();
  expect(await screen.findByText('Connection lost')).toBeInTheDocument();
  expect(screen.queryByLabelText(/^API token/)).not.toBeInTheDocument();
});
