import { describe, expect, it } from 'vitest';
import {
  formatRefreshInterval,
  isInsecureHttpUrl,
  validateAliasHostname,
  validateAliasHostnames,
  validateAliasRefreshSeconds,
  validateAliasUrl,
} from './dynamicAlias';
import { addressAliasesForFamily } from './natEdit';
import type { AddressAlias } from './types';

describe('dynamicAlias', () => {
  it('validates alias URLs and flags insecure http:// URLs', () => {
    expect(isInsecureHttpUrl('http://lists.example/ips.txt')).toBe(true);
    expect(isInsecureHttpUrl('HTTP://lists.example/ips.txt')).toBe(true);
    expect(isInsecureHttpUrl('https://www.cloudflare.com/ips-v4')).toBe(false);

    expect(validateAliasUrl('https://www.cloudflare.com/ips-v4')).toBeUndefined();
    expect(validateAliasUrl('http://lists.example:8080/ips?v=4')).toBeUndefined();
    expect(validateAliasUrl('https://[2001:db8::1]:8443/list')).toBeUndefined();
    expect(validateAliasUrl('')).toBe('Enter a URL.');
    expect(validateAliasUrl('ftp://lists.example/ips')).toMatch(/https:\/\/ or http:\/\//);
    expect(validateAliasUrl('https://user:pass@lists.example/ips')).toMatch(/no credentials/);
    expect(validateAliasUrl('https://lists.example:0/ips')).toMatch(/valid port/);
    expect(validateAliasUrl('https://lists.example:65536/ips')).toMatch(/valid port/);
    expect(validateAliasUrl('https://lists.example:/ips')).toMatch(/valid port/);
    expect(validateAliasUrl('https://lists.example/a b')).toMatch(/no spaces/);
    expect(validateAliasUrl('https://lists.example/\u202eips')).toMatch(/invisible characters/);
    expect(validateAliasUrl('https://lists.example/%zz')).toMatch(/https:\/\/ or http:\/\//);
    expect(validateAliasUrl(`https://lists.example/${'a'.repeat(2040)}`)).toMatch(/2048 characters/);
  });

  it('validates host names, rejecting IPs, numeric resolver names, trailing dots, and duplicates', () => {
    expect(validateAliasHostname('vpn.example.com')).toBeUndefined();
    expect(validateAliasHostname('app.cafe')).toBeUndefined();
    expect(validateAliasHostname('edge-1.internal')).toBeUndefined();
    expect(validateAliasHostname('192.0.2.1')).toMatch(/host name/i);
    expect(validateAliasHostname('2001:db8::1')).toMatch(/host name/i);
    expect(validateAliasHostname('127.1')).toMatch(/host name/i);
    expect(validateAliasHostname('0x7f.1')).toMatch(/host name/i);
    expect(validateAliasHostname('0x7f000001')).toMatch(/host name/i);
    expect(validateAliasHostname('vpn.example.com.')).toMatch(/host name/i);
    expect(validateAliasHostname('bad host')).toMatch(/host name/i);

    expect(validateAliasHostnames([])).toMatch(/at least one/i);
    expect(validateAliasHostnames(['vpn.example.com', 'VPN.EXAMPLE.COM'])).toMatch(/listed once/i);
    expect(validateAliasHostnames(Array.from({ length: 21 }, (_, i) => `h${i}.example.com`))).toMatch(/At most 20/);
    expect(validateAliasHostnames(['vpn.example.com', 'app.cafe'])).toBeUndefined();
  });

  it('validates and formats refresh intervals from 60 to 86400 seconds', () => {
    expect(validateAliasRefreshSeconds('60')).toBeUndefined();
    expect(validateAliasRefreshSeconds('3600')).toBeUndefined();
    expect(validateAliasRefreshSeconds('86400')).toBeUndefined();
    expect(validateAliasRefreshSeconds('59')).toMatch(/60 to 86400/);
    expect(validateAliasRefreshSeconds('86401')).toMatch(/60 to 86400/);
    expect(validateAliasRefreshSeconds('300.5')).toMatch(/60 to 86400/);

    expect(formatRefreshInterval(60)).toBe('1 minute');
    expect(formatRefreshInterval(90)).toBe('1 minute 30 seconds');
    expect(formatRefreshInterval(300)).toBe('5 minutes');
    expect(formatRefreshInterval(3600)).toBe('1 hour');
    expect(formatRefreshInterval(86400)).toBe('1 day');
  });

  it('includes dynamic aliases in both families by default and omits them when excludeDynamic is set', () => {
    const aliases: AddressAlias[] = [
      { id: 'adr_v4', name: 'lan-v4', kind: 'network', prefix: '192.0.2.0/24' },
      { id: 'adr_v6', name: 'lan-v6', kind: 'network', prefix: '2001:db8::/64' },
      { id: 'adr_url', name: 'cf-feed', kind: 'url', url: 'https://www.cloudflare.com/ips-v4', refreshSeconds: 3600 },
      { id: 'adr_dns', name: 'vpn-dns', kind: 'hostnames', names: ['vpn.example.com'], refreshSeconds: 300 },
    ];
    expect(addressAliasesForFamily(aliases, 'ipv4').map((o) => o.value)).toEqual(['adr_v4', 'adr_url', 'adr_dns']);
    expect(addressAliasesForFamily(aliases, 'ipv6').map((o) => o.value)).toEqual(['adr_v6', 'adr_url', 'adr_dns']);
    expect(addressAliasesForFamily(aliases, 'ipv4', true).map((o) => o.value)).toEqual(['adr_v4']);
    expect(addressAliasesForFamily(aliases, 'ipv6', true).map((o) => o.value)).toEqual(['adr_v6']);
  });
});
