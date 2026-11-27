// SPDX-License-Identifier: AGPL-3.0-or-later
import { createTheme, MantineProvider } from '@mantine/core';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { BrowserRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, apiUrl, configureNetplaneApi } from './api';
import { apiBasePath, deviceIdFromPath, devicePath, getRouterBasename, netplanePath, pinAppRoot, resetPinnedRoot, setRoutePrefix, vlanFlowPath } from './routing';
import { Shell } from './Shell';
import { emptyFlow, emptyState, emptyTopology } from '../test/fleet';

const testTheme = createTheme({ primaryColor: 'teal', defaultRadius: 'sm' });

function setBaseHref(href: string | null) {
  document.head.querySelectorAll('base').forEach((el) => el.remove());
  if (href !== null) {
    const base = document.createElement('base');
    base.setAttribute('href', href);
    document.head.appendChild(base);
  }
}

afterEach(() => {
  configureNetplaneApi(null);
  setRoutePrefix('');
  resetPinnedRoot();
  vi.restoreAllMocks();
  setBaseHref(null);
  window.history.replaceState({}, '', '/');
});

// The shell reads the fleet itself; these answers keep it on the screens under test.
function mockFleet() {
  vi.spyOn(api, 'getState').mockResolvedValue(emptyState);
  vi.spyOn(api, 'getTopology').mockResolvedValue(emptyTopology);
  vi.spyOn(api, 'getVLANFlow').mockResolvedValue(emptyFlow);
  vi.spyOn(api, 'listTargets').mockResolvedValue([]);
  vi.spyOn(api, 'listDiagnostics').mockResolvedValue([]);
}

function mockDevice() {
  return vi.spyOn(api, 'getDevice').mockImplementation(async (id) => ({
    device: {
      id,
      driver: 'openwrt',
      target_id: 't1',
      name: 'Rack Switch',
      role: 'switch',
      macs: [],
      firmware: {},
      uptime_sec: 10,
      online: true,
      last_seen: '2026-01-01T00:00:00Z',
      capabilities: [],
      system_stats: {},
      sensors: [],
    },
    links: [],
    lag_groups: [],
    segments: [],
    zones: [],
    gateways: [],
    routes: [],
    vpns: [],
    radios: [],
    wireless_uplinks: [],
    clients: [],
    recent_diagnostics: [],
  }));
}

function renderDeviceDetail(rawId: string) {
  mockFleet();
  setRoutePrefix('/network');
  window.history.replaceState({}, '', devicePath(rawId));
  render(
    <MantineProvider theme={testTheme}>
      <BrowserRouter>
        <Shell onSessionLost={() => {}} />
      </BrowserRouter>
    </MantineProvider>,
  );
}

