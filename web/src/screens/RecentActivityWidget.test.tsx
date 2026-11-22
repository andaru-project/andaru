import { expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes } from '../test/fetch';
import { render, screen } from '../test/render';
import { RecentActivityWidget } from './RecentActivityWidget';

const event = (id: number, action: string, result = 'success', count = 1) => ({ id, at: '2026-11-22T10:00:00Z', actor: { kind: 'account', id: 'admin' }, source: '192.0.2.1', action, target: '', result, category: 'general', count, metadata: {} });

it('asks for the newest events and lists them newest first, marking what did not succeed', async () => {
  const fetch = fetchRoutes({ '/api/v1/audit/events': { items: [event(9, 'configuration.applied'), event(8, 'login.failure', 'failure', 3), event(7, 'login.success')] } });
  render(<RecentActivityWidget api={new ApiClient()} onSessionLost={vi.fn()} />);
  expect(await screen.findByText('configuration.applied')).toBeInTheDocument();
  const query = new URL(String(fetch.mock.calls[0][0]), 'http://box').searchParams;
  expect([query.get('order'), query.get('limit')]).toEqual(['desc', '10']);
  expect(screen.getAllByText(/account:admin/)).toHaveLength(3);
  // A count only where the event repeated, said in words to whoever hears the page.
  expect(screen.getByText('×3')).toBeInTheDocument();
  expect(screen.getByText('repeated 3 times')).toBeInTheDocument();
  expect(screen.queryByText(/×1/)).not.toBeInTheDocument();
  expect(screen.getByText('failure')).toBeInTheDocument();
  expect(screen.queryByText('success')).not.toBeInTheDocument();
});

it('leaves out successful reads of the log, its own polling among them, and shows five at most', async () => {
  const items = [event(20, 'audit.queried', 'success', 40), event(19, 'audit.queried', 'unavailable'), ...[18, 17, 16, 15, 14, 13].map((id) => event(id, `action.${id}`))];
  fetchRoutes({ '/api/v1/audit/events': { items } });
  render(<RecentActivityWidget api={new ApiClient()} onSessionLost={vi.fn()} />);
  expect(await screen.findByText('unavailable')).toBeInTheDocument();
  expect(screen.queryByText(/×40/)).not.toBeInTheDocument();
  expect(screen.getAllByText(/^(audit\.queried|action\.\d+)$/).map((node) => node.textContent)).toEqual(['audit.queried', 'action.18', 'action.17', 'action.16', 'action.15']);
});

it('says so when the log is empty', async () => {
  fetchRoutes({ '/api/v1/audit/events': { items: [] } });
  render(<RecentActivityWidget api={new ApiClient()} onSessionLost={vi.fn()} />);
  expect(await screen.findByText('No audit events yet.')).toBeInTheDocument();
});
