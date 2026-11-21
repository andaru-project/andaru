import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { ApiClient } from './api';
import { clock, type ChangeState } from './changes';
import { PendingNotice } from './PendingNotice';
import { fetchRoutes } from './test/fetch';
import { act, render, screen } from './test/render';
import type { Apply } from './types';

const all = new Set(['configuration.read', 'configuration.apply', 'configuration.confirm']);
const apply = (fields: Partial<Apply>): Apply => ({ id: '1', savedVersion: '3', state: 'confirmed', previous: null, reason: '', createdAt: '2026-11-21T10:00:00Z', confirmTimeoutSeconds: 120, files: [], ...fields });
const pointers = { confirmed: null, lastKnownGood: null, pending: null, live: null };

function show(state: ChangeState, onChanged = vi.fn()) {
  render(<MemoryRouter><PendingNotice api={new ApiClient('csrf')} capabilities={all} state={state} onChanged={onChanged} onSessionLost={vi.fn()} /></MemoryRouter>);
  return onChanged;
}

afterEach(() => vi.useRealTimers());

it('says saved changes are not applied when the saved head is newer than the live generation', () => {
  show({ applies: { pointers: { ...pointers, live: '1' }, items: [apply({})] }, head: '4' });
  expect(screen.getByRole('status')).toHaveTextContent('Saved changes are not applied yet');
  expect(screen.getByRole('link', { name: 'Review and apply' })).toHaveAttribute('href', '/changes');
});

it('says nothing when the live generation runs the saved head', () => {
  show({ applies: { pointers: { ...pointers, live: '1' }, items: [apply({})] }, head: '3' });
  expect(screen.queryByRole('status')).not.toBeInTheDocument();
});

it('counts down to the server deadline and offers Confirm and Roll back', async () => {
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] });
  vi.setSystemTime(new Date('2026-11-21T10:00:00Z'));
  const deadline = '2026-11-21T10:01:30Z';
  const fetchMock = fetchRoutes({ 'POST /api/v1/applies/2/confirmation': apply({ id: '2', state: 'confirmed' }) });
  const onChanged = show({ applies: { pointers: { ...pointers, live: '2', pending: '2' }, items: [apply({ id: '2', state: 'awaiting_confirmation', deadline })] }, head: '3' });

  expect(screen.getByRole('status')).toHaveTextContent(`Applied, awaiting confirmation: rolls back at ${clock(deadline)}`);
  expect(screen.getByText('1:30 left')).toBeInTheDocument();
  act(() => { vi.advanceTimersByTime(1000); });
  expect(screen.getByText('1:29 left')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Roll back' })).toBeInTheDocument();

  vi.useRealTimers();
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }));
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/applies/2/confirmation', expect.objectContaining({ method: 'POST' }));
  expect(onChanged).toHaveBeenCalled();
});

it('reports a failed or rolled back apply until something newer is saved', () => {
  const items = [apply({ id: '2', savedVersion: '4', state: 'failed', reason: 'boom' }), apply({})];
  show({ applies: { pointers: { ...pointers, live: '1' }, items }, head: '4' });
  expect(screen.getByRole('status')).toHaveTextContent('The last apply failed');
  expect(screen.getByRole('link', { name: 'See its details' })).toHaveAttribute('href', '/changes');
});

it('reports a rollback', () => {
  show({ applies: { pointers: { ...pointers, live: '1' }, items: [apply({ id: '2', savedVersion: '4', state: 'rolled_back' }), apply({})] }, head: '4' });
  expect(screen.getByRole('status')).toHaveTextContent('The last apply was rolled back');
});

it('moves on from a failed apply to unapplied changes once something newer is saved', () => {
  show({ applies: { pointers: { ...pointers, live: '1' }, items: [apply({ id: '2', savedVersion: '4', state: 'failed' }), apply({})] }, head: '5' });
  expect(screen.getByRole('status')).toHaveTextContent('Saved changes are not applied yet');
});
