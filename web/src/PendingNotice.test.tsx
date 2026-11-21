import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { ApiClient } from './api';
import { clock, type ChangeState } from './changes';
import { PendingNotice } from './PendingNotice';
import { fetchRoutes, jsonResponse } from './test/fetch';
import { apply, pointers, session } from './test/fixtures';
import { act, render, screen } from './test/render';
import type { Session } from './types';

const admin = session(['configuration.read', 'configuration.apply', 'configuration.confirm']);

function notice(state: ChangeState, who: Session = admin, onChanged = vi.fn()) {
  return <MemoryRouter><PendingNotice api={new ApiClient('csrf')} session={who} state={state} onChanged={onChanged} onSessionLost={vi.fn()} /></MemoryRouter>;
}

const awaiting = (deadline: string, id = '2'): ChangeState => ({ applies: { pointers: { ...pointers, live: id, pending: id }, items: [apply({ id, state: 'awaiting_confirmation', deadline })] }, head: '3' });

afterEach(() => vi.useRealTimers());

it('says saved changes are not applied when the saved head is newer than the live generation', () => {
  render(notice({ applies: { pointers: { ...pointers, live: '1' }, items: [apply({})] }, head: '4' }));
  expect(screen.getByRole('status')).toHaveTextContent('Saved changes are not applied yet');
  expect(screen.getByRole('link', { name: 'Review and apply' })).toHaveAttribute('href', '/changes');
});

it('compares the saved head with the live generation, not with the newest apply', () => {
  // Apply 2 of version 5 is confirmed, but a boot fell back to apply 1 of version 3.
  render(notice({ applies: { pointers: { ...pointers, confirmed: '2', live: '1' }, items: [apply({ id: '2', savedVersion: '5' }), apply({})] }, head: '5' }));
  expect(screen.getByRole('status')).toHaveTextContent('Saved changes are not applied yet');
});

it('says nothing when the live generation runs the saved head', () => {
  render(notice({ applies: { pointers: { ...pointers, live: '1' }, items: [apply({})] }, head: '3' }));
  expect(screen.queryByRole('status')).not.toBeInTheDocument();
});

it('says an apply is activating, with nothing to act on', () => {
  render(notice({ applies: { pointers: { ...pointers, live: '1' }, items: [apply({ id: '2', savedVersion: '4', state: 'activating' }), apply({})] }, head: '4' }));
  expect(screen.getByRole('status')).toHaveTextContent('Applying saved version 4…');
  expect(screen.queryByRole('link')).not.toBeInTheDocument();
  expect(screen.queryByRole('button')).not.toBeInTheDocument();
});

it('counts down to the server deadline and offers Confirm and Roll back', async () => {
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] });
  vi.setSystemTime(new Date('2026-11-21T10:00:00Z'));
  const deadline = '2026-11-21T10:01:30Z';
  const fetchMock = fetchRoutes({ 'POST /api/v1/applies/2/confirmation': apply({ id: '2', state: 'confirmed' }) });
  const onChanged = vi.fn();
  render(notice(awaiting(deadline), admin, onChanged));

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

it('counts down correctly from the first render after a deadline appears', () => {
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] });
  vi.setSystemTime(new Date('2026-11-21T10:00:00Z'));
  const { rerender } = render(notice({ applies: { pointers: { ...pointers, live: '1' }, items: [apply({})] }, head: '3' }));
  vi.setSystemTime(new Date('2026-11-21T10:05:00Z'));
  rerender(notice(awaiting('2026-11-21T10:06:00Z')));
  expect(screen.getByText('1:00 left')).toBeInTheDocument();
});

it('disables Confirm and Roll back while a request runs', async () => {
  let answer: (response: Response) => void = () => {};
  fetchRoutes({ 'POST /api/v1/applies/2/rollback': () => new Promise<Response>((resolve) => { answer = resolve; }) });
  render(notice(awaiting('2026-11-21T12:00:00Z')));

  await userEvent.click(screen.getByRole('button', { name: 'Roll back' }));
  expect(screen.getByRole('button', { name: 'Confirm' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Roll back' })).toBeDisabled();
  await act(async () => { answer(jsonResponse(200, apply({ id: '2', state: 'rolled_back' }))); });
  expect(screen.getByRole('button', { name: 'Confirm' })).toBeEnabled();
});

it('drops an error once the notice shows another apply', async () => {
  fetchRoutes({ 'POST /api/v1/applies/2/confirmation': () => jsonResponse(409, { error: { code: 'APPLY_NOT_AWAITING', message: 'Apply 2 is rolled_back.' } }) });
  const { rerender } = render(notice(awaiting('2026-11-21T12:00:00Z')));

  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }));
  expect(await screen.findByText('Apply 2 is rolled_back.')).toBeInTheDocument();
  rerender(notice(awaiting('2026-11-21T12:10:00Z', '3')));
  expect(screen.queryByText('Apply 2 is rolled_back.')).not.toBeInTheDocument();
});

it('hides Confirm without configuration.confirm', () => {
  render(notice(awaiting('2026-11-21T12:00:00Z'), session(['configuration.read', 'configuration.apply'])));
  expect(screen.queryByRole('button', { name: 'Confirm' })).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Roll back' })).toBeInTheDocument();
});

it.each([
  ['failed', 'The last apply failed'],
  ['rolled_back', 'The last apply was rolled back'],
] as const)('reports a %s apply while nothing newer is saved', (state, title) => {
  render(notice({ applies: { pointers: { ...pointers, live: '1' }, items: [apply({ id: '2', savedVersion: '4', state }), apply({})] }, head: '4' }));
  expect(screen.getByRole('status')).toHaveTextContent(title);
  expect(screen.getByRole('link', { name: 'See its details' })).toHaveAttribute('href', '/changes');
});

it('moves on from a failed apply to unapplied changes once something newer is saved', () => {
  render(notice({ applies: { pointers: { ...pointers, live: '1' }, items: [apply({ id: '2', savedVersion: '4', state: 'failed' }), apply({})] }, head: '5' }));
  expect(screen.getByRole('status')).toHaveTextContent('Saved changes are not applied yet');
});
