import { useCallback, useMemo } from 'react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { expect, it } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen, within } from '../test/render';
import type { UpdateStatus } from '../types';
import { useResource } from '../useResource';
import { UpdatesScreen } from './UpdatesScreen';

const availableStatus: UpdateStatus = {
  supported: true,
  state: 'available',
  booted: {
    image: 'ghcr.io/andaru-project/andaru:stable',
    version: '0.5.0-a.4',
    digest: 'sha256:11111111111111111111111111111111',
    timestamp: '2026-11-23T10:00:00Z',
    signature: 'containerPolicy',
  },
  available: {
    image: 'ghcr.io/andaru-project/andaru:stable',
    version: '0.5.0-a.5',
    digest: 'sha256:22222222222222222222222222222222',
    timestamp: '2026-11-24T10:00:00Z',
    signature: 'containerPolicy',
  },
  rollback: {
    image: 'ghcr.io/andaru-project/andaru:0.5.0-a.3',
    version: '0.5.0-a.3',
    digest: 'sha256:00000000000000000000000000000000',
    signature: 'containerPolicy',
  },
  checkedAt: '2026-11-24T11:00:00Z',
  rebootNeeded: false,
};

const stagedStatus: UpdateStatus = {
  ...availableStatus,
  state: 'staged',
  available: undefined,
  staged: availableStatus.available,
  rebootNeeded: true,
};

const noopSessionLost = () => {};

function Harness({ api, capabilities }: { api: ApiClient; capabilities: string[] }) {
  const load = useCallback(() => api.updateStatus(), [api]);
  const update = useResource(load, noopSessionLost);
  const sess = useMemo(() => session(capabilities), [capabilities]);
  return (
    <MemoryRouter>
      <UpdatesScreen api={api} session={sess} update={update} onSessionLost={noopSessionLost} />
    </MemoryRouter>
  );
}

