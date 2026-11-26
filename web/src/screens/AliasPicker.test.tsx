import { MantineProvider } from '@mantine/core';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { AliasMultiSelect, AliasSelect } from './AliasPicker';

afterEach(() => {
  vi.restoreAllMocks();
});

function MultiHarness({
  api,
  initial = [],
  onSessionLost,
  family,
  excludeGroups,
  exclude,
}: {
  api: ApiClient;
  initial?: string[];
  onSessionLost?: () => void;
  family?: 'ipv4' | 'ipv6';
  excludeGroups?: boolean;
  exclude?: string;
}) {
  const [value, setValue] = useState<string[]>(initial);
  return (
    <MantineProvider>
      <AliasMultiSelect
        api={api}
        aliasType="addresses"
        family={family}
        excludeGroups={excludeGroups}
        exclude={exclude}
        debounceMs={120}
        label="Addresses"
        initialOptions={[{ value: 'adr_lan', label: 'lan-net' }]}
        value={value}
        onSessionLost={onSessionLost}
        onChange={setValue}
      />
    </MantineProvider>
  );
}

function SingleHarness({ api, initial = null }: { api: ApiClient; initial?: string | null }) {
  const [value, setValue] = useState<string | null>(initial);
  return (
    <MantineProvider>
      <AliasSelect
        api={api}
        aliasType="addresses"
        family="ipv4"
        debounceMs={20}
        label="Destination alias"
        initialOptions={[{ value: 'adr_lan', label: 'lan-net' }]}
        value={value}
        onChange={setValue}
      />
      <button type="button" onClick={() => setValue('adr_lan')}>Reset external</button>
    </MantineProvider>
  );
}

describe('AliasPicker', () => {
  it('debounces rapid typing, renders backend similarity order with bold highlight segments, and preserves selected labels', async () => {
    const onConnection = vi.fn();
    const fetchMock = fetchRoutes({
      '/api/v1/config/aliases/search': () => jsonResponse(200, {
        state: 'saved',
        savedVersion: '3',
        items: [
          {
            id: 'adr_websrv',
            name: 'web-servers',
            kind: 'network',
            summary: '192.168.10.0/24',
            score: 410,
            highlights: [
              { text: 'w', match: true },
              { text: 'eb-', match: false },
              { text: 's', match: true },
              { text: 'ervers', match: false },
            ],
            summaryHighlights: [
              { text: '192.168.10.0/24', match: false },
            ],
          },
          {
            id: 'adr_ws_proxy',
            name: 'edge-ws-proxy',
            kind: 'host',
            summary: '10.0.0.5',
            score: 350,
            highlights: [
              { text: 'edge-', match: false },
              { text: 'ws', match: true },
              { text: '-proxy', match: false },
            ],
          },
        ],
      }),
    });

    const user = userEvent.setup();
    const api = new ApiClient('csrf', onConnection);
    render(<MultiHarness api={api} family="ipv4" excludeGroups exclude="adr_self" />);

    const input = screen.getByLabelText('Addresses', { selector: 'input' });
    // Rapid typing inside debounce window should collapse into one API request.
    await user.type(input, 'ws');

    const options = await waitFor(() => {
      const found = screen.getAllByRole('option', { hidden: true });
      expect(found.length).toBe(2);
      return found;
    });

    const requestedUrls = fetchMock.mock.calls.map(([url]) => String(url));
    expect(requestedUrls).toHaveLength(1);
    const params = new URL(requestedUrls[0], 'http://localhost').searchParams;
    expect(params.get('type')).toBe('addresses');
    expect(params.get('q')).toBe('ws');
    expect(params.get('family')).toBe('ipv4');
    expect(params.get('excludeGroups')).toBe('true');
    expect(params.get('exclude')).toBe('adr_self');

    // Exact similarity order preserved even though "ws" is not a substring of "web-servers".
    expect(options[0].textContent).toContain('web-servers');
    expect(options[0].textContent).toContain('192.168.10.0/24');
    expect(options[1].textContent).toContain('edge-ws-proxy');

    // Matching segments are bolded with <strong>.
    const strongs = Array.from(options[0].querySelectorAll('strong')).map((el) => el.textContent);
    expect(strongs).toEqual(['w', 's']);

    // Picking an item not in initialOptions preserves its name in the selected pill.
    await user.click(options[0]);
    expect(await screen.findByText('web-servers', { selector: 'span' })).toBeInTheDocument();
    expect(onConnection).not.toHaveBeenCalledWith(false);
  });

  it('supports single-select alias autocomplete, cancels in-flight search on external value change, and handles 401 and errors', async () => {
    const user = userEvent.setup();
    const onSessionLost = vi.fn();
    let mode: 'ok' | 'hold' | 'unauth' | 'fail' = 'ok';
    let heldSignal: AbortSignal | undefined;
    let releaseHeld: ((res: Response) => void) | undefined;
    fetchRoutes({
      '/api/v1/config/aliases/search': (init) => {
        if (mode === 'hold') {
          heldSignal = init?.signal ?? undefined;
          return new Promise<Response>((resolve) => { releaseHeld = resolve; });
        }
        if (mode === 'unauth') return jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Expired.' } });
        if (mode === 'fail') return jsonResponse(500, { error: { code: 'INTERNAL', message: 'Boom.' } });
        return jsonResponse(200, {
          state: 'saved',
          savedVersion: '4',
          items: [
            {
              id: 'adr_db',
              name: 'db-primary',
              kind: 'host',
              summary: '10.1.0.10',
              score: 850,
              highlights: [{ text: 'db', match: true }, { text: '-primary', match: false }],
            },
          ],
        });
      },
    });

    const api = new ApiClient('csrf');
    const { unmount } = render(<SingleHarness api={api} />);
    const singleInput = screen.getByLabelText('Destination alias', { selector: 'input' });
    await user.type(singleInput, 'db');
    const opt = await waitFor(() => {
      const found = screen.getByRole('option', { hidden: true });
      expect(found.querySelector('strong')?.textContent).toBe('db');
      return found;
    });
    await user.click(opt);
    expect((singleInput as HTMLInputElement).value).toBe('db-primary');

    // External value change while a search is in flight aborts the request and ignores stale results.
    mode = 'hold';
    await user.clear(singleInput);
    await user.type(singleInput, 'web');
    await waitFor(() => expect(releaseHeld).toBeDefined());
    await user.click(screen.getByRole('button', { name: 'Reset external' }));
    expect((singleInput as HTMLInputElement).value).toBe('lan-net');
    expect(heldSignal?.aborted).toBe(true);
    releaseHeld?.(jsonResponse(200, {
      state: 'saved',
      savedVersion: '4',
      items: [{ id: 'adr_stale', name: 'web-stale', kind: 'host', summary: '10.9.9.9', score: 900, highlights: [{ text: 'web', match: true }, { text: '-stale', match: false }] }],
    }));
    await waitFor(() => expect((singleInput as HTMLInputElement).value).toBe('lan-net'));
    expect(screen.queryByText('web-stale')).not.toBeInTheDocument();
    unmount();

    mode = 'fail';
    render(<MultiHarness api={api} onSessionLost={onSessionLost} />);
    const multiInput = screen.getByLabelText('Addresses', { selector: 'input' });
    await user.type(multiInput, 'xyz');
    expect(await screen.findByText('Could not search aliases.')).toBeInTheDocument();

    mode = 'unauth';
    await user.clear(multiInput);
    await user.type(multiInput, 'abc');
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
  });
});
