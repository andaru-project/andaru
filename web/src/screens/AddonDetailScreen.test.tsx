import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen } from '../test/render';
import type { AddonDetailResponse } from '../types';
import { AddonDetailScreen } from './AddonDetailScreen';

const noopSessionLost = () => {};
const sampleDigest = 'ghcr.io/example/sample@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';

function makeDetail(overrides: Partial<AddonDetailResponse> = {}): AddonDetailResponse {
  return {
    id: 'org.andaru.sample',
    slug: 'sample',
    name: 'Sample Add-on',
    description: 'Provides **mesh** routing.',
    version: '1.0.0',
    application: { name: 'SampleApp', version: '2.0.0' },
    catalogImage: sampleDigest,
    installed: true,
    hasWebUI: true,
    unreviewedBusCount: 1,
    staged: { present: true, staging: false, digest: sampleDigest },
    runtime: { applied: true, activeState: 'active', subState: 'running', health: 'healthy', appliedImage: sampleDigest, updateAvailable: false },
    safeMode: { active: false, reasons: [] },
    saved: {
      id: 'org.andaru.sample',
      enabled: true,
      settings: {
        uiHost: 'sample.lan',
        port: 8443,
        advertiseExit: false,
        mode: ['fast'],
        tags: ['edge'],
        routes: ['10.10.0.0/24'],
        lanZone: 'zone-lan',
        authKey: { secret: 'authKey', isSet: true, version: '2' },
      },
      interfaces: [{ name: 'sample0', zone: 'zone-vpn' }],
      busGrants: { publish: { 'notification/addon.sample': 'granted' } },
    },
    webui: {
      title: 'Sample Admin',
      path: '/app',
      hostname: 'sample.lan',
      url: 'https://sample.lan:8443/app',
      originConflict: false,
    },
    manifest: {
      manifestVersion: 1,
      id: 'org.andaru.sample',
      slug: 'sample',
      name: 'Sample Add-on',
      version: '1.0.0',
      application: { name: 'SampleApp', version: '2.0.0' },
      properties: [
        { name: 'uiHost', type: 'string', format: 'hostname', title: 'WebUI Hostname', description: 'Use a **separate** host. <script>bad()</script>' },
        { name: 'port', type: 'integer', minimum: 1024, maximum: 65535, title: 'Listen Port' },
        { name: 'advertiseExit', type: 'boolean', title: 'Advertise Exit Node' },
        {
          name: 'mode',
          type: 'enum',
          maxItems: 1,
          title: 'Routing Mode',
          options: [
            { value: 'fast', title: 'Fast path', description: 'Direct `kernel` forwarding.' },
            { value: 'safe', title: 'Safe path' },
          ],
        },
        {
          name: 'tags',
          type: 'enum',
          maxItems: 3,
          title: 'Tags',
          options: [
            { value: 'edge', title: 'Edge' },
            { value: 'core', title: 'Core' },
          ],
        },
        { name: 'routes', type: 'string-list', itemFormat: 'cidr', title: 'Advertised Routes' },
        { name: 'lanZone', type: 'zone-reference', title: 'Primary Zone' },
        { name: 'authKey', type: 'secret-reference', title: 'Auth Key', description: 'Write-only key from [docs](https://example.com).' },
      ],
      interfaces: [{ name: 'sample0', title: 'Sample Tunnel', zoneRequired: true }],
      bus: {
        publish: [{ topic: 'notification/addon.sample', title: 'Alerts', description: 'Publishes add-on notifications.' }],
        subscribe: [{ topic: 'system/status', title: 'System Status' }],
      },
      logs: [{ name: 'runtime', title: 'Runtime journal', permission: 'logs', source: { type: 'runtime' } }],
    },
    ...overrides,
  };
}

