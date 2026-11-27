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

  it('accepts literal addresses and ports directly by click, Enter, or blur without creating aliases', async () => {
    const user = userEvent.setup();
    const postCalls: string[] = [];

    fetchRoutes({
      '/api/v1/config/aliases/search': () => jsonResponse(200, {
        state: 'saved',
        savedVersion: '5',
        items: [],
      }),
      'POST /api/v1/config/aliases/addresses': () => {
        postCalls.push('/api/v1/config/aliases/addresses');
        return jsonResponse(500, {});
      },
      'POST /api/v1/config/aliases/ports': () => {
        postCalls.push('/api/v1/config/aliases/ports');
        return jsonResponse(500, {});
      },
    });

    function LiteralHarness({ api }: { api: ApiClient }) {
      const [addrs, setAddrs] = useState<string[]>(['10.0.0.99']);
      const [ports, setPorts] = useState<string[]>([]);
      const [single, setSingle] = useState<string | null>('10.1.0.0/24');
      return (
        <MantineProvider>
          <AliasMultiSelect
            api={api}
            aliasType="addresses"
            family="ipv4"
            debounceMs={200}
            label="Multi addresses"
            initialOptions={[]}
            value={addrs}
            onChange={setAddrs}
          />
          <AliasMultiSelect
            api={api}
            aliasType="ports"
            debounceMs={10}
            label="Multi ports"
            initialOptions={[]}
            value={ports}
            onChange={setPorts}
          />
          <AliasSelect
            api={api}
            aliasType="addresses"
            family="ipv4"
            debounceMs={10}
            label="Single address"
            initialOptions={[]}
            value={single}
            onChange={setSingle}
          />
          <output data-testid="state">{JSON.stringify({ addrs, ports, single })}</output>
          <button type="button">Outside</button>
        </MantineProvider>
      );
    }

    const api = new ApiClient('csrf');
    render(<LiteralHarness api={api} />);

    // Pre-populated literals display cleanly without "(missing)".
    expect(screen.getByText('10.0.0.99', { selector: 'span' })).toBeInTheDocument();
    expect(screen.queryByText(/\(missing\)/)).not.toBeInTheDocument();
    const singleInput = screen.getByLabelText('Single address', { selector: 'input' }) as HTMLInputElement;
    expect(singleInput.value).toBe('10.1.0.0/24');

    // 1. Literal IPv4 host entered immediately via Enter (before 200ms search debounce finishes).
    const addrInput = screen.getByLabelText('Multi addresses', { selector: 'input' });
    await user.type(addrInput, '10.0.0.1{Enter}');
    expect(screen.getByText('10.0.0.1', { selector: 'span' })).toBeInTheDocument();

    // 2. Literal CIDR clicked from dropdown (no "Create alias" hint).
    await user.type(addrInput, '192.168.1.0/24');
    expect(screen.queryByText(/creates/i)).not.toBeInTheDocument();
    await user.click(await screen.findByText('IPv4 network'));
    expect(screen.getByText('192.168.1.0/24', { selector: 'span' })).toBeInTheDocument();

    // 3. Literal range committed on blur; Escape cancels without committing.
    await user.type(addrInput, '10.0.0.20');
    await user.keyboard('{Escape}');
    await user.click(screen.getByRole('button', { name: 'Outside' }));
    expect(screen.queryByText('10.0.0.20', { selector: 'span' })).not.toBeInTheDocument();

    await user.type(addrInput, '10.0.0.10-10.0.0.15');
    await user.click(screen.getByRole('button', { name: 'Outside' }));
    expect(screen.getByText('10.0.0.10-10.0.0.15', { selector: 'span' })).toBeInTheDocument();

    // 4. Bare port in AliasMultiSelect offers TCP and UDP; choosing TCP stores tcp/443 directly.
    const portInput = screen.getByLabelText('Multi ports', { selector: 'input' });
    await user.type(portInput, '443');
    expect(await screen.findByText('UDP 443')).toBeInTheDocument();
    await user.click(screen.getByText('TCP 443'));
    expect(screen.getByText('tcp/443', { selector: 'span' })).toBeInTheDocument();

    // Explicit protocol/port commits on Enter.
    await user.type(portInput, 'udp/53{Enter}');
    expect(screen.getByText('udp/53', { selector: 'span' })).toBeInTheDocument();

    // 5. Literal CIDR in AliasSelect via Enter, followed by editing and committing on blur.
    await user.clear(singleInput);
    await user.type(singleInput, '10.5.0.0/24{Enter}');
    expect(singleInput.value).toBe('10.5.0.0/24');
    await user.clear(singleInput);
    await user.type(singleInput, '10.6.0.0/24');
    await user.click(screen.getByRole('button', { name: 'Outside' }));
    expect(singleInput.value).toBe('10.6.0.0/24');

    expect(JSON.parse(screen.getByTestId('state').textContent ?? '{}')).toEqual({
      addrs: ['10.0.0.99', '10.0.0.1', '192.168.1.0/24', '10.0.0.10-10.0.0.15'],
      ports: ['tcp/443', 'udp/53'],
      single: '10.6.0.0/24',
    });
    expect(postCalls).toEqual([]);
  });

  it('prevents Enter on an invalid literal from submitting an enclosing form and does not re-add after Clear + blur', async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn((e: React.FormEvent) => e.preventDefault());
    fetchRoutes({
      '/api/v1/config/aliases/search': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [] }),
    });

    function FormHarness({ api }: { api: ApiClient }) {
      const [addrs, setAddrs] = useState<string[]>(['10.0.0.1']);
      return (
        <MantineProvider>
          <form onSubmit={onSubmit}>
            <AliasMultiSelect
              api={api}
              aliasType="addresses"
              family="ipv4"
              debounceMs={10}
              label="Addresses"
              clearable
              initialOptions={[]}
              value={addrs}
              onChange={setAddrs}
            />
            <button type="submit">Save</button>
            <button type="button">Outside</button>
          </form>
          <output data-testid="addrs">{JSON.stringify(addrs)}</output>
        </MantineProvider>
      );
    }

    const { container } = render(<FormHarness api={new ApiClient('csrf')} />);
    const input = screen.getByLabelText('Addresses', { selector: 'input' });

    // Typing an invalid CIDR (non-zero host bits) and pressing Enter must not submit the form.
    await user.type(input, '10.0.0.5/24{Enter}');
    expect(onSubmit).not.toHaveBeenCalled();

    // Removing the existing pill while a valid literal is typed and then blurring must not re-commit that literal.
    await user.clear(input);
    await user.type(input, '10.0.0.9');
    const removePill = container.querySelector<HTMLButtonElement>('.mantine-Pill-remove')!;
    expect(removePill).not.toBeNull();
    await user.click(removePill);
    await user.click(screen.getByRole('button', { name: 'Outside' }));
    expect(JSON.parse(screen.getByTestId('addrs').textContent ?? '[]')).toEqual([]);
  });

  it('still accepts literal addresses when the alias search endpoint fails', async () => {
    const user = userEvent.setup();
    fetchRoutes({
      '/api/v1/config/aliases/search': () => jsonResponse(500, { error: { code: 'INTERNAL', message: 'Boom.' } }),
    });

    const api = new ApiClient('csrf');
    render(<MultiHarness api={api} family="ipv4" />);

    const input = screen.getByLabelText('Addresses', { selector: 'input' });
    await user.type(input, '10.0.0.1{Enter}');
    expect(screen.getByText('10.0.0.1', { selector: 'span' })).toBeInTheDocument();
  });

  it('hides dynamic url and hostnames aliases from remote search when excludeDynamic is set and reports resolved items', async () => {
    const user = userEvent.setup();
    const onResolved = vi.fn();
    fetchRoutes({
      '/api/v1/config/aliases/search': () => jsonResponse(200, {
        state: 'saved',
        savedVersion: '5',
        items: [
          { id: 'adr_lan', name: 'lan-net', kind: 'network', summary: '192.0.2.0/24', score: 900, highlights: [{ text: 'lan-net', match: true }] },
          { id: 'adr_grp', name: 'lan-grp', kind: 'group', summary: 'lan-net', score: 850, highlights: [{ text: 'lan-grp', match: true }] },
          { id: 'adr_url', name: 'lan-feed', kind: 'url', summary: 'https://example.com/ips', score: 800, highlights: [{ text: 'lan-feed', match: true }] },
          { id: 'adr_dns', name: 'lan-vpn', kind: 'hostnames', summary: 'vpn.example.com', score: 750, highlights: [{ text: 'lan-vpn', match: true }] },
        ],
      }),
    });

    function DynamicFilterHarness({ api }: { api: ApiClient }) {
      const [selected, setSelected] = useState<string[]>([]);
      const [single, setSingle] = useState<string | null>(null);
      return (
        <MantineProvider>
          <AliasMultiSelect api={api} aliasType="addresses" excludeDynamic debounceMs={10} label="Addresses" initialOptions={[]} value={selected} onChange={setSelected} onItemsResolved={onResolved} />
          <AliasSelect api={api} aliasType="addresses" excludeDynamic debounceMs={10} label="Single address" initialOptions={[]} value={single} onChange={setSingle} onItemsResolved={onResolved} />
        </MantineProvider>
      );
    }

    render(<DynamicFilterHarness api={new ApiClient('csrf')} />);
    await user.type(screen.getByLabelText('Addresses', { selector: 'input' }), 'lan');
    const options = await waitFor(() => {
      const found = screen.getAllByRole('option', { hidden: true });
      expect(found.length).toBe(2);
      return found;
    });
    expect(options.map((o) => o.textContent)).toEqual(['lan-net192.0.2.0/24', 'lan-grplan-net']);
    expect(screen.queryByText('lan-feed')).not.toBeInTheDocument();
    expect(screen.queryByText('lan-vpn')).not.toBeInTheDocument();
    expect(onResolved).toHaveBeenCalledWith(expect.arrayContaining([
      expect.objectContaining({ id: 'adr_url', kind: 'url' }),
      expect.objectContaining({ id: 'adr_dns', kind: 'hostnames' }),
    ]));

    // AliasSelect with excludeDynamic also omits url and hostnames from remote search.
    const singleInput = screen.getByLabelText('Single address', { selector: 'input' });
    await user.type(singleInput, 'lan');
    const singleListbox = document.getElementById(singleInput.getAttribute('aria-controls')!)!;
    await waitFor(() => {
      const opts = Array.from(singleListbox.querySelectorAll('[role="option"]'));
      expect(opts.map((o) => o.textContent)).toEqual(['lan-net192.0.2.0/24', 'lan-grplan-net']);
    });
  });
});
