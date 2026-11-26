import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router';
import { ApiClient, ApiError } from '../api';
import { newRowKey } from '../dhcpEdit';
import {
  checkDns, dnsErrors, dnsListenAddresses, dnsPayload, isDnsEligibleSegment, pruneDnsDraft, servedDnsIpv6,
} from '../dnsEdit';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render } from '../test/render';
import type { DnsService, Segment, Snapshot } from '../types';
import { DnsScreen } from './DnsScreen';

const rwCaps = ['configuration.read', 'configuration.write'];
const roCaps = ['configuration.read'];

const lanSeg: Segment = {
  id: 'seg_lan',
  name: 'lan',
  link: 'lnk_lan',
  zone: 'zon_lan',
  addressing: {
    ipv4: { mode: 'static', addresses: ['192.168.10.1/24'] },
    ipv6: { mode: 'static', addresses: ['fd00:10::1/64'] },
  },
};

const guestSeg: Segment = {
  id: 'seg_guest',
  name: 'guest',
  link: 'lnk_guest',
  zone: 'zon_guest',
  addressing: {
    ipv4: { mode: 'static', addresses: ['192.168.20.1/24'] },
    ipv6: { mode: 'delegated', from: 'seg_wan', subnetId: '2' },
  },
};

const wanSeg: Segment = {
  id: 'seg_wan',
  name: 'wan',
  link: 'lnk_wan',
  zone: 'zon_wan',
  addressing: {
    ipv4: { mode: 'dhcp' },
    ipv6: { mode: 'dhcp6', requestPrefixLength: 56 },
  },
};

const sampleSnapshot: Snapshot = {
  state: 'saved',
  savedVersion: '8',
  value: {
    links: [],
    segments: [wanSeg, lanSeg, guestSeg],
    zones: [
      { id: 'zon_wan', name: 'wan' },
      { id: 'zon_lan', name: 'lan' },
      { id: 'zon_guest', name: 'guest' },
    ],
    management: { segment: 'seg_lan' },
    dhcp: [{ id: 'dhp_lan', name: 'lan', segment: 'seg_lan', ranges: [{ first: '192.168.10.100', last: '192.168.10.200' }] }],
    virtualAddresses: [{ id: 'vip_dns', name: 'dns-vip', address: '172.16.53.53' }],
  },
};

const existingDns: DnsService = {
  mode: 'forward',
  upstreams: [{ address: '1.1.1.1', protocol: 'dot', tlsName: 'cloudflare-dns.com' }],
  segments: ['seg_lan'],
  localDomain: 'home.arpa',
  searchDomains: ['home.arpa', 'lab.example'],
  registerLeases: true,
};

