import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes } from '../test/fetch';
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
    ],
    segments: [
      { id: 'seg_lan', name: 'LAN', link: 'lnk_lan', zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } } },
      { id: 'seg_wan', name: 'WAN', link: 'lnk_wan', zone: 'zon_wan', addressing: { ipv4: { mode: 'static', addresses: ['203.0.113.2/24'], gateway: '203.0.113.1' }, ipv6: { mode: 'none' } } },
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
  rules: [],
};

function renderSimulator(caps = ['configuration.read', 'status.read']) {
  render(
    <MemoryRouter>
      <TrafficFlowScreen api={new ApiClient('csrf')} session={session(caps)} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
}

describe('TrafficFlowScreen', () => {
  it('simulates traffic, renders the flow diagram and ordered text trace with links, and switches between saved and live', async () => {
    const user = userEvent.setup();
    fetchRoutes({
      '/api/v1/config': savedSnapshot,
      '/api/v1/applies': {
        pointers: { ...pointers, live: 'app_live', confirmed: 'app_live' },
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

    // Client validation catches invalid address.
    await user.type(screen.getByLabelText(/^Source address/), 'not-an-ip');
    await user.type(screen.getByLabelText(/^Destination address/), '1.1.1.1');
    await user.click(screen.getByRole('button', { name: 'Simulate traffic' }));
    expect(await screen.findByText('Enter a valid bare IPv4 or IPv6 source address.')).toBeInTheDocument();

    // Fix source address and simulate against Saved (v5) -> Allowed + masqueraded.
    const srcInput = screen.getByLabelText(/^Source address/);
    await user.clear(srcInput);
    await user.type(srcInput, '192.168.1.50');
    await user.click(screen.getByRole('button', { name: 'Simulate traffic' }));

    expect(await screen.findByText('Allowed')).toBeInTheDocument();
    expect(screen.getByText('Final: TCP 203.0.113.2 → 1.1.1.1:443')).toBeInTheDocument();

    const diagram = screen.getByRole('region', { name: 'Traffic flow diagram' });
    expect(within(diagram).getByText('Ingress')).toBeInTheDocument();
    const ruleLinks = within(diagram).getAllByRole('link', { name: 'Rule #1 (Allow LAN HTTPS)' });
    expect(ruleLinks.length).toBeGreaterThanOrEqual(1);
    expect(ruleLinks[0]).toHaveAttribute('href', '/firewall/rules');

    const trace = screen.getByRole('region', { name: 'Step-by-step trace' });
    const listItems = within(trace).getAllByRole('listitem');
    expect(listItems.length).toBe(6);
    expect(trace).toHaveTextContent('Outbound NAT masqueraded source address to 203.0.113.2 on WAN.');

    // Switch to Live (v4) where Allow LAN HTTPS does not exist -> Dropped by default policy.
    await user.click(screen.getByRole('radio', { name: 'Live (v4)' }));
    expect(await screen.findByText('Dropped')).toBeInTheDocument();
    expect(within(trace).getByText(/dropped by default forward chain policy/i)).toBeInTheDocument();
  });

  it('shows an empty-config prompt when no segments exist', async () => {
    fetchRoutes({
      '/api/v1/config': { state: 'saved', savedVersion: '1', value: { links: [], segments: [], zones: [] } },
      '/api/v1/applies': { pointers, items: [] },
    });
    renderSimulator(['configuration.read']);

    expect(await screen.findByText('No segments configured')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Interfaces' })).toHaveAttribute('href', '/network/interfaces');
  });
});
