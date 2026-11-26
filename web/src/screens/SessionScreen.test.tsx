import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen, waitFor } from '../test/render';
import { SessionScreen } from './SessionScreen';

afterEach(() => {
  vi.restoreAllMocks();
});

const currentId = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaCURR0001';
const otherId = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbOTHR0002';

describe('SessionScreen', () => {
  it('lists active sessions, revokes another session with CSRF and reloads, and confirms before revoking current session', async () => {
    const user = userEvent.setup();
    const onSessionLost = vi.fn();
    let sessions = [
      { id: currentId, createdAt: '2026-11-26T08:00:00Z', lastSeenAt: '2026-11-26T09:30:00Z', expiresAt: '2026-11-26T20:00:00Z', current: true },
      { id: otherId, createdAt: '2026-11-26T07:00:00Z', lastSeenAt: '2026-11-26T08:15:00Z', expiresAt: '2026-11-26T19:00:00Z', current: false },
    ];
    const fetchMock = fetchRoutes({
      '/api/v1/sessions': () => jsonResponse(200, { items: sessions }),
      [`DELETE /api/v1/sessions/${otherId}`]: () => {
        sessions = sessions.filter((s) => s.id !== otherId);
        return new Response(null, { status: 204 });
      },
      [`DELETE /api/v1/sessions/${currentId}`]: () => new Response(null, { status: 204 }),
    });

    render(<SessionScreen api={new ApiClient('csrf-tok')} session={session(['status.read'])} onSessionLost={onSessionLost} />);

    expect(await screen.findByText('Current')).toBeInTheDocument();
    expect(screen.getByText('…OTHR0002')).toBeInTheDocument();

    // Revoking another session sends DELETE with CSRF header and reloads list.
    await user.click(screen.getByRole('button', { name: 'Revoke session ending in OTHR0002' }));
    await waitFor(() => expect(screen.queryByText('…OTHR0002')).not.toBeInTheDocument());
    const deleteCall = fetchMock.mock.calls.find(([url, init]) => url === `/api/v1/sessions/${otherId}` && init?.method === 'DELETE');
    expect(deleteCall).toBeDefined();
    expect(new Headers(deleteCall?.[1]?.headers).get('X-Andaru-CSRF')).toBe('csrf-tok');
    expect(onSessionLost).not.toHaveBeenCalled();

    // Revoking current session asks for confirmation and triggers onSessionLost.
    await user.click(screen.getByRole('button', { name: 'Revoke current session' }));
    expect(await screen.findByText(/signs you out of this browser immediately/i)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Revoke session' }));
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
  });

  it('surfaces errors and signs out on 401 when revoking a session', async () => {
    const user = userEvent.setup();
    const onSessionLost = vi.fn();
    let mode: 'fail' | 'unauth' = 'fail';
    fetchRoutes({
      '/api/v1/sessions': () => jsonResponse(200, {
        items: [
          { id: currentId, createdAt: '2026-11-26T08:00:00Z', lastSeenAt: '2026-11-26T09:30:00Z', expiresAt: '2026-11-26T20:00:00Z', current: true },
          { id: otherId, createdAt: '2026-11-26T07:00:00Z', lastSeenAt: '2026-11-26T08:15:00Z', expiresAt: '2026-11-26T19:00:00Z', current: false },
        ],
      }),
      [`DELETE /api/v1/sessions/${otherId}`]: () => (
        mode === 'fail'
          ? jsonResponse(503, { error: { code: 'UNAVAILABLE', message: 'Identity unavailable.' } })
          : jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Expired.' } })
      ),
    });

    render(<SessionScreen api={new ApiClient('csrf-tok')} session={session(['status.read'])} onSessionLost={onSessionLost} />);
    await screen.findByText('…OTHR0002');

    await user.click(screen.getByRole('button', { name: 'Revoke session ending in OTHR0002' }));
    expect(await screen.findByText('Identity unavailable.')).toBeInTheDocument();

    mode = 'unauth';
    await user.click(screen.getByRole('button', { name: 'Revoke session ending in OTHR0002' }));
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
  });
});