it('shows available update, stages it, and confirms reboot with CSRF header', async () => {
  let current: UpdateStatus = availableStatus;
  const fetchMock = fetchRoutes({
    '/api/v1/system/update': () => new Response(JSON.stringify(current), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    'POST /api/v1/system/update/check': () => new Response(JSON.stringify(current), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    'POST /api/v1/system/update/stage': () => {
      current = stagedStatus;
      return new Response(JSON.stringify(current), { status: 200, headers: { 'Content-Type': 'application/json' } });
    },
    'POST /api/v1/system/update/reboot': { rebooting: true },
  });

  render(<Harness api={new ApiClient('csrf-tok')} capabilities={['status.read', 'configuration.apply']} />);

  expect(await screen.findByText('0.5.0-a.4')).toBeInTheDocument();
  expect(screen.getByText('0.5.0-a.5')).toBeInTheDocument();
  expect(screen.getByText('0.5.0-a.3')).toBeInTheDocument();
  expect(screen.getAllByText('Verified')).toHaveLength(3);
  expect(screen.queryByText(/cosign/i)).not.toBeInTheDocument();

  const runningSection = screen.getByRole('region', { name: 'Running image' });
  expect(within(runningSection).getByText('Active')).toBeInTheDocument();

  await userEvent.click(screen.getByRole('button', { name: 'Download and stage update' }));
  expect(await screen.findByRole('button', { name: 'Reboot to apply' })).toBeInTheDocument();
  expect(within(runningSection).getByText('Active')).toBeInTheDocument();
  expect(within(runningSection).queryByText(/staged/i)).not.toBeInTheDocument();
  expect(within(screen.getByRole('region', { name: 'Staged for next boot' })).getByText('Reboot required')).toBeInTheDocument();

  await userEvent.click(screen.getByRole('button', { name: 'Reboot to apply' }));
  expect(await screen.findByText('Reboot appliance now?')).toBeInTheDocument();
  await userEvent.click(screen.getByRole('button', { name: 'Reboot now' }));
  expect(await screen.findByText('Rebooting appliance')).toBeInTheDocument();

  const rebootCall = fetchMock.mock.calls.find(([url, init]) => url === '/api/v1/system/update/reboot' && init?.method === 'POST');
  expect(rebootCall).toBeDefined();
  expect(new Headers(rebootCall?.[1]?.headers).get('X-Andaru-CSRF')).toBe('csrf-tok');
});

it('queues rollback after confirmation and offers Reboot to apply even when staged is absent', async () => {
  let current: UpdateStatus = availableStatus;
  const fetchMock = fetchRoutes({
    '/api/v1/system/update': () => new Response(JSON.stringify(current), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    'POST /api/v1/system/update/rollback': () => {
      current = { ...availableStatus, state: 'staged', available: undefined, staged: undefined, rollbackQueued: true, rebootNeeded: true };
      return new Response(JSON.stringify(current), { status: 200, headers: { 'Content-Type': 'application/json' } });
    },
  });

  render(<Harness api={new ApiClient('csrf-tok')} capabilities={['status.read', 'configuration.apply']} />);
  await userEvent.click(await screen.findByRole('button', { name: 'Roll back to previous version' }));
  await userEvent.click(await screen.findByRole('button', { name: 'Queue rollback' }));

  expect(fetchMock.mock.calls.some(([url, init]) => url === '/api/v1/system/update/rollback' && init?.method === 'POST')).toBe(true);
  expect(await screen.findByRole('button', { name: 'Reboot to apply' })).toBeInTheDocument();
  expect(screen.getByText('Rollback queued for next boot')).toBeInTheDocument();
});

it('hides update controls for read-only accounts', async () => {
  fetchRoutes({ '/api/v1/system/update': availableStatus });
  render(<Harness api={new ApiClient('csrf-tok')} capabilities={['status.read']} />);

  expect(await screen.findByText('Read-only access')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Check for updates' })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Download and stage update' })).not.toBeInTheDocument();
});

it('explains when the host is not running a bootc appliance image', async () => {
  fetchRoutes({ '/api/v1/system/update': { supported: false, state: 'unsupported', rebootNeeded: false } });
  render(<Harness api={new ApiClient('csrf-tok')} capabilities={['status.read', 'configuration.apply']} />);

  expect(await screen.findByText(/not booted from a bootc-managed Andaru appliance image/i)).toBeInTheDocument();
});

it('clears the rebooting banner and re-enables controls once the appliance returns on the new image', async () => {
  fetchRoutes({
    'POST /api/v1/system/update/reboot': () => new Response(JSON.stringify({ rebooting: true }), { status: 202, headers: { 'Content-Type': 'application/json' } }),
  });
  const api = new ApiClient('csrf-tok');
  const mgr = session(['status.read', 'configuration.apply']);
  const staged: UpdateStatus = {
    ...availableStatus,
    state: 'staged',
    available: undefined,
    staged: availableStatus.available,
    rebootNeeded: true,
  };
  const makeResource = (data: UpdateStatus) => ({
    data,
    loading: false,
    error: '',
    refresh: () => Promise.resolve(),
    reload: () => Promise.resolve(),
  });

  const { rerender } = render(<UpdatesScreen api={api} session={mgr} update={makeResource(staged)} onSessionLost={noopSessionLost} />);
  await userEvent.click(screen.getByRole('button', { name: 'Reboot to apply' }));
  await userEvent.click(await screen.findByRole('button', { name: 'Reboot now' }));

  expect(await screen.findByText('Rebooting appliance')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Check for updates' })).toBeDisabled();

  // Stale poll, same booted image with rebootNeeded=false, or unchecked status with missing booted keep rebooting active.
  rerender(<UpdatesScreen api={api} session={mgr} update={makeResource({ ...staged })} onSessionLost={noopSessionLost} />);
  expect(screen.getByText('Rebooting appliance')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Check for updates' })).toBeDisabled();

  rerender(<UpdatesScreen api={api} session={mgr} update={makeResource({ ...staged, rebootNeeded: false })} onSessionLost={noopSessionLost} />);
  expect(screen.getByText('Rebooting appliance')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Check for updates' })).toBeDisabled();

  rerender(<UpdatesScreen api={api} session={mgr} update={makeResource({ supported: true, state: 'unchecked', rebootNeeded: false })} onSessionLost={noopSessionLost} />);
  expect(screen.getByText('Rebooting appliance')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Check for updates' })).toBeDisabled();

  // Post-reboot status with the new booted deployment clears rebooting and re-enables controls.
  const bootedNew: UpdateStatus = {
    supported: true,
    state: 'up_to_date',
    booted: availableStatus.available,
    rollback: availableStatus.booted,
    rebootNeeded: false,
  };
  rerender(<UpdatesScreen api={api} session={mgr} update={makeResource(bootedNew)} onSessionLost={noopSessionLost} />);
  expect(screen.queryByText('Rebooting appliance')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Check for updates' })).toBeEnabled();

  // Once cleared, a later poll without booted info does not falsely re-enter the rebooting state.
  rerender(<UpdatesScreen api={api} session={mgr} update={makeResource({ supported: true, state: 'unchecked', rebootNeeded: false })} onSessionLost={noopSessionLost} />);
  expect(screen.queryByText('Rebooting appliance')).not.toBeInTheDocument();
});

it('shows Active on Running image with Queueing rollback on rollback card, and falls back when secondary cards are absent', () => {
  const api = new ApiClient('csrf-tok');
  const mgr = session(['status.read', 'configuration.apply']);
  const makeResource = (data: UpdateStatus) => ({
    data,
    loading: false,
    error: '',
    refresh: () => Promise.resolve(),
    reload: () => Promise.resolve(),
  });

  const rollingBack: UpdateStatus = {
    ...availableStatus,
    state: 'rolling_back',
    available: undefined,
    rebootNeeded: false,
  };
  const { rerender } = render(<UpdatesScreen api={api} session={mgr} update={makeResource(rollingBack)} onSessionLost={noopSessionLost} />);
  expect(within(screen.getByRole('region', { name: 'Running image' })).getByText('Active')).toBeInTheDocument();
  expect(within(screen.getByRole('region', { name: 'Previous image (rollback)' })).getByText('Queueing rollback…')).toBeInTheDocument();

  // If a state has no secondary card rendered, Running image preserves the state label instead of hiding it behind Active.
  rerender(<UpdatesScreen api={api} session={mgr} update={makeResource({ supported: true, state: 'staged', booted: availableStatus.booted, rebootNeeded: false })} onSessionLost={noopSessionLost} />);
  expect(within(screen.getByRole('region', { name: 'Running image' })).getByText('Staged — reboot required')).toBeInTheDocument();
});
