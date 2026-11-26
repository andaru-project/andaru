import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { render, screen, waitFor, within } from '../test/render';
import type { Snapshot } from '../types';
import { ManagementPanel } from './ManagementPanel';

afterEach(() => {
  vi.restoreAllMocks();
});

const baseSnapshot: Snapshot = {
  state: 'saved',
  savedVersion: '10',
  value: {
    links: [
      { id: 'lnk_lan', name: 'LAN port', kind: 'ethernet', mtu: 1500, enabled: true },
      { id: 'lnk_dmz', name: 'DMZ port', kind: 'ethernet', mtu: 1500, enabled: true },
      { id: 'lnk_parent_off', name: 'Disabled trunk', kind: 'ethernet', mtu: 1500, enabled: false },
      { id: 'lnk_vlan_off', name: 'VLAN 20', kind: 'vlan', parent: 'lnk_parent_off', vlanId: 20, mtu: 1500, enabled: true },
    ],
    segments: [
      { id: 'seg_lan', name: 'LAN', link: 'lnk_lan', zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } } },
      { id: 'seg_dmz', name: 'DMZ', link: 'lnk_dmz', zone: 'zon_dmz', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } } },
      { id: 'seg_off', name: 'Mgmt VLAN', link: 'lnk_vlan_off', zone: 'zon_lan', addressing: { ipv4: { mode: 'none' }, ipv6: { mode: 'none' } } },
    ],
    zones: [
      { id: 'zon_lan', name: 'LAN', color: 'green' },
      { id: 'zon_dmz', name: 'DMZ', color: 'violet' },
    ],
    aliases: {
      addresses: [
        { id: 'adr_admin', name: 'Admin workstation', kind: 'host', address: '192.168.1.10' },
        { id: 'adr_ops', name: 'Ops subnet', kind: 'network', prefix: '10.0.0.0/24' },
      ],
      ports: [],
    },
    management: { segment: 'seg_lan', allowedSources: ['adr_admin'] },
  },
};

