import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render } from '../test/render';
import type { SavedObject, Snapshot, Zone } from '../types';
import { ZonesScreen } from './ZonesScreen';

afterEach(() => {
  vi.restoreAllMocks();
});

const savedZones: Array<SavedObject<Zone>> = [
  { value: { id: 'zon_wan', name: 'WAN', description: 'Upstream internet' }, version: '1' },
  { value: { id: 'zon_lan', name: 'LAN', description: 'Trusted internal' }, version: '2' },
  { value: { id: 'zon_dmz', name: 'DMZ', description: 'Public services', color: 'violet' }, version: '3' },
  { value: { id: 'zon_unused', name: 'Lab' }, version: '4' },
];

const sampleSnapshot: Snapshot = {
  state: 'saved',
  savedVersion: '9',
  value: {
    links: [{ id: 'lnk_1', name: 'eth0', kind: 'ethernet', enabled: true, mtu: 1500 }],
    segments: [
      { id: 'seg_lan', name: 'LAN', link: 'lnk_1', zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } } },
    ],
    zones: savedZones.map((z) => z.value),
    rules: [
      { id: 'rul_1', name: 'Allow web', enabled: true, action: 'accept', from: { zones: ['zon_lan'], addresses: [] }, to: { zones: ['zon_wan'], addresses: [] }, ports: [], log: false },
    ],
    nat: {
      portForwards: [
        { id: 'pfw_1', name: 'HTTPS', enabled: true, ingressZones: ['zon_wan'], protocol: 'tcp', from: [], to: { self: true }, ports: '443', target: { address: '10.0.0.10' } },
      ],
      oneToOne: [
        { id: 'o2o_1', name: 'Mail host', enabled: true, zone: 'zon_dmz', external: '203.0.113.10', internal: '10.0.2.10', destination: [] },
      ],
    },
  },
};

function renderZones(caps = ['configuration.read', 'configuration.write']) {
  const onSaved = vi.fn();
  const onSessionLost = vi.fn();
  render(
    <MemoryRouter>
      <ZonesScreen api={new ApiClient('csrf')} session={session(caps)} onSaved={onSaved} onSessionLost={onSessionLost} />
    </MemoryRouter>,
  );
  return { onSaved, onSessionLost };
}

