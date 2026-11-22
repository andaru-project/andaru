import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes } from '../test/fetch';
import { render, screen } from '../test/render';
import { RecentActivityWidget } from './RecentActivityWidget';

const event = (id: number, action: string, result = 'success', count = 1) => ({ id, at: '2026-11-22T10:00:00Z', actor: { kind: 'account', id: 'admin' }, source: '192.0.2.1', action, target: '', result, category: 'general', count, metadata: {} });

it('asks for the newest few events and lists them newest first, marking what did not succeed', async () => {
  const fetch = fetchRoutes({ '/api/v1/audit/events': { items: [event(9, 'audit.queried', 'success', 4), event(8, 'login.failure', 'failure')] } });
  render(<RecentActivityWidget api={new ApiClient()} onSessionLost={vi.fn()} />);
  expect(await screen.findByText('audit.queried')).toBeInTheDocument();
  expect(String(fetch.mock.calls[0][0])).toMatch(/order=desc.*limit=5|limit=5.*order=desc/);
  expect(screen.getByText(/×4/)).toBeInTheDocument();
  expect(screen.getByText('failure')).toBeInTheDocument();
  expect(screen.queryByText('success')).not.toBeInTheDocument();
});

it('says so when the log is empty', async () => {
  fetchRoutes({ '/api/v1/audit/events': { items: [] } });
  render(<RecentActivityWidget api={new ApiClient()} onSessionLost={vi.fn()} />);
  expect(await screen.findByText('No audit events yet.')).toBeInTheDocument();
});