describe('ManagementPanel', () => {
  it('shows configured management segment and allowed SSH sources, warns on move/DHCP/disabled parent/SSH restriction, and saves PUT with If-Match', async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    let savedBody: unknown;
    let ifMatch: string | null = null;

    fetchRoutes({
      '/api/v1/config/management': () => jsonResponse(200, {
        state: 'saved',
        savedVersion: '10',
        version: '3',
        value: { segment: 'seg_lan', allowedSources: ['adr_admin'] },
      }),
      '/api/v1/config': () => jsonResponse(200, baseSnapshot),
      'PUT /api/v1/config/management': (init) => {
        savedBody = JSON.parse(String(init?.body ?? '{}'));
        ifMatch = new Headers(init?.headers).get('If-Match');
        return jsonResponse(200, {
          state: 'saved',
          savedVersion: '11',
          version: '4',
          value: savedBody,
        });
      },
    });

    render(
      <MemoryRouter>
        <ManagementPanel api={new ApiClient('csrf')} snapshot={baseSnapshot} canWrite onSaved={onSaved} onSessionLost={vi.fn()} />
      </MemoryRouter>,
    );

    expect(screen.getByText('Admin workstation')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Edit management access' }));
    expect(await screen.findByRole('dialog', { name: 'Edit management access' })).toBeInTheDocument();
    expect(screen.getByText('Verify SSH access before applying')).toBeInTheDocument();

    // Selecting a VLAN with a disabled parent link shows disabled + unaddressed warnings.
    await user.click(screen.getByLabelText(/^Management segment/, { selector: 'input' }));
    await user.click(await screen.findByRole('option', { name: /Mgmt VLAN/ }));
    expect(await screen.findByText('Selected link is disabled')).toBeInTheDocument();
    expect(screen.getByText('Unaddressed management segment')).toBeInTheDocument();

    // Moving management to DHCP DMZ shows move and dynamic-address warnings.
    await user.click(screen.getByLabelText(/^Management segment/, { selector: 'input' }));
    await user.click(await screen.findByRole('option', { name: /DMZ/ }));
    expect(await screen.findByText('Moving management segment')).toBeInTheDocument();
    expect(screen.getByText('Dynamic management address')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Save management access' }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(ifMatch).toBe('"management.3"');
    expect(savedBody).toEqual({ segment: 'seg_dmz', allowedSources: ['adr_admin'] });
  });

  it('requires explicit segment selection when unset, hides Configure for read-only or empty segments, and omits empty allowedSources', async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    const unsetSnapshot: Snapshot = {
      ...baseSnapshot,
      value: { ...baseSnapshot.value, management: undefined },
    };
    const emptySegmentsSnapshot: Snapshot = {
      ...unsetSnapshot,
      value: { ...unsetSnapshot.value, segments: [] },
    };

    const { rerender } = render(
      <ManagementPanel api={new ApiClient('csrf')} snapshot={unsetSnapshot} canWrite={false} onSaved={onSaved} onSessionLost={vi.fn()} />,
    );
    expect(screen.getByText('No management segment is set')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /management access/i })).not.toBeInTheDocument();

    rerender(
      <ManagementPanel api={new ApiClient('csrf')} snapshot={emptySegmentsSnapshot} canWrite onSaved={onSaved} onSessionLost={vi.fn()} />,
    );
    expect(screen.queryByRole('button', { name: /management access/i })).not.toBeInTheDocument();

    let ifMatch: string | null = null;
    let savedBody: unknown;
    fetchRoutes({
      '/api/v1/config/management': () => jsonResponse(404, { error: { code: 'NOT_FOUND', message: 'Not set.' } }),
      '/api/v1/config': () => jsonResponse(200, unsetSnapshot),
      'PUT /api/v1/config/management': (init) => {
        ifMatch = new Headers(init?.headers).get('If-Match');
        savedBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(200, { state: 'saved', savedVersion: '11', version: '1', value: savedBody });
      },
    });

    rerender(
      <ManagementPanel api={new ApiClient('csrf')} snapshot={unsetSnapshot} canWrite onSaved={onSaved} onSessionLost={vi.fn()} />,
    );
    await user.click(screen.getByRole('button', { name: 'Configure management access' }));

    // Submitting without choosing a segment fails client validation instead of auto-selecting segments[0].
    await user.click(await screen.findByRole('button', { name: 'Save management access' }));
    expect(await screen.findByText('Choose a management segment.')).toBeInTheDocument();
    expect(onSaved).not.toHaveBeenCalled();

    await user.click(screen.getByLabelText(/^Management segment/, { selector: 'input' }));
    await user.click(await screen.findByRole('option', { name: /^LAN \(/ }));
    await user.click(screen.getByRole('button', { name: 'Save management access' }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(ifMatch).toBe('"management.0"');
    expect(savedBody).toEqual({ segment: 'seg_lan' });
  });

  it('handles unprefixed 422 field errors, 412 conflict refresh, open errors, dangling IDs, and 401 session loss', async () => {
    const user = userEvent.setup();
    const onSessionLost = vi.fn();
    const onRefresh = vi.fn();
    let getMode: 'fail' | 'ok' = 'fail';
    let putMode: 'invalid' | 'conflict' | 'unauth' = 'invalid';

    const danglingSnapshot: Snapshot = {
      ...baseSnapshot,
      value: {
        ...baseSnapshot.value,
        management: { segment: 'seg_gone', allowedSources: ['adr_gone'] },
      },
    };

    fetchRoutes({
      '/api/v1/config/management': () => (
        getMode === 'fail'
          ? jsonResponse(503, { error: { code: 'UNAVAILABLE', message: 'Store unavailable.' } })
          : jsonResponse(200, { state: 'saved', savedVersion: '10', version: '3', value: { segment: 'seg_lan', allowedSources: ['adr_admin'] } })
      ),
      '/api/v1/config': () => jsonResponse(200, baseSnapshot),
      'PUT /api/v1/config/management': () => {
        if (putMode === 'invalid') {
          return jsonResponse(422, {
            error: {
              code: 'VALIDATION_FAILED',
              message: 'Invalid management config.',
              details: [
                { path: '/segment', message: 'Segment must be enabled.' },
                { path: '/allowedSources/0', message: 'Alias does not exist.' },
              ],
            },
          });
        }
        if (putMode === 'conflict') {
          return jsonResponse(412, { error: { code: 'CONFLICT', message: 'Version mismatch.' } });
        }
        return jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Expired.' } });
      },
    });

    render(<ManagementPanel api={new ApiClient('csrf')} snapshot={danglingSnapshot} canWrite onSaved={vi.fn()} onRefresh={onRefresh} onSessionLost={onSessionLost} />);
    expect(screen.getByText('seg_gone (missing)')).toBeInTheDocument();
    expect(screen.getByText('adr_gone (missing)')).toBeInTheDocument();

    // Open error surfaces inline.
    await user.click(screen.getByRole('button', { name: 'Edit management access' }));
    expect(await screen.findByText('Store unavailable.')).toBeInTheDocument();

    // Retry open -> succeeds, then 422 maps unprefixed /segment and /allowedSources/0.
    getMode = 'ok';
    await user.click(screen.getByRole('button', { name: 'Edit management access' }));
    await user.click(await screen.findByRole('button', { name: 'Save management access' }));
    expect(await screen.findByText('Segment must be enabled.')).toBeInTheDocument();
    expect(screen.getByText('Alias does not exist.')).toBeInTheDocument();

    // 412 conflict calls onRefresh so parent snapshot reloads and keeps the conflict alert visible.
    putMode = 'conflict';
    await user.click(screen.getByRole('button', { name: 'Save management access' }));
    await waitFor(() => expect(onRefresh).toHaveBeenCalledTimes(1));
    expect(await screen.findByText(/changed by someone else/i)).toBeInTheDocument();

    // 401 signs out.
    putMode = 'unauth';
    await user.click(screen.getByRole('button', { name: 'Save management access' }));
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
  });

  it('keeps the open editor drawer mounted with unsaved edits if the parent snapshot temporarily disappears', async () => {
    const user = userEvent.setup();
    fetchRoutes({
      '/api/v1/config/management': () => jsonResponse(200, {
        state: 'saved',
        savedVersion: '10',
        version: '3',
        value: { segment: 'seg_lan', allowedSources: ['adr_admin'] },
      }),
      '/api/v1/config': () => jsonResponse(200, baseSnapshot),
    });

    const api = new ApiClient('csrf');
    const { rerender } = render(
      <ManagementPanel api={api} snapshot={baseSnapshot} canWrite onSaved={vi.fn()} onSessionLost={vi.fn()} />,
    );

    await user.click(screen.getByRole('button', { name: 'Edit management access' }));
    await screen.findByRole('dialog', { name: 'Edit management access' });
    const segmentSelect = screen.getByLabelText(/^Management segment/, { selector: 'input' });
    await user.click(segmentSelect);
    await user.click(await screen.findByRole('option', { name: /DMZ/ }));
    expect(await screen.findByText('Moving management segment')).toBeInTheDocument();

    // Transient poll failure clears parent snapshot; drawer and unsaved edits must stay mounted.
    rerender(<ManagementPanel api={api} snapshot={undefined} canWrite onSaved={vi.fn()} onSessionLost={vi.fn()} />);
    expect(screen.getByRole('dialog', { name: 'Edit management access' })).toBeInTheDocument();
    expect(screen.getByText('Moving management segment')).toBeInTheDocument();
  });

  it('excludes dynamic url and hostnames aliases from Allowed SSH sources', async () => {
    const user = userEvent.setup();
    const snapWithDynamic = {
      ...baseSnapshot,
      value: {
        ...baseSnapshot.value,
        aliases: {
          addresses: [
            ...(baseSnapshot.value.aliases?.addresses ?? []),
            { id: 'adr_url', name: 'cf-feed', kind: 'url' as const, url: 'https://www.cloudflare.com/ips-v4', refreshSeconds: 3600 },
            { id: 'adr_dns', name: 'vpn-dns', kind: 'hostnames' as const, names: ['vpn.example.com'], refreshSeconds: 300 },
          ],
          ports: [],
        },
      },
    };
    fetchRoutes({
      '/api/v1/config/management': () => jsonResponse(200, {
        state: 'saved',
        savedVersion: '10',
        version: '3',
        value: { segment: 'seg_lan', allowedSources: [] },
      }),
      '/api/v1/config': () => jsonResponse(200, snapWithDynamic),
    });

    render(<ManagementPanel api={new ApiClient('csrf')} snapshot={snapWithDynamic} canWrite onSaved={vi.fn()} onSessionLost={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: 'Edit management access' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit management access' });
    const sourcesInput = within(dialog).getByLabelText(/^Allowed SSH sources/, { selector: 'input' });
    await user.click(sourcesInput);
    const listbox = document.getElementById(sourcesInput.getAttribute('aria-controls')!)!;
    const labels = within(listbox).getAllByRole('option', { hidden: true }).map((o) => o.textContent);
    expect(labels).toContain('Admin workstation');
    expect(labels).not.toContain('cf-feed');
    expect(labels).not.toContain('vpn-dns');
  });
});