describe('ZonesScreen', () => {
  it('lists zones alphabetically with colour labels and usage references, and filters rows', async () => {
    const user = userEvent.setup();
    fetchRoutes({
      '/api/v1/config/zones': { state: 'saved', savedVersion: '9', items: savedZones },
      '/api/v1/config': sampleSnapshot,
    });
    renderZones();

    expect(await screen.findByText('Public services')).toBeInTheDocument();
    expect(screen.getByText('Violet')).toBeInTheDocument();
    expect(screen.getByText('Automatic (Green)')).toBeInTheDocument();
    expect(screen.getByText('Interface LAN')).toBeInTheDocument();
    expect(screen.getAllByText('Rule 1 (Allow web)').length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('Port forward 1 (HTTPS)')).toBeInTheDocument();
    expect(screen.getByText('1:1 NAT 1 (Mail host)')).toBeInTheDocument();
    expect(screen.getByText('Not used')).toBeInTheDocument();

    await user.type(screen.getByLabelText('Filter zones'), 'violet');
    expect(screen.getByText('Public services')).toBeInTheDocument();
    expect(screen.queryByText('Upstream internet')).not.toBeInTheDocument();
  });

  it('adds a zone with an explicit colour, rejects duplicate names, and edits an existing zone with If-Match', async () => {
    const user = userEvent.setup();
    let postedBody: Record<string, unknown> | undefined;
    let putBody: Record<string, unknown> | undefined;
    let putHeaders: HeadersInit | undefined;
    fetchRoutes({
      '/api/v1/config/zones': { state: 'saved', savedVersion: '9', items: savedZones },
      '/api/v1/config': sampleSnapshot,
      'POST /api/v1/config/zones': (init) => {
        postedBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(201, { value: { id: 'zon_iot', ...postedBody }, version: '10' });
      },
      'PUT /api/v1/config/zones/zon_dmz': (init) => {
        putHeaders = init?.headers;
        putBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(200, { value: { id: 'zon_dmz', ...putBody }, version: '11' });
      },
    });
    const { onSaved } = renderZones();

    await user.click(await screen.findByRole('button', { name: 'Add zone' }));
    const addDrawer = await screen.findByRole('dialog', { name: 'Add zone' });
    await user.type(within(addDrawer).getByLabelText(/^Name/), 'LAN');
    await user.click(within(addDrawer).getByRole('button', { name: 'Add zone' }));
    expect(await within(addDrawer).findByText('Another zone has this name.')).toBeInTheDocument();

    await user.clear(within(addDrawer).getByLabelText(/^Name/));
    await user.type(within(addDrawer).getByLabelText(/^Name/), 'IoT');
    await user.type(within(addDrawer).getByLabelText('Description'), 'Isolated sensors');
    await user.click(within(addDrawer).getByRole('radio', { name: 'Orange' }));
    await user.click(within(addDrawer).getByRole('button', { name: 'Add zone' }));

    await waitFor(() => expect(postedBody).toEqual({
      name: 'IoT',
      description: 'Isolated sensors',
      color: 'orange',
    }));
    expect(onSaved).toHaveBeenCalledTimes(1);

    // Edit existing DMZ zone: rename and reset colour to Automatic (omits color).
    await user.click(screen.getByRole('button', { name: 'Edit zone DMZ' }));
    const editDrawer = await screen.findByRole('dialog', { name: 'Edit zone: DMZ' });
    await user.clear(within(editDrawer).getByLabelText(/^Name/));
    await user.type(within(editDrawer).getByLabelText(/^Name/), 'Servers');
    await user.click(within(editDrawer).getByRole('radio', { name: 'Automatic' }));
    await user.click(within(editDrawer).getByRole('button', { name: 'Save zone' }));

    await waitFor(() => expect(putBody).toEqual({
      name: 'Servers',
      description: 'Public services',
    }));
    expect(new Headers(putHeaders).get('If-Match')).toBe('"zon_dmz.3"');
  });

  it('blocks deletion when a zone is referenced and deletes an unused zone with If-Match', async () => {
    const user = userEvent.setup();
    let deleteHeaders: HeadersInit | undefined;
    fetchRoutes({
      '/api/v1/config/zones': { state: 'saved', savedVersion: '9', items: savedZones },
      '/api/v1/config': sampleSnapshot,
      'DELETE /api/v1/config/zones/zon_unused': (init) => {
        deleteHeaders = init?.headers;
        return jsonResponse(200, { state: 'saved', savedVersion: '10' });
      },
    });
    const { onSaved } = renderZones();

    // Used zone shows blockers and refuses deletion.
    await user.click(await screen.findByRole('button', { name: 'Delete zone DMZ' }));
    const blocked = await screen.findByRole('dialog', { name: 'Delete zone' });
    expect(blocked).toHaveTextContent('cannot be deleted while these name it');
    expect(blocked).toHaveTextContent('1:1 NAT 1 (Mail host)');
    await user.click(within(blocked).getByRole('button', { name: 'Keep it' }));

    // Unused zone deletes with If-Match header.
    await user.click(screen.getByRole('button', { name: 'Delete zone Lab' }));
    const confirm = await screen.findByRole('dialog', { name: 'Delete zone' });
    await user.click(within(confirm).getByRole('button', { name: 'Delete zone' }));

    await waitFor(() => expect(deleteHeaders).toBeDefined());
    expect(new Headers(deleteHeaders).get('If-Match')).toBe('"zon_unused.4"');
    expect(onSaved).toHaveBeenCalledTimes(1);
  });

  it('hides write controls for read-only sessions', async () => {
    fetchRoutes({
      '/api/v1/config/zones': { state: 'saved', savedVersion: '9', items: savedZones },
      '/api/v1/config': sampleSnapshot,
    });
    renderZones(['configuration.read']);

    expect(await screen.findByText('Public services')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add zone' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit zone DMZ' })).not.toBeInTheDocument();
  });
});
