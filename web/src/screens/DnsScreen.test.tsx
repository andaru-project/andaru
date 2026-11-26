import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router';
import { ApiClient, ApiError } from '../api';
import {
  checkDns, dnsErrors, dnsListenAddresses, dnsPayload, isDnsEligibleSegment, newDnsRowKey, servedDnsIpv6,
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
});

describe('dnsEdit helpers', () => {
  it('computes eligible segments and listen addresses matching engine rules', () => {
    expect(isDnsEligibleSegment(wanSeg)).toBe(false);
    expect(dnsListenAddresses(lanSeg)).toEqual(['192.168.10.1', 'fd00:10::1']);
    const delegatedWithUla: Segment = {
      ...guestSeg,
      addressing: {
        ipv4: { mode: 'none' },
        ipv6: { mode: 'delegated', from: 'seg_wan', subnetId: '2', addresses: ['fe80::1/64', 'fd00:20::1/64'] },
      },
    };
    expect(servedDnsIpv6(delegatedWithUla)).toBe('fd00:20::1');
    expect(isDnsEligibleSegment(delegatedWithUla)).toBe(true);
  });

  it('validates forward mode requirements and omits upstreams in recursive payload', () => {
    const draft = {
      mode: 'forward' as const,
      protocol: 'dot' as const,
      upstreams: [{ key: newDnsRowKey(), address: '1.1.1.1', tlsName: '' }],
      segments: ['seg_lan'],
      virtualAddresses: [],
      localDomain: 'home.arpa',
      searchDomains: 'home.arpa, lab.example',
      registerLeases: false,
    };
    expect(checkDns(draft).get('upstreams/0/tlsName')).toBe('DNS over TLS needs the certificate hostname.');
    expect(dnsPayload({ ...draft, mode: 'recursive' })).toEqual({
      mode: 'recursive',
      segments: ['seg_lan'],
      localDomain: 'home.arpa',
      searchDomains: ['home.arpa', 'lab.example'],
      registerLeases: false,
    });
  });

  it('maps server DNS error pointers cleanly', () => {
    const err = new ApiError(422, 'VALIDATION_FAILED', 'Validation failed.', [
      { path: '/dns/upstreams/0/tlsName', message: 'Bad TLS name' },
      { path: '/dns/searchDomains/2', message: 'Invalid search domain.' },
      { path: '/dns/segments/0', message: 'Expected static segment.' },
      { path: '/dns/virtualAddresses/0', message: 'Unknown virtual address.' },
    ]);
    const mapped = dnsErrors(err);
    expect(mapped.get('upstreams/0/tlsName')).toBe('Bad TLS name');
    expect(mapped.get('searchDomains')).toBe('Invalid search domain.');
    expect(mapped.get('segments')).toBe('Expected static segment.');
    expect(mapped.get('virtualAddresses')).toBe('Unknown virtual address.');
  });
});
