// SPDX-License-Identifier: AGPL-3.0-or-later
import { afterEach, expect, it, vi } from 'vitest';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { answersWithoutToken, api, configureNetplaneApi, hasAccess, signIn, signOut } from './api';
import { pinAppRoot, resetPinnedRoot } from './routing';

afterEach(() => {
  signOut();
  configureNetplaneApi(null);
  document.head.querySelectorAll('base').forEach((el) => el.remove());
  resetPinnedRoot();
});

const unauthorized = () => jsonResponse(401, { error: { code: 'UNAUTHORIZED', message: 'valid bearer token required' } });

it('keeps the token in a SameSite=Strict cookie scoped to the app, Secure on https', async () => {
  const base = document.createElement('base');
  base.setAttribute('href', '/api/hassio_ingress/tok_1/');
  document.head.appendChild(base);
  pinAppRoot();
  vi.spyOn(window, 'location', 'get').mockReturnValue({ ...window.location, protocol: 'https:' });
  const cookies = vi.spyOn(Document.prototype, 'cookie', 'set');
  fetchRoutes({ '/api/hassio_ingress/tok_1/api/v1/diagnostics': { items: [] } });

  await signIn('  s3cret/+ \n');
  expect(cookies).toHaveBeenLastCalledWith('netplane_token=s3cret%2F%2B; path=/api/hassio_ingress/tok_1/; SameSite=Strict; Secure');
  cookies.mockClear();
  signOut();
  // Expired at the app's path and at every path above it, where an older build may have left one.
  for (const path of ['/api/hassio_ingress/tok_1/', '/api/hassio_ingress/', '/api/', '/']) {
    expect(cookies).toHaveBeenCalledWith(`netplane_token=; path=${path}; SameSite=Strict; Max-Age=0; Secure`);
  }
});

it('takes the server for one without a token only on a plain success without the cookie', async () => {
  for (const [answer, free] of [[{ items: [] }, true], [jsonResponse(502, {}), false], [unauthorized(), false], [new TypeError('Failed to fetch'), false]] as const) {
    fetchRoutes({ '/api/v1/diagnostics': answer });
    expect(await answersWithoutToken()).toBe(free);
  }
  const redirected = jsonResponse(200, {});
  Object.defineProperty(redirected, 'redirected', { value: true });
  fetchRoutes({ '/api/v1/diagnostics': redirected });
  expect(await answersWithoutToken()).toBe(false); // a proxy's login page
});

it('keeps no token it could not check, and says the server is unreachable', async () => {
  fetchRoutes({ '/api/v1/diagnostics': new TypeError('Failed to fetch') });
  await expect(signIn('secret')).rejects.toThrow('Could not reach the Netplane server.');
  expect(document.cookie).not.toContain('netplane_token=');
});

it.each([500, 421])('counts a %i on the access check as access: only a 401 asks for the token', async (status) => {
  fetchRoutes({ '/api/v1/diagnostics': () => jsonResponse(status, { error: { code: 'X', message: 'x' } }) });
  await expect(hasAccess()).resolves.toBe(true);
});

it('gives up on an access check that never answers', async () => {
  const timeout = vi.spyOn(AbortSignal, 'timeout');
  const fetchMock = fetchRoutes({ '/api/v1/diagnostics': { items: [] } });
  await hasAccess();
  expect(fetchMock.mock.calls[0][1]?.signal).toBe(timeout.mock.results[0].value);
});

it('ignores a 401 to a request sent before the latest sign-in, or with another token than the one now stored', async () => {
  const onUnauthorized = vi.fn();
  configureNetplaneApi({ apiPrefix: 'api/v1', onUnauthorized });
  let answer!: (response: Response) => void;
  fetchRoutes({
    '/api/v1/state': () => new Promise<Response>((resolve) => { answer = resolve; }),
    '/api/v1/diagnostics': { items: [] },
  });

  // Sent before signing in again.
  const stale = api.getState();
  await signIn('new');
  answer(unauthorized());
  await expect(stale).rejects.toThrow('HTTP 401');
  expect(onUnauthorized).not.toHaveBeenCalled();
  expect(document.cookie).toContain('netplane_token=new');

  // Another tab signed in while it was out.
  const crossed = api.getState();
  document.cookie = 'netplane_token=other; path=/; SameSite=Strict';
  answer(unauthorized());
  await expect(crossed).rejects.toThrow('HTTP 401');
  expect(onUnauthorized).not.toHaveBeenCalled();

  // Sent before signing in again with the same token.
  const resigned = api.getState();
  await signIn('other');
  answer(unauthorized());
  await expect(resigned).rejects.toThrow('HTTP 401');
  expect(onUnauthorized).not.toHaveBeenCalled();

  // A 401 to the token now stored is the server refusing it.
  const current = api.getState();
  answer(unauthorized());
  await expect(current).rejects.toThrow('HTTP 401');
  expect(onUnauthorized).toHaveBeenCalledTimes(1);
});
