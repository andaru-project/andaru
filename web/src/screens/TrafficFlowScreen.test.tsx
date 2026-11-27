import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { apply, pointers, session } from '../test/fixtures';
import { render } from '../test/render';
import type { Snapshot } from '../types';
import { TrafficFlowScreen } from './TrafficFlowScreen';

afterEach(() => {
  vi.restoreAllMocks();
});

const savedSnapshot: Snapshot = {
  state: 'saved',
  savedVersion: '5',
  value: {
    links: [
      { id: 'lnk_wan', name: 'eth0', kind: 'ethernet', mtu: 1500, enabled: true },
      { id: 'lnk_lan', name: 'eth1', kind: 'ethernet', mtu: 1500, enabled: true },
      { id: 'lnk_guest', name: 'eth2', kind: 'ethernet', mtu: 1500, enabled: true },
    ],
    segments: [
      { id: 'seg_lan', name: 'LAN', link: 'lnk_lan', zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } } },
      { id: 'seg_wan', name: 'WAN', link: 'lnk_wan', zone: 'zon_wan', addressing: { ipv4: { mode: 'static', addresses: ['203.0.113.2/24'], gateway: '203.0.113.1' }, ipv6: { mode: 'none' } } },
      { id: 'seg_guest', name: 'Guest', link: 'lnk_guest', zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.50.1/24'] }, ipv6: { mode: 'none' } } },
    ],
    zones: [
      { id: 'zon_lan', name: 'LAN', color: 'green' },
      { id: 'zon_wan', name: 'WAN', color: 'red' },
    ],
    management: { segment: 'seg_lan' },
    rules: [
      { id: 'rul_allow', name: 'Allow LAN HTTPS', enabled: true, action: 'accept', from: { zones: ['zon_lan'], addresses: [] }, to: { zones: ['zon_wan'], addresses: [] }, ports: [], log: false },
    ],
    nat: { outbound: { mode: 'automatic' } },
  },
};

const liveConfigValue: Snapshot['value'] = {
  ...savedSnapshot.value,
  segments: savedSnapshot.value.segments.filter((s) => s.id !== 'seg_guest'),
  rules: [],
};

function renderSimulator(caps = ['configuration.read', 'status.read'], onSessionLost = vi.fn()) {
  render(
    <MemoryRouter>
      <TrafficFlowScreen api={new ApiClient('csrf')} session={session(caps)} onSessionLost={onSessionLost} />
    </MemoryRouter>,
  );
  return { onSessionLost };
}

describe('TrafficFlowScreen', () => {
  it('simulates traffic, renders semantic flow diagram and ordered text trace with links, switches saved/live, and clears results', async () => {
    const user = userEvent.setup();
    fetchRoutes({
      '/api/v1/config': savedSnapshot,
      '/api/v1/applies': {
        pointers: { ...pointers, live: 'app_live', confirmed: 'app_live' },
        unapplied: true,
        items: [apply({ id: 'app_live', savedVersion: '4', state: 'confirmed' })],
      },
      '/api/v1/versions/4': {
        state: 'saved',
        id: '4',
        createdAt: '2026-11-26T10:00:00Z',
        actor: 'admin',
        changedObjectCount: 1,
        value: liveConfigValue,
        changes: [],
      },
      '/api/v1/status/links': { items: [] },
    });
    renderSimulator();

    expect(await screen.findByText('Hypothetical packet')).toBeInTheDocument();

    // Client validation catches invalid address and focuses the invalid input.
    const srcInput = screen.getByLabelText(/^Source address/);
    await user.type(srcInput, 'not-an-ip');
    await user.type(screen.getByLabelText(/^Destination address/), '1.1.1.1');
    await user.click(screen.getByRole('button', { name: 'Simulate traffic' }));
    expect(await screen.findByText('Enter a valid bare IPv4 or IPv6 source address.')).toBeInTheDocument();
    expect(srcInput).toHaveFocus();

    // Fix source address and simulate against Saved (v5) -> Allowed + masqueraded.
    await user.clear(srcInput);
    await user.type(srcInput, '192.168.1.50');
    await user.click(screen.getByRole('button', { name: 'Simulate traffic' }));

    expect(await screen.findByText('Allowed')).toBeInTheDocument();
    expect(screen.getByText('Final: TCP 203.0.113.2 → 1.1.1.1:443')).toBeInTheDocument();

    const diagram = screen.getByRole('region', { name: 'Flow diagram' });
    expect(within(diagram).getAllByRole('listitem')).toHaveLength(6);
    const ruleLinks = within(diagram).getAllByRole('link', { name: 'Rule #1 (Allow LAN HTTPS)' });
    expect(ruleLinks[0]).toHaveAttribute('href', '/firewall/rules');

    const trace = screen.getByRole('region', { name: 'Step-by-step trace' });
    expect(within(trace).getAllByRole('listitem')).toHaveLength(6);
    expect(trace).toHaveTextContent('Outbound NAT masqueraded source address to 203.0.113.2 on WAN.');

    // Switch to Live (v4) where Allow LAN HTTPS does not exist -> Dropped by default policy.
    await user.click(screen.getByRole('radio', { name: 'Live (v4)' }));
    expect(await screen.findByText('Dropped')).toBeInTheDocument();
    expect(within(trace).getByText(/dropped by default forward chain policy/i)).toBeInTheDocument();

    // Switch back to Saved, simulate on Guest (not present in Live), then switch to Live -> shows stale-ingress notice.
    await user.click(screen.getByRole('radio', { name: 'Saved (v5)' }));
    await user.click(screen.getByLabelText(/^Ingress interface/, { selector: 'input' }));
    await user.click(await screen.findByRole('option', { name: /Guest/ }));
    await user.click(screen.getByRole('button', { name: 'Simulate traffic' }));
    expect(await screen.findByText('Allowed')).toBeInTheDocument();

    await user.click(screen.getByRole('radio', { name: 'Live (v4)' }));
    expect(await screen.findByText('Ingress interface not present in selected configuration')).toBeInTheDocument();

    // Clear result resets the verdict and hides the notice.
    await user.click(screen.getByRole('button', { name: 'Clear result' }));
    expect(screen.queryByText('Ingress interface not present in selected configuration')).not.toBeInTheDocument();

    // Protocol toggle to ICMP hides port inputs and shows ICMP type.
    await user.selectOptions(screen.getByLabelText('Protocol'), 'icmp');
    expect(screen.queryByLabelText(/^Destination port/)).not.toBeInTheDocument();
    expect(screen.getByLabelText('ICMP type')).toHaveValue('echo-request');
  });

  it('shows empty-config prompt without status.read and propagates 401 session loss', async () => {
    fetchRoutes({
      '/api/v1/config': { state: 'saved', savedVersion: '1', value: { links: [], segments: [], zones: [] } },
      '/api/v1/applies': { pointers, unapplied: true, items: [] },
    });
    const first = render(
      <MemoryRouter>
        <TrafficFlowScreen api={new ApiClient('csrf')} session={session(['configuration.read'])} onSessionLost={vi.fn()} />
      </MemoryRouter>,
    );
    expect(await screen.findByText('No segments configured')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Interfaces' })).toHaveAttribute('href', '/network/interfaces');
    first.unmount();

    const onSessionLost = vi.fn();
    fetchRoutes({
      '/api/v1/config': () => jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Expired.' } }),
      '/api/v1/applies': () => jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Expired.' } }),
    });
    renderSimulator(['configuration.read'], onSessionLost);
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
  });
});
