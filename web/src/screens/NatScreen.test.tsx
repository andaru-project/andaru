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
});

describe('natEdit helpers', () => {
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
