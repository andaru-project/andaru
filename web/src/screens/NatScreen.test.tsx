import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router';
import { ApiClient, ApiError } from '../api';
import {
  checkOneToOne, checkPortForward, oneToOneErrors, oneToOnePayload, parsePortSpan, portForwardDraftOf, portForwardErrors, portForwardPayload,
} from '../natEdit';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render } from '../test/render';
import type { OneToOneNat, OutboundNat, PortForward, Snapshot } from '../types';
import { NatScreen } from './NatScreen';

const rwCaps = ['configuration.read', 'configuration.write'];
const roCaps = ['configuration.read'];

const sampleSnapshot: Snapshot = {
  state: 'saved',
  savedVersion: '5',
  value: {
    links: [],
    segments: [{
      id: 'seg_lan',
      name: 'lan',
      link: 'lnk_lan',
      zone: 'zon_lan',
      addressing: { ipv4: { mode: 'static', addresses: ['192.168.10.1/24'] }, ipv6: { mode: 'none' } },
    }],
    zones: [
      { id: 'zon_wan', name: 'wan' },
      { id: 'zon_lan', name: 'lan' },
    ],
    management: { segment: 'seg_lan' },
    aliases: {
      addresses: [{ id: 'adr_trusted', name: 'trusted-clients', kind: 'network', prefix: '203.0.113.0/24' }],
      ports: [],
    },
  },
};

const sampleForward: PortForward = {
  id: 'pfw_1',
  name: 'HTTPS to web',
  enabled: true,
  ingressZones: ['zon_wan'],
  protocol: 'tcp',
  from: [],
  to: { self: true },
  ports: '443',
  target: { address: '192.168.10.50' },
  targetPort: 8443,
  filterRule: 'auto',
  reflection: true,
};

const sampleOneToOne: OneToOneNat = {
  id: 'o2o_1',
  name: 'Mail server',
  enabled: true,
  zone: 'zon_wan',
  external: '203.0.113.50',
  internal: '192.168.10.60',
  destination: [],
};