function renderDns(caps = rwCaps, onSaved = vi.fn()) {
  const api = new ApiClient('');
  api.setCSRFToken('csrf');
  return render(
    <MemoryRouter>
      <DnsScreen api={api} session={session(caps)} onSaved={onSaved} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
}

describe('DnsScreen', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('enables DNS service from off state in recursive mode with If-Match dns.0', async () => {
    let putBody: Record<string, unknown> | undefined;
    let putHeaders: HeadersInit | undefined;
    let savedDns: DnsService | null = null;
    fetchRoutes({
      '/api/v1/config': sampleSnapshot,
      '/api/v1/config/dns': () => (savedDns
        ? jsonResponse(200, { state: 'saved', savedVersion: '9', version: '1', value: savedDns })
        : jsonResponse(404, { error: { code: 'NOT_FOUND', message: 'Not configured.', details: [] } })),
      'PUT /api/v1/config/dns': (init) => {
        putHeaders = init?.headers;
        putBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        savedDns = putBody as unknown as DnsService;
        return jsonResponse(200, { state: 'saved', savedVersion: '9', version: '1', value: savedDns });
      },
    });
    const onSaved = vi.fn();
    renderDns(rwCaps, onSaved);

    expect(await screen.findByText('DNS service is off')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Enable DNS service' }));

    // Eligible non-WAN segments are pre-selected with their listen addresses shown.
    expect(screen.getByText('192.168.10.1, fd00:10::1')).toBeInTheDocument();
    expect(screen.getByText('192.168.20.1')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('checkbox', { name: /dns-vip/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Enable DNS' }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(new Headers(putHeaders).get('If-Match')).toBe('"dns.0"');
    expect(putBody).toEqual({
      mode: 'recursive',
      segments: ['seg_guest', 'seg_lan'],
      localDomain: 'home.arpa',
      registerLeases: true,
      virtualAddresses: ['vip_dns'],
    });
  });

  it('edits forwarding DNS over TLS upstreams, prunes blank rows, and maps 422 errors', async () => {
    let putBody: Record<string, unknown> | undefined;
    let attempt = 0;
    fetchRoutes({
      '/api/v1/config': { ...sampleSnapshot, value: { ...sampleSnapshot.value, dns: existingDns } },
      '/api/v1/config/dns': { state: 'saved', savedVersion: '8', version: '4', value: existingDns },
      'PUT /api/v1/config/dns': (init) => {
        attempt += 1;
        putBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        if (attempt === 1) {
          return jsonResponse(422, {
            error: {
              code: 'VALIDATION_FAILED',
              message: 'Validation failed.',
              details: [{ path: '/dns/upstreams/1/address', message: 'The DNS service itself listens on this address; it would ask itself.' }],
            },
          });
        }
        return jsonResponse(200, { state: 'saved', savedVersion: '9', version: '5', value: putBody });
      },
    });
    const onSaved = vi.fn();
    renderDns(rwCaps, onSaved);

    expect(await screen.findByDisplayValue('cloudflare-dns.com')).toBeInTheDocument();
    // Add a blank upstream row before a populated row to verify blank-row pruning aligns 422 indices.
    fireEvent.click(screen.getByRole('button', { name: 'Add upstream' }));
    fireEvent.click(screen.getByRole('button', { name: 'Add upstream' }));
    fireEvent.change(screen.getByLabelText('Upstream 3 address'), { target: { value: '192.168.10.1' } });
    fireEvent.change(screen.getByLabelText('Upstream 3 TLS server name'), { target: { value: 'dns.quad9.net' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    expect(await screen.findByText('The DNS service itself listens on this address; it would ask itself.')).toBeInTheDocument();
    // Blank row 2 was pruned so the second populated row is now Upstream 2.
    expect(screen.getByLabelText('Upstream 2 address')).toHaveValue('192.168.10.1');

    fireEvent.change(screen.getByLabelText('Upstream 2 address'), { target: { value: '9.9.9.9' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(putBody?.upstreams).toEqual([
      { address: '1.1.1.1', protocol: 'dot', tlsName: 'cloudflare-dns.com' },
      { address: '9.9.9.9', protocol: 'dot', tlsName: 'dns.quad9.net' },
    ]);
  });

  it('disables DNS service with two-step confirmation and If-Match header', async () => {
    let deleteHeaders: HeadersInit | undefined;
    let deleted = false;
    fetchRoutes({
      '/api/v1/config': () => jsonResponse(200, {
        ...sampleSnapshot,
        value: { ...sampleSnapshot.value, ...(deleted ? {} : { dns: existingDns }) },
      }),
      '/api/v1/config/dns': () => (deleted
        ? jsonResponse(404, { error: { code: 'NOT_FOUND', message: 'Not configured.', details: [] } })
        : jsonResponse(200, { state: 'saved', savedVersion: '8', version: '4', value: existingDns })),
      'DELETE /api/v1/config/dns': (init) => {
        deleteHeaders = init?.headers;
        deleted = true;
        return new Response(null, { status: 204 });
      },
    });
    const onSaved = vi.fn();
    renderDns(rwCaps, onSaved);

    fireEvent.click(await screen.findByRole('button', { name: 'Disable DNS…' }));
    expect(screen.getByText('Disable the DNS service?')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Disable DNS' }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(new Headers(deleteHeaders).get('If-Match')).toBe('"dns.4"');
    expect(await screen.findByText('DNS service is off')).toBeInTheDocument();
  });

  it('warns when a segment declares per-segment DNS and hides write controls for read-only users', async () => {
    const segWithHostDns: Segment = {
      ...lanSeg,
      addressing: { ...lanSeg.addressing, dns: { servers: ['1.1.1.1'] } },
    };
    fetchRoutes({
      '/api/v1/config': { ...sampleSnapshot, value: { ...sampleSnapshot.value, segments: [wanSeg, segWithHostDns], dns: existingDns } },
      '/api/v1/config/dns': { state: 'saved', savedVersion: '8', version: '4', value: existingDns },
    });
    renderDns(roCaps);

    expect(await screen.findByText('Per-segment DNS servers must be moved first')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Disable DNS…' })).not.toBeInTheDocument();
  });

  it('hides Enable button in off state for read-only accounts', async () => {
    fetchRoutes({
      '/api/v1/config': sampleSnapshot,
      '/api/v1/config/dns': () => jsonResponse(404, { error: { code: 'NOT_FOUND', message: 'Not configured.', details: [] } }),
    });
    renderDns(roCaps);

    expect(await screen.findByText('DNS service is off')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Enable DNS service' })).not.toBeInTheDocument();
  });

  it('renders dangling segment and virtual address IDs for removal and warns when management segment is missing', async () => {
    const dnsWithDangling: DnsService = {
      mode: 'recursive',
      segments: ['seg_lan', 'seg_deleted'],
      virtualAddresses: ['vip_deleted'],
      localDomain: 'home.arpa',
      registerLeases: true,
    };
    const noMgmtSnapshot: Snapshot = {
      ...sampleSnapshot,
      value: { ...sampleSnapshot.value, management: undefined, virtualAddresses: [], dns: dnsWithDangling },
    };
    let putBody: Record<string, unknown> | undefined;
    fetchRoutes({
      '/api/v1/config': noMgmtSnapshot,
      '/api/v1/config/dns': { state: 'saved', savedVersion: '8', version: '4', value: dnsWithDangling },
      'PUT /api/v1/config/dns': (init) => {
        putBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(200, { state: 'saved', savedVersion: '9', version: '5', value: putBody });
      },
    });
    renderDns();

    expect(await screen.findByText('Virtual addresses require a management segment')).toBeInTheDocument();
    expect(screen.getByText('Missing segment')).toBeInTheDocument();
    expect(screen.getByText('Missing virtual address')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('checkbox', { name: 'seg_deleted' }));
    fireEvent.click(screen.getByRole('checkbox', { name: 'vip_deleted' }));
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(putBody).toBeDefined());
    expect(putBody).toEqual({
      mode: 'recursive',
      segments: ['seg_lan'],
      localDomain: 'home.arpa',
      registerLeases: true,
    });
  });

  it('surfaces cross-collection 422 errors in the top banner and warns on remote version change while dirty', async () => {
    let version = '4';
    fetchRoutes({
      '/api/v1/config': { ...sampleSnapshot, value: { ...sampleSnapshot.value, dns: existingDns } },
      '/api/v1/config/dns': () => jsonResponse(200, { state: 'saved', savedVersion: '8', version, value: existingDns }),
      'PUT /api/v1/config/dns': () => jsonResponse(422, {
        error: {
          code: 'VALIDATION_FAILED',
          message: 'Validation failed.',
          details: [{ path: '/segments/0/addressing/dns', message: 'The DNS service is declared: move these servers to its upstreams.' }],
        },
      }),
    });
    renderDns();

    const domainInput = await screen.findByLabelText('Local domain');
    fireEvent.change(domainInput, { target: { value: 'lan.example' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    expect(await screen.findByText('The DNS service is declared: move these servers to its upstreams.')).toBeInTheDocument();

    // Simulate a remote update arriving on Refresh while edits are dirty.
    version = '5';
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    expect(await screen.findByText('Saved DNS configuration changed elsewhere')).toBeInTheDocument();
  });

  it('keeps newly enabled DNS state visible even if an earlier refresh resolves during PUT and post-PUT reload fails', async () => {
    let configCalls = 0;
    let releaseOverlapRefresh!: () => void;
    let releasePut!: () => void;
    fetchRoutes({
      '/api/v1/config': () => {
        configCalls += 1;
        if (configCalls === 1) return jsonResponse(200, sampleSnapshot);
        if (configCalls === 2) {
          return new Promise<Response>((resolve) => {
            releaseOverlapRefresh = () => resolve(jsonResponse(200, sampleSnapshot));
          });
        }
        return jsonResponse(503, { error: { code: 'UNAVAILABLE', message: 'Temporary read error.', details: [] } });
      },
      '/api/v1/config/dns': () => jsonResponse(404, { error: { code: 'NOT_FOUND', message: 'Not configured.', details: [] } }),
      'PUT /api/v1/config/dns': (init) => new Promise<Response>((resolve) => {
        const body = JSON.parse(String(init?.body)) as DnsService;
        releasePut = () => resolve(jsonResponse(200, { state: 'saved', savedVersion: '9', version: '1', value: body }));
      }),
    });
    renderDns();

    fireEvent.click(await screen.findByRole('button', { name: 'Enable DNS service' }));
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() => expect(releaseOverlapRefresh).toBeDefined());
    fireEvent.click(screen.getByRole('button', { name: 'Enable DNS' }));
    await waitFor(() => expect(releasePut).toBeDefined());

    // Finish the earlier refresh while PUT is in flight, then complete PUT (whose follow-up reload fails with 503).
    releaseOverlapRefresh();
    await waitFor(() => expect(configCalls).toBe(2));
    releasePut();

    expect(await screen.findByText('Temporary read error.')).toBeInTheDocument();
    await waitFor(() => expect(configCalls).toBe(3));
    expect(screen.getByText('Serving')).toBeInTheDocument();
    expect(screen.queryByText('DNS service is off')).not.toBeInTheDocument();
  });

  it('keeps disabled DNS state visible even if an earlier refresh resolves during DELETE and post-DELETE reload fails', async () => {
    let configCalls = 0;
    let releaseOverlapRefresh!: () => void;
    let releaseDelete!: () => void;
    fetchRoutes({
      '/api/v1/config': () => {
        configCalls += 1;
        if (configCalls === 1) return jsonResponse(200, { ...sampleSnapshot, value: { ...sampleSnapshot.value, dns: existingDns } });
        if (configCalls === 2) {
          return new Promise<Response>((resolve) => {
            releaseOverlapRefresh = () => resolve(jsonResponse(200, { ...sampleSnapshot, value: { ...sampleSnapshot.value, dns: existingDns } }));
          });
        }
        return jsonResponse(503, { error: { code: 'UNAVAILABLE', message: 'Temporary read error.', details: [] } });
      },
      '/api/v1/config/dns': () => jsonResponse(200, { state: 'saved', savedVersion: '8', version: '4', value: existingDns }),
      'DELETE /api/v1/config/dns': () => new Promise<Response>((resolve) => {
        releaseDelete = () => resolve(new Response(null, { status: 204 }));
      }),
    });
    renderDns();

    fireEvent.click(await screen.findByRole('button', { name: 'Disable DNS…' }));
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() => expect(releaseOverlapRefresh).toBeDefined());
    fireEvent.click(screen.getByRole('button', { name: 'Disable DNS' }));
    await waitFor(() => expect(releaseDelete).toBeDefined());

    // Finish the earlier refresh while DELETE is in flight, then complete DELETE (whose follow-up reload fails with 503).
    releaseOverlapRefresh();
    await waitFor(() => expect(configCalls).toBe(2));
    releaseDelete();

    expect(await screen.findByText('Temporary read error.')).toBeInTheDocument();
    await waitFor(() => expect(configCalls).toBe(3));
    expect(screen.getByText('DNS service is off')).toBeInTheDocument();
    expect(screen.queryByText('Serving')).not.toBeInTheDocument();
  });
});

describe('dnsEdit helpers', () => {
  it('computes eligible segments and listen addresses strictly matching engine rules', () => {
    expect(isDnsEligibleSegment(wanSeg)).toBe(false);
    expect(dnsListenAddresses(lanSeg)).toEqual(['192.168.10.1', 'fd00:10::1']);
    const delegatedWithUla: Segment = {
      ...guestSeg,
      addressing: {
        ipv4: { mode: 'none' },
        ipv6: { mode: 'delegated', from: 'seg_wan', subnetId: '2', addresses: ['fe80::1/64', '::1/128', 'ff02::1/64', 'not-an-ip/64', 'fd00:20::1/64'] },
      },
    };
    expect(servedDnsIpv6(delegatedWithUla)).toBe('fd00:20::1');
    expect(isDnsEligibleSegment(delegatedWithUla)).toBe(true);
  });

  it('prunes do53 rows with residual tlsName, strips tlsName in do53 payload, and omits upstreams in recursive payload', () => {
    const draft = {
      mode: 'forward' as const,
      protocol: 'do53' as const,
      upstreams: [
        { key: newRowKey(), address: '', tlsName: 'leftover.example' },
        { key: newRowKey(), address: '1.1.1.1', tlsName: 'leftover.example' },
      ],
      segments: ['seg_lan'],
      virtualAddresses: [],
      localDomain: 'home.arpa',
      searchDomains: 'home.arpa, lab.example',
      registerLeases: false,
    };
    const pruned = pruneDnsDraft(draft);
    expect(pruned.upstreams).toHaveLength(1);
    expect(checkDns(pruned).size).toBe(0);
    expect(dnsPayload(pruned).upstreams).toEqual([{ address: '1.1.1.1', protocol: 'do53' }]);
    expect(dnsPayload({ ...draft, mode: 'recursive' })).toEqual({
      mode: 'recursive',
      segments: ['seg_lan'],
      localDomain: 'home.arpa',
      searchDomains: ['home.arpa', 'lab.example'],
      registerLeases: false,
    });
  });

  it('maps server DNS error pointers and falls back unrendered paths to top-level banner', () => {
    const err = new ApiError(422, 'VALIDATION_FAILED', 'Validation failed.', [
      { path: '/dns/upstreams/0/tlsName', message: 'Bad TLS name' },
      { path: '/dns/upstreams/0/protocol', message: 'Mixed protocol' },
      { path: '/dns/searchDomains/2', message: 'Invalid search domain.' },
      { path: '/dns/segments/0', message: 'Expected static segment.' },
      { path: '/dns/virtualAddresses/0', message: 'Unknown virtual address.' },
      { path: '/segments/0/addressing/dns', message: 'Move per-segment DNS.' },
    ]);
    const mapped = dnsErrors(err, 'dot');
    expect(mapped.get('upstreams/0/tlsName')).toBe('Bad TLS name');
    expect(mapped.get('upstreams')).toBe('Mixed protocol');
    expect(mapped.get('searchDomains')).toBe('Invalid search domain.');
    expect(mapped.get('segments')).toBe('Expected static segment.');
    expect(mapped.get('virtualAddresses')).toBe('Unknown virtual address.');
    expect(mapped.get('')).toBe('Move per-segment DNS.');
  });
});
