import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { render, screen, waitFor } from '../test/render';
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
    ],
    segments: [
      { id: 'seg_lan', name: 'LAN', link: 'lnk_lan', zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } } },
      { id: 'seg_dmz', name: 'DMZ', link: 'lnk_dmz', zone: 'zon_dmz', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } } },
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
  it('shows configured management segment and allowed sources, opens editor with fresh version, warns on move/DHCP, and saves PUT with If-Match', async () => {
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

    // Moving management to DHCP DMZ shows both move and DHCP warnings.
    await user.click(screen.getByLabelText(/^Management segment/, { selector: 'input' }));
    await user.click(await screen.findByRole('option', { name: /DMZ/ }));
    expect(await screen.findByText('Moving management segment')).toBeInTheDocument();
    expect(screen.getByText('DHCP management segment')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Save management access' }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(ifMatch).toBe('"management.3"');
    expect(savedBody).toEqual({ segment: 'seg_dmz', allowedSources: ['adr_admin'] });
  });

  it('shows unset management status alert, hides edit button for read-only users, and saves with version 0 when unset', async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    const unsetSnapshot: Snapshot = {
      ...baseSnapshot,
      value: { ...baseSnapshot.value, management: undefined },
    };

    const { rerender } = render(
      <ManagementPanel api={new ApiClient('csrf')} snapshot={unsetSnapshot} canWrite={false} onSaved={onSaved} onSessionLost={vi.fn()} />,
    );
    expect(screen.getByText('No management segment is set')).toBeInTheDocument();
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
    await user.click(await screen.findByRole('button', { name: 'Save management access' }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(ifMatch).toBe('"management.0"');
    expect(savedBody).toEqual({ segment: 'seg_lan' });
  });

  it('shows 422 field errors and triggers onSessionLost on 401', async () => {
    const user = userEvent.setup();
    const onSessionLost = vi.fn();
    let mode: 'invalid' | 'unauth' = 'invalid';

    fetchRoutes({
      '/api/v1/config/management': () => jsonResponse(200, {
        state: 'saved',
        savedVersion: '10',
        version: '3',
        value: { segment: 'seg_lan' },
      }),
      '/api/v1/config': () => jsonResponse(200, baseSnapshot),
      'PUT /api/v1/config/management': () => (
        mode === 'invalid'
          ? jsonResponse(422, {
            error: {
              code: 'VALIDATION_FAILED',
              message: 'Invalid management config.',
              details: [{ path: '/management/segment', message: 'Segment must be enabled.' }],
            },
          })
          : jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Expired.' } })
      ),
    });

    render(<ManagementPanel api={new ApiClient('csrf')} snapshot={baseSnapshot} canWrite onSaved={vi.fn()} onSessionLost={onSessionLost} />);
    await user.click(screen.getByRole('button', { name: 'Edit management access' }));
    await user.click(await screen.findByRole('button', { name: 'Save management access' }));
    expect(await screen.findByText('Segment must be enabled.')).toBeInTheDocument();

    mode = 'unauth';
    await user.click(screen.getByRole('button', { name: 'Save management access' }));
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
  });
});
