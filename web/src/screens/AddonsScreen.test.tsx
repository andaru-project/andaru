import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen, within } from '../test/render';
import type { AddonListResponse } from '../types';
import { AddonsScreen } from './AddonsScreen';

const noopSessionLost = () => {};

function LocationDisplay() {
  const { pathname } = useLocation();
  return <div data-testid="location">{pathname}</div>;
}

const sampleDigest = 'ghcr.io/tailscale/tailscale@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';
const oldDigest = 'ghcr.io/example/sample@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';
const newDigest = 'ghcr.io/example/sample@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb';

const listFixture: AddonListResponse = {
  safeMode: { active: false, reasons: [] },
  items: [
    {
      id: 'org.andaru.tailscale',
      slug: 'tailscale',
      name: 'Tailscale',
      description: 'Mesh VPN built on **WireGuard**.',
      version: '1.0.0',
      application: { name: 'Tailscale', version: '1.80.0' },
      catalogImage: sampleDigest,
      installed: false,
      hasWebUI: false,
      unreviewedBusCount: 0,
      staged: { present: false, staging: false, digest: sampleDigest },
      runtime: { applied: false, activeState: 'not-applied', health: 'none', updateAvailable: false },
    },
    {
      id: 'org.andaru.sample',
      slug: 'sample',
      name: 'Sample DNS',
      description: 'DNS filter add-on.',
      version: '1.1.0',
      application: { name: 'SampleDNS', version: '2.1.0' },
      catalogImage: newDigest,
      installed: true,
      hasWebUI: true,
      unreviewedBusCount: 2,
      staged: { present: false, staging: false, digest: newDigest },
      runtime: { applied: true, activeState: 'active', health: 'healthy', appliedImage: oldDigest, updateAvailable: true },
      saved: { id: 'org.andaru.sample', enabled: true },
    },
  ],
};

it('stages image and navigates on Install, and confirms Uninstall with optimistic locking', async () => {
  const onSaved = vi.fn();
  const stageCalls: string[] = [];
  const deleteHeaders: Array<string | null> = [];

  fetchRoutes({
    '/api/v1/addons': listFixture,
    'POST /api/v1/addons/tailscale/stage': (init) => {
      stageCalls.push(new Headers(init?.headers).get('X-Andaru-CSRF') ?? '');
      return new Response(JSON.stringify({ present: false, staging: true, digest: sampleDigest }), {
        status: 202,
        headers: { 'Content-Type': 'application/json' },
      });
    },
    '/api/v1/config/addons/org.andaru.sample': {
      value: { id: 'org.andaru.sample', enabled: true },
      version: '7',
    },
    'DELETE /api/v1/config/addons/org.andaru.sample': (init) => {
      deleteHeaders.push(new Headers(init?.headers).get('If-Match'));
      return new Response(null, { status: 204 });
    },
  });

  render(
    <MemoryRouter initialEntries={['/system/addons']}>
      <Routes>
        <Route
          path="/system/addons"
          element={
            <AddonsScreen
              api={new ApiClient('csrf-tok')}
              session={session(['addons.read', 'addons.manage', 'configuration.read', 'configuration.write'])}
              onSaved={onSaved}
              onSessionLost={noopSessionLost}
            />
          }
        />
        <Route path="/system/addons/:slug" element={<LocationDisplay />} />
      </Routes>
    </MemoryRouter>,
  );

  expect(await screen.findByText('Tailscale')).toBeInTheDocument();
  expect(screen.getByText('Sample DNS')).toBeInTheDocument();
  expect(screen.getByText('Update available')).toBeInTheDocument();
  expect(screen.getByText('2 bus permissions to review')).toBeInTheDocument();

  // Uninstall flow with explicit confirmation modal.
  await userEvent.click(screen.getByRole('button', { name: 'Uninstall' }));
  const dialog = await screen.findByRole('dialog', { name: 'Uninstall Sample DNS' });
  expect(within(dialog).getByText(/Review and apply/)).toBeInTheDocument();
  await userEvent.click(within(dialog).getByRole('button', { name: 'Uninstall' }));
  expect(await screen.findByText(/Removed Sample DNS from saved configuration/)).toBeInTheDocument();
  expect(deleteHeaders).toEqual(['"org.andaru.sample.7"']);
  expect(onSaved).toHaveBeenCalledTimes(1);

  // Stage image and continue navigates to /system/addons/tailscale.
  await userEvent.click(screen.getByRole('button', { name: 'Stage image and continue' }));
  expect(await screen.findByTestId('location')).toHaveTextContent('/system/addons/tailscale');
  expect(stageCalls).toEqual(['csrf-tok']);
});

it('shows read-only catalog without hitting /api/v1/config for addons.read-only users', async () => {
  const fetchMock = fetchRoutes({
    '/api/v1/addons': {
      ...listFixture,
      safeMode: { active: true, reasons: ['kernelArg'] },
      items: listFixture.items.map((item) => ({ ...item, saved: undefined })),
    },
  });

  render(
    <MemoryRouter initialEntries={['/system/addons']}>
      <AddonsScreen
        api={new ApiClient('csrf-tok')}
        session={session(['addons.read'])}
        onSaved={() => {}}
        onSessionLost={noopSessionLost}
      />
    </MemoryRouter>,
  );

  expect(await screen.findByText('Safe mode active')).toBeInTheDocument();
  expect(screen.getByText('Tailscale')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Stage image and continue' })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Uninstall' })).not.toBeInTheDocument();

  const calledUrls = fetchMock.mock.calls.map(([input]) => String(input));
  expect(calledUrls.some((url) => url.startsWith('/api/v1/config'))).toBe(false);
});