it('renders all declarative property types, aria-describedby markdown, write-only secrets, logs, and saves with If-Match', async () => {
  const detail = makeDetail();
  const onSaved = vi.fn();
  const savedPayloads: Array<{ ifMatch: string | null; body: unknown }> = [];

  fetchRoutes({
    '/api/v1/addons/sample': detail,
    '/api/v1/config/zones': {
      items: [
        { value: { id: 'zone-lan', name: 'LAN', inputDefault: 'drop', forwardDefault: 'drop' }, version: '1' },
        { value: { id: 'zone-vpn', name: 'VPN', inputDefault: 'drop', forwardDefault: 'drop' }, version: '1' },
      ],
    },
    '/api/v1/config/addons/org.andaru.sample': { value: detail.saved, version: '4' },
    '/api/v1/addons/sample/logs/runtime?lines=200': {
      name: 'runtime',
      format: 'text',
      lines: ['started sample', 'ready'],
      truncated: false,
    },
    'PUT /api/v1/config/addons/org.andaru.sample': (init) => {
      savedPayloads.push({
        ifMatch: new Headers(init?.headers).get('If-Match'),
        body: JSON.parse(String(init?.body ?? '{}')),
      });
      return new Response(JSON.stringify({ value: detail.saved, version: '5' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    },
  });

  const { container } = render(
    <MemoryRouter initialEntries={['/system/addons/sample']}>
      <Routes>
        <Route
          path="/system/addons/:slug"
          element={
            <AddonDetailScreen
              api={new ApiClient('csrf-tok')}
              session={session(['addons.read', 'addons.manage', 'configuration.read', 'configuration.write'])}
              onSaved={onSaved}
              onSessionLost={noopSessionLost}
            />
          }
        />
      </Routes>
    </MemoryRouter>,
  );

  // Declarative properties + sanitized markdown + aria-describedby.
  const hostInput = await screen.findByLabelText('WebUI Hostname');
  expect(hostInput).toHaveValue('sample.lan');
  expect(hostInput).toHaveAttribute('aria-describedby', 'addon-prop-desc-uiHost');
  expect(container.querySelector('script')).toBeNull();
  expect(screen.getByText(/<script>bad\(\)<\/script>/)).toBeInTheDocument();

  // Enum option description is shown for the selected option.
  expect(screen.getByText('kernel').tagName).toBe('CODE');

  // Write-only secret never exposes stored value.
  const secretInput = screen.getByLabelText('Auth Key');
  expect(secretInput).toHaveValue('');
  expect(screen.getByText(/Stored securely \(authKey\)/)).toBeInTheDocument();

  // Separate external WebUI link opens in new tab with noopener noreferrer.
  const webuiLink = screen.getByRole('link', { name: /Open Sample Admin/ });
  expect(webuiLink).toHaveAttribute('href', 'https://sample.lan:8443/app');
  expect(webuiLink).toHaveAttribute('target', '_blank');
  expect(webuiLink).toHaveAttribute('rel', 'noopener noreferrer');

  // Logs rendered cleanly.
  expect(await screen.findByText(/started sample/)).toBeInTheDocument();

  // Type a new secret value and save.
  await userEvent.type(secretInput, 'tskey-new-secret');
  await userEvent.click(screen.getByRole('button', { name: 'Save settings' }));
  expect(await screen.findByText(/Saved add-on configuration/)).toBeInTheDocument();

  expect(onSaved).toHaveBeenCalledTimes(1);
  expect(savedPayloads).toHaveLength(1);
  expect(savedPayloads[0].ifMatch).toBe('"org.andaru.sample.4"');
  expect(savedPayloads[0].body).toMatchObject({
    id: 'org.andaru.sample',
    enabled: true,
    settings: {
      uiHost: 'sample.lan',
      port: 8443,
      advertiseExit: false,
      mode: ['fast'],
      tags: ['edge'],
      routes: ['10.10.0.0/24'],
      lanZone: 'zone-lan',
      authKey: { secret: 'authKey', set: 'tskey-new-secret', expectedVersion: '2' },
    },
    interfaces: [{ name: 'sample0', zone: 'zone-vpn' }],
    busGrants: { publish: { 'notification/addon.sample': 'granted' } },
  });
});

it('suppresses WebUI link on server multi-origin conflict', async () => {
  const conflictDetail = makeDetail({
    webui: {
      title: 'Sample Admin',
      path: '/app',
      hostname: 'admin.corp',
      originConflict: true,
      conflictReason: 'Configured hostname matches a configured Andaru management origin.',
    },
  });

  fetchRoutes({
    '/api/v1/addons/sample': conflictDetail,
    '/api/v1/addons/sample/logs/runtime?lines=200': { name: 'runtime', format: 'text', lines: [], truncated: false },
  });

  render(
    <MemoryRouter initialEntries={['/system/addons/sample']}>
      <Routes>
        <Route
          path="/system/addons/:slug"
          element={
            <AddonDetailScreen
              api={new ApiClient('csrf-tok')}
              session={session(['addons.read'])}
              onSaved={() => {}}
              onSessionLost={noopSessionLost}
            />
          }
        />
      </Routes>
    </MemoryRouter>,
  );

  expect(await screen.findByText('Separate origin required')).toBeInTheDocument();
  expect(screen.queryByRole('link', { name: /Open Sample Admin/ })).not.toBeInTheDocument();
});

it('suppresses WebUI link client-side when URL hostname matches window.location.hostname', async () => {
  const sameHostDetail = makeDetail({
    webui: {
      title: 'Sample Admin',
      path: '/app',
      hostname: window.location.hostname,
      url: `https://${window.location.hostname}:8443/app`,
      originConflict: false,
    },
  });

  fetchRoutes({
    '/api/v1/addons/sample': sameHostDetail,
    '/api/v1/addons/sample/logs/runtime?lines=200': { name: 'runtime', format: 'text', lines: [], truncated: false },
  });

  render(
    <MemoryRouter initialEntries={['/system/addons/sample']}>
      <Routes>
        <Route
          path="/system/addons/:slug"
          element={
            <AddonDetailScreen
              api={new ApiClient('csrf-tok')}
              session={session(['addons.read'])}
              onSaved={() => {}}
              onSessionLost={noopSessionLost}
            />
          }
        />
      </Routes>
    </MemoryRouter>,
  );

  expect(await screen.findByText('Separate origin required')).toBeInTheDocument();
  expect(screen.queryByRole('link', { name: /Open Sample Admin/ })).not.toBeInTheDocument();
});

it('sends expectedVersion "0" for unconfigured secrets and invokes onSessionLost on 401 save', async () => {
  const freshDetail = makeDetail({ installed: false, saved: undefined });
  const createBodies: unknown[] = [];
  const onSessionLost = vi.fn();

  fetchRoutes({
    '/api/v1/addons/sample': freshDetail,
    '/api/v1/config/zones': {
      items: [{ value: { id: 'zone-lan', name: 'LAN', inputDefault: 'drop', forwardDefault: 'drop' }, version: '1' }],
    },
    '/api/v1/addons/sample/logs/runtime?lines=200': { name: 'runtime', format: 'text', lines: [], truncated: false },
    'POST /api/v1/config/addons': (init) => {
      createBodies.push(JSON.parse(String(init?.body ?? '{}')));
      return new Response(JSON.stringify({ error: { code: 'UNAUTHORIZED', message: 'Session expired.' } }), {
        status: 401,
        headers: { 'Content-Type': 'application/json' },
      });
    },
  });

  render(
    <MemoryRouter initialEntries={['/system/addons/sample']}>
      <Routes>
        <Route
          path="/system/addons/:slug"
          element={
            <AddonDetailScreen
              api={new ApiClient('csrf-tok')}
              session={session(['addons.read', 'configuration.read', 'configuration.write'])}
              onSaved={() => {}}
              onSessionLost={onSessionLost}
            />
          }
        />
      </Routes>
    </MemoryRouter>,
  );

  const secretInput = await screen.findByLabelText('Auth Key');
  await userEvent.type(secretInput, 'brand-new-secret');
  await userEvent.click(screen.getByRole('button', { name: 'Install and save' }));

  expect(createBodies).toHaveLength(1);
  expect(createBodies[0]).toMatchObject({
    id: 'org.andaru.sample',
    settings: {
      authKey: { secret: 'authKey', set: 'brand-new-secret', expectedVersion: '0' },
    },
  });
  expect(onSessionLost).toHaveBeenCalledTimes(1);
});

it('allows addons.read-only users to view detail and logs with zero /api/v1/config calls', async () => {
  const detail = makeDetail({ saved: undefined });
  const fetchMock = fetchRoutes({
    '/api/v1/addons/sample': detail,
    '/api/v1/addons/sample/logs/runtime?lines=200': {
      name: 'runtime',
      format: 'text',
      lines: ['log line 1'],
      truncated: false,
    },
  });

  render(
    <MemoryRouter initialEntries={['/system/addons/sample']}>
      <Routes>
        <Route
          path="/system/addons/:slug"
          element={
            <AddonDetailScreen
              api={new ApiClient('csrf-tok')}
              session={session(['addons.read'])}
              onSaved={() => {}}
              onSessionLost={noopSessionLost}
            />
          }
        />
      </Routes>
    </MemoryRouter>,
  );

  expect(await screen.findByText('Sample Add-on')).toBeInTheDocument();
  expect(await screen.findByText('log line 1')).toBeInTheDocument();
  expect(screen.queryByText('Configuration')).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Restart' })).not.toBeInTheDocument();

  const calledUrls = fetchMock.mock.calls.map(([input]) => String(input));
  expect(calledUrls.some((url) => url.startsWith('/api/v1/config'))).toBe(false);
});

it('loads managed certificates and saves certificate-reference properties as plain cert IDs', async () => {
  const user = userEvent.setup();
  let savedPayload: unknown;
  const detail = makeDetail();
  detail.manifest.properties = [
    { name: 'tlsCert', type: 'certificate-reference', required: true, title: 'TLS Certificate' },
  ];
  detail.saved = { id: 'org.andaru.sample', enabled: true, settings: {} };

  fetchRoutes({
    '/api/v1/addons/sample': detail,
    '/api/v1/addons/sample/logs/runtime?lines=200': { name: 'runtime', format: 'text', lines: [], truncated: false },
    '/api/v1/config/zones': { state: 'saved', savedVersion: '1', items: [] },
    '/api/v1/config/addons/org.andaru.sample': { state: 'saved', savedVersion: '1', version: '3', value: detail.saved },
    '/api/v1/config/pki/certificates': {
      state: 'saved',
      savedVersion: '1',
      items: [
        {
          state: 'saved',
          savedVersion: '1',
          version: '1',
          value: {
            id: 'crt_edge',
            name: 'Edge TLS',
            subject: 'CN=edge.example.com',
            serial: '01',
            fingerprintSha256: '1234',
            notBefore: '2026-01-01T00:00:00Z',
            notAfter: '2027-01-01T00:00:00Z',
            certPem: '-----BEGIN CERTIFICATE-----\nX\n-----END CERTIFICATE-----',
            keyRef: { secret: 'crt_edge', isSet: true, version: '1' },
            keyAlgorithm: 'ecdsa-p256',
          },
        },
      ],
    },
    'PUT /api/v1/config/addons/org.andaru.sample': (init) => {
      savedPayload = JSON.parse(String(init?.body ?? '{}'));
      return { state: 'saved', savedVersion: '2', version: '4', value: savedPayload };
    },
  });

  render(
    <MemoryRouter initialEntries={['/system/addons/sample']}>
      <Routes>
        <Route
          path="/system/addons/:slug"
          element={
            <AddonDetailScreen
              api={new ApiClient('csrf-tok')}
              session={session(['addons.read', 'addons.manage', 'configuration.read', 'configuration.write'])}
              onSaved={() => {}}
              onSessionLost={noopSessionLost}
            />
          }
        />
      </Routes>
    </MemoryRouter>,
  );

  const select = await screen.findByLabelText(/^TLS Certificate/, { selector: 'input' });
  await user.click(select);
  await user.click(await screen.findByRole('option', { name: /Edge TLS/ }));
  await user.click(screen.getByRole('button', { name: 'Save settings' }));

  await vi.waitFor(() => expect(savedPayload).toMatchObject({
    id: 'org.andaru.sample',
    enabled: true,
    settings: { tlsCert: 'crt_edge' },
  }));
});