function renderNat(caps = rwCaps, onSaved = vi.fn()) {
  const api = new ApiClient('');
  api.setCSRFToken('csrf');
  return render(
    <MemoryRouter>
      <NatScreen api={api} session={session(caps)} onSaved={onSaved} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
}

describe('NatScreen', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('lists port forwards, toggles enabled state, and adds a new port forward', async () => {
    let posted: Record<string, unknown> | undefined;
    let toggledBody: Record<string, unknown> | undefined;
    let toggledHeaders: HeadersInit | undefined;
    fetchRoutes({
      '/api/v1/config': sampleSnapshot,
      '/api/v1/config/nat/port-forwards': { state: 'saved', savedVersion: '5', version: '2', items: [{ value: sampleForward, version: '3' }] },
      '/api/v1/config/nat/one-to-one': { state: 'saved', savedVersion: '5', version: '1', items: [{ value: sampleOneToOne, version: '2' }] },
      '/api/v1/config/nat/outbound': { state: 'saved', savedVersion: '5', version: '1', value: { mode: 'automatic' } },
      'PUT /api/v1/config/nat/port-forwards/pfw_1': (init) => {
        toggledHeaders = init?.headers;
        toggledBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(200, { value: { id: 'pfw_1', ...toggledBody }, version: '4' });
      },
      'POST /api/v1/config/nat/port-forwards': (init) => {
        posted = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(201, { value: { id: 'pfw_2', ...posted }, version: '1' });
      },
    });
    const onSaved = vi.fn();
    renderNat(rwCaps, onSaved);

    expect(await screen.findByText('HTTPS to web')).toBeInTheDocument();
    expect(screen.getByText('192.168.10.50')).toBeInTheDocument();
    expect(screen.getByText(':8443')).toBeInTheDocument();
    expect(screen.getByText('Auto rule')).toBeInTheDocument();

    // Toggle existing forward off.
    fireEvent.click(screen.getByRole('switch', { name: 'Enable port forward 1, HTTPS to web' }));
    await waitFor(() => expect(toggledBody?.enabled).toBe(false));
    expect(new Headers(toggledHeaders).get('If-Match')).toBe('"pfw_1.3"');

    // Add a new port forward via drawer.
    fireEvent.click(screen.getByRole('button', { name: 'Add port forward' }));
    const drawer = await screen.findByRole('dialog', { name: 'Add port forward' });
    fireEvent.change(within(drawer).getByLabelText(/Name/), { target: { value: 'SSH jump' } });
    fireEvent.change(within(drawer).getByLabelText(/Destination port/), { target: { value: '2222' } });
    fireEvent.change(within(drawer).getByLabelText(/Target IPv4 address/), { target: { value: '192.168.10.20' } });
    fireEvent.change(within(drawer).getByLabelText(/Target port/), { target: { value: '22' } });
    fireEvent.click(within(drawer).getByRole('button', { name: 'Add port forward' }));

    await waitFor(() => expect(posted).toBeDefined());
    expect(posted).toEqual({
      name: 'SSH jump',
      enabled: true,
      ingressZones: ['zon_wan'],
      protocol: 'tcp',
      from: [],
      to: { self: true },
      ports: '2222',
      target: { address: '192.168.10.20' },
      targetPort: 22,
      filterRule: 'auto',
    });
  });

  it('deletes a 1:1 NAT entry with confirmation modal and If-Match header', async () => {
    let deleteHeaders: HeadersInit | undefined;
    fetchRoutes({
      '/api/v1/config': sampleSnapshot,
      '/api/v1/config/nat/port-forwards': { state: 'saved', savedVersion: '5', version: '1', items: [] },
      '/api/v1/config/nat/one-to-one': { state: 'saved', savedVersion: '5', version: '1', items: [{ value: sampleOneToOne, version: '2' }] },
      '/api/v1/config/nat/outbound': { state: 'saved', savedVersion: '5', version: '1', value: { mode: 'automatic' } },
      'DELETE /api/v1/config/nat/one-to-one/o2o_1': (init) => {
        deleteHeaders = init?.headers;
        return new Response(null, { status: 204 });
      },
    });
    const onSaved = vi.fn();
    renderNat(rwCaps, onSaved);

    fireEvent.click(await screen.findByRole('tab', { name: '1:1 NAT (1)' }));
    expect(screen.getByText('Mail server')).toBeInTheDocument();
    expect(screen.getByText('203.0.113.50')).toBeInTheDocument();
    expect(screen.getByText('192.168.10.60')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Delete 1:1 entry 1, Mail server' }));
    const modal = await screen.findByRole('dialog', { name: 'Delete 1:1 NAT entry' });
    fireEvent.click(within(modal).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(new Headers(deleteHeaders).get('If-Match')).toBe('"o2o_1.2"');
  });

  it('saves outbound NAT mode from unset 404 using If-Match nat.outbound.0', async () => {
    let putBody: OutboundNat | undefined;
    let putHeaders: HeadersInit | undefined;
    fetchRoutes({
      '/api/v1/config': sampleSnapshot,
      '/api/v1/config/nat/port-forwards': { state: 'saved', savedVersion: '5', version: '1', items: [] },
      '/api/v1/config/nat/one-to-one': { state: 'saved', savedVersion: '5', version: '1', items: [] },
      '/api/v1/config/nat/outbound': () => (putBody
        ? jsonResponse(200, { state: 'saved', savedVersion: '6', version: '1', value: putBody })
        : jsonResponse(404, { error: { code: 'NOT_FOUND', message: 'Unset.', details: [] } })),
      'PUT /api/v1/config/nat/outbound': (init) => {
        putHeaders = init?.headers;
        putBody = JSON.parse(String(init?.body)) as OutboundNat;
        return jsonResponse(200, { state: 'saved', savedVersion: '6', version: '1', value: putBody });
      },
    });
    const onSaved = vi.fn();
    renderNat(rwCaps, onSaved);

    fireEvent.click(await screen.findByRole('tab', { name: 'Outbound' }));
    fireEvent.click(screen.getByLabelText('Automatic masquerade'));
    fireEvent.click(screen.getByRole('button', { name: 'Save outbound NAT' }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(new Headers(putHeaders).get('If-Match')).toBe('"nat.outbound.0"');
    expect(putBody).toEqual({ mode: 'automatic' });
  });

  it('warns when management segment is unset and hides write actions for read-only accounts', async () => {
    fetchRoutes({
      '/api/v1/config': { ...sampleSnapshot, value: { ...sampleSnapshot.value, management: undefined } },
      '/api/v1/config/nat/port-forwards': { state: 'saved', savedVersion: '5', version: '1', items: [{ value: sampleForward, version: '3' }] },
      '/api/v1/config/nat/one-to-one': { state: 'saved', savedVersion: '5', version: '1', items: [] },
      '/api/v1/config/nat/outbound': { state: 'saved', savedVersion: '5', version: '1', value: { mode: 'automatic' } },
    });
    renderNat(roCaps);

    expect(await screen.findByText('No management segment is set')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add port forward' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit order' })).not.toBeInTheDocument();
  });

  it('edits an existing port forward, adds a 1:1 NAT entry, and handles outbound NAT 412 conflict', async () => {
    let putForwardHeaders: HeadersInit | undefined;
    let putForwardBody: Record<string, unknown> | undefined;
    let postedO2o: Record<string, unknown> | undefined;
    let outboundVersion = '1';
    fetchRoutes({
      '/api/v1/config': sampleSnapshot,
      '/api/v1/config/nat/port-forwards': { state: 'saved', savedVersion: '5', version: '2', items: [{ value: sampleForward, version: '3' }] },
      '/api/v1/config/nat/one-to-one': { state: 'saved', savedVersion: '5', version: '1', items: [] },
      '/api/v1/config/nat/outbound': () => jsonResponse(200, { state: 'saved', savedVersion: '5', version: outboundVersion, value: { mode: 'automatic' } }),
      'PUT /api/v1/config/nat/port-forwards/pfw_1': (init) => {
        putForwardHeaders = init?.headers;
        putForwardBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(200, { value: { id: 'pfw_1', ...putForwardBody }, version: '4' });
      },
      'POST /api/v1/config/nat/one-to-one': (init) => {
        postedO2o = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(201, { value: { id: 'o2o_new', ...postedO2o }, version: '1' });
      },
      'PUT /api/v1/config/nat/outbound': () => {
        outboundVersion = '2';
        return jsonResponse(412, { error: { code: 'CONFLICT', message: 'Stale version.', details: [] } });
      },
    });
    renderNat();

    // Edit existing port forward.
    fireEvent.click(await screen.findByRole('button', { name: 'Edit port forward 1, HTTPS to web' }));
    const pfDrawer = await screen.findByRole('dialog', { name: 'Edit port forward: HTTPS to web' });
    fireEvent.change(within(pfDrawer).getByLabelText(/Target port/), { target: { value: '9443' } });
    fireEvent.click(within(pfDrawer).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(putForwardBody?.targetPort).toBe(9443));
    expect(new Headers(putForwardHeaders).get('If-Match')).toBe('"pfw_1.3"');

    // Add a 1:1 NAT entry.
    fireEvent.click(screen.getByRole('tab', { name: '1:1 NAT (0)' }));
    fireEvent.click(screen.getByRole('button', { name: 'Add 1:1 entry' }));
    const o2oDrawer = await screen.findByRole('dialog', { name: 'Add 1:1 NAT entry' });
    fireEvent.change(within(o2oDrawer).getByLabelText(/Name/), { target: { value: 'DMZ host' } });
    fireEvent.change(within(o2oDrawer).getByLabelText(/Internal IPv4/), { target: { value: '192.168.10.70' } });
    fireEvent.change(within(o2oDrawer).getByLabelText(/External IPv4/), { target: { value: '203.0.113.70' } });
    fireEvent.click(within(o2oDrawer).getByRole('button', { name: 'Add 1:1 entry' }));
    await waitFor(() => expect(postedO2o).toEqual({
      name: 'DMZ host',
      enabled: true,
      zone: 'zon_wan',
      internal: '192.168.10.70',
      external: '203.0.113.70',
      destination: [],
    }));

    // Outbound NAT 412 conflict reloads and surfaces remote-changed warning.
    fireEvent.click(screen.getByRole('tab', { name: 'Outbound' }));
    fireEvent.click(screen.getByLabelText('Disabled (none)'));
    fireEvent.click(screen.getByRole('button', { name: 'Save outbound NAT' }));
    expect(await screen.findByText('Outbound NAT changed elsewhere')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save outbound NAT' })).toBeDisabled();

    // Returning to the saved mode cleans the draft and syncs its baseline to the reloaded version.
    fireEvent.click(screen.getByLabelText('Automatic masquerade'));
    expect(screen.queryByText('Outbound NAT changed elsewhere')).not.toBeInTheDocument();
    fireEvent.click(screen.getByLabelText('Disabled (none)'));
    expect(screen.queryByText('Outbound NAT changed elsewhere')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save outbound NAT' })).toBeEnabled();
  });
});

describe('natEdit helpers', () => {
  it('defaults IPv6 noRedirect drafts to targetKind self and routes self-mode /to 422s to banner', () => {
    const v6Excluded: PortForward = {
      id: 'pfw_v6',
      name: 'v6 bypass',
      enabled: true,
      family: 'ipv6',
      ingressZones: ['zon_wan'],
      protocol: 'tcp',
      from: [],
      to: { self: true },
      ports: '443',
      noRedirect: true,
    };
    const draft = portForwardDraftOf(v6Excluded);
    expect(draft.targetKind).toBe('self');

    const err = new ApiError(422, 'VALIDATION_FAILED', 'Validation failed.', [
      { path: '/nat/portForwards/0/to', message: 'Destination refused.' },
    ]);
    expect(portForwardErrors(err, draft).get('')).toBe('Destination refused.');
  });
  it('parses port spans strictly and rejects shifted overflow', () => {
    expect(parsePortSpan('443')).toEqual({ first: 443, last: 443 });
    expect(parsePortSpan('8000-8080')).toEqual({ first: 8000, last: 8080 });
    expect(parsePortSpan('080')).toBeNull();
    expect(parsePortSpan('9000-8000')).toBeNull();
    expect(parsePortSpan('0-80')).toBeNull();

    const draft = { ...portForwardDraftOf(sampleForward), ports: '65500-65535', targetPort: '65510' };
    expect(checkPortForward(draft).get('targetPort')).toBe('The shifted range would end past port 65535.');
  });

  it('strips incompatible fields on noRedirect and noBinat payloads and maps 422 errors safely', () => {
    const excludedPf = { ...portForwardDraftOf(sampleForward), noRedirect: true };
    expect(portForwardPayload(excludedPf)).toEqual({
      name: 'HTTPS to web',
      enabled: true,
      ingressZones: ['zon_wan'],
      protocol: 'tcp',
      from: [],
      to: { self: true },
      ports: '443',
      noRedirect: true,
    });

    const excludedO2o = {
      name: 'Exempt host',
      description: '',
      enabled: true,
      zone: 'zon_wan',
      noBinat: true,
      external: '203.0.113.50',
      internal: '192.168.10.60',
      destination: ['adr_trusted'],
      destinationNegated: true,
      reflection: true,
    };
    expect(checkOneToOne(excludedO2o).size).toBe(0);
    expect(oneToOnePayload(excludedO2o)).toEqual({
      name: 'Exempt host',
      enabled: true,
      zone: 'zon_wan',
      internal: '192.168.10.60',
      destination: [],
      noBinat: true,
    });

    const pfErr = new ApiError(422, 'VALIDATION_FAILED', 'Validation failed.', [
      { path: '/nat/portForwards/0/target/address', message: 'Loopback target refused.' },
      { path: '/nat/portForwards/0/ports', message: 'Overlapping ports.' },
    ]);
    expect(portForwardErrors(pfErr, portForwardDraftOf(sampleForward)).get('target')).toBe('Loopback target refused.');
    expect(portForwardErrors(pfErr, excludedPf).get('')).toBe('Loopback target refused.');

    const o2oErr = new ApiError(422, 'VALIDATION_FAILED', 'Validation failed.', [
      { path: '/nat/oneToOne/0/external', message: 'Overlaps another entry.' },
    ]);
    expect(oneToOneErrors(o2oErr, excludedO2o).get('')).toBe('Overlaps another entry.');
  });
});