describe('Ingress base href routing', () => {
  it('resolves basename and API paths from Home Assistant Ingress <base href>', () => {
    setBaseHref('/api/hassio_ingress/tok_abc123/');
    window.history.replaceState({}, '', '/api/hassio_ingress/tok_abc123/vlans/flow');

    expect(getRouterBasename()).toBe('/api/hassio_ingress/tok_abc123');
    expect(apiBasePath()).toBe('/api/hassio_ingress/tok_abc123/');
    expect(apiUrl('/vlans/flow')).toBe('/api/hassio_ingress/tok_abc123/api/v1/vlans/flow');
  });

  it('resolves the root from the relative <base href> the server sets on nested routes', () => {
    setBaseHref('../');
    window.history.replaceState({}, '', '/devices/sw-core-rack');
    expect(getRouterBasename()).toBe('/');
    expect(apiUrl('/devices')).toBe('/api/v1/devices');

    // Behind a reverse proxy on a sub-path, the same relative base lands on the prefix.
    window.history.replaceState({}, '', '/netplane/devices/sw-core-rack');
    expect(getRouterBasename()).toBe('/netplane');
    expect(apiBasePath()).toBe('/netplane/');
    expect(apiUrl('/devices')).toBe('/netplane/api/v1/devices');
  });

  it('puts the app at the root when there is no <base> (the Vite dev server)', () => {
    setBaseHref(null);
    window.history.replaceState({}, '', '/devices/sw-1');
    expect(getRouterBasename()).toBe('/');
  });

  it('routes screens and prefixes every navigation link under an Ingress base href', () => {
    mockFleet();
    const prefix = '/api/hassio_ingress/ha_token_99';
    setBaseHref(`${prefix}/`);
    window.history.replaceState({}, '', `${prefix}/network/vlans/flow`);

    const basename = getRouterBasename();
    expect(basename).toBe(prefix);

    render(
      <MantineProvider theme={testTheme}>
        <BrowserRouter basename={basename}>
          <Shell onSessionLost={() => {}} />
        </BrowserRouter>
      </MantineProvider>,
    );

    // Renders the VLAN Flow screen matched under the Ingress prefix.
    expect(screen.getByRole('heading', { name: 'VLAN Flow' })).toBeInTheDocument();

    // Sidebar links stay scoped to the Ingress basename.
    const topoLink = screen.getByRole('link', { name: 'Topology' });
    expect(topoLink).toHaveAttribute('href', `${prefix}/network/topology`);

    const devicesLink = screen.getByRole('link', { name: 'Devices' });
    expect(devicesLink).toHaveAttribute('href', `${prefix}/network/devices`);

    fireEvent.click(screen.getByRole('button', { name: 'Diagnostics' }));
    const diagLink = screen.getByRole('link', { name: 'Tools & events' });
    expect(diagLink).toHaveAttribute('href', `${prefix}/network/diagnostics`);
  });

  it('does not double-decode device IDs containing % in DeviceDetailScreen', async () => {
    const rawId = 'sw%100-rack';
    const spy = mockDevice();
    renderDeviceDetail(rawId);
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: 'Rack Switch' })).toBeInTheDocument();
    });
    expect(spy).toHaveBeenCalledWith(rawId);
  });

  it.each(['rack%2F1', 'rack/1'])('keeps device ID %s distinct from its encoded twin', async (rawId) => {
    const spy = mockDevice();
    renderDeviceDetail(rawId);
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: 'Rack Switch' })).toBeInTheDocument();
    });
    expect(spy).toHaveBeenCalledWith(rawId);
  });

  it('keeps API paths on the root pinned at startup after the route changes', () => {
    setBaseHref('./');
    window.history.replaceState({}, '', '/netplane/');
    expect(pinAppRoot()).toBe('/netplane');
    window.history.pushState({}, '', '/netplane/devices/sw-core-rack');
    expect(apiUrl('/state')).toBe('/netplane/api/v1/state');
  });

  it('supports embedded Andaru route and API prefixes with CSRF and 401 callback', async () => {
    setRoutePrefix('/network');
    const onUnauthorized = vi.fn();
    configureNetplaneApi({ apiPrefix: 'api/v1/fleet', csrfToken: 'tok-csrf', onUnauthorized });
    try {
      expect(netplanePath('/topology')).toBe('/network/topology');
      expect(devicePath('sw/1', 'ge-0/1')).toBe('/network/devices/sw%2F1?port=ge-0%2F1');
      expect(deviceIdFromPath('/network/devices/sw%2F1')).toBe('sw/1');
      expect(vlanFlowPath({ vlanId: 20 })).toBe('/network/vlans/flow?vlan=20');
      expect(apiUrl('/topology')).toBe('/api/v1/fleet/topology');

      const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(
        new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
      );
      await api.pollTarget('sw-1');
      expect(fetchSpy).toHaveBeenCalledWith(
        '/api/v1/fleet/targets/sw-1/poll',
        expect.objectContaining({
          method: 'POST',
          headers: expect.objectContaining({ 'X-Andaru-CSRF': 'tok-csrf' }),
        }),
      );

      const promptSpy = vi.spyOn(window, 'prompt').mockReturnValue('should-not-be-called');
      fetchSpy.mockResolvedValueOnce(new Response('unauthorized', { status: 401 }));
      await expect(api.getState()).rejects.toThrow('HTTP 401');
      expect(onUnauthorized).toHaveBeenCalledTimes(1);
      expect(promptSpy).not.toHaveBeenCalled();

      // A 401 arriving after configureNetplaneApi(null) must still use the request's snapshot config, never prompt.
      let resolveDelayed!: (r: Response) => void;
      fetchSpy.mockImplementationOnce(() => new Promise<Response>((r) => { resolveDelayed = r; }));
      const pending = api.getState();
      configureNetplaneApi(null);
      resolveDelayed(new Response('unauthorized', { status: 401 }));
      await expect(pending).rejects.toThrow('HTTP 401');
      expect(onUnauthorized).toHaveBeenCalledTimes(2);
      expect(promptSpy).not.toHaveBeenCalled();

      // Structured JSON error responses surface their message field instead of raw JSON.
      fetchSpy.mockResolvedValueOnce(
        new Response(JSON.stringify({ error: { code: 'FLEET_UNAVAILABLE', message: 'Fleet subsystem disabled.' } }), { status: 503 }),
      );
      await expect(api.getState()).rejects.toThrow('Fleet subsystem disabled.');
    } finally {
      configureNetplaneApi(null);
      setRoutePrefix('');
    }
  });
});
