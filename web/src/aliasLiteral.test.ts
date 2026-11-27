import { describe, expect, it } from 'vitest';
import {
  formatAddressRef,
  formatPortRef,
  isAddressLiteral,
  isPortLiteral,
  parseAddressLiteral,
  parseAliasLiterals,
  parsePortLiteral,
} from './aliasLiteral';

describe('parseAddressLiteral', () => {
  it('parses canonical IPv4 and IPv6 host addresses and honors family/kind filters', () => {
    expect(parseAddressLiteral('192.168.1.50')).toEqual([{
      value: '192.168.1.50',
      label: '192.168.1.50',
      optionLabel: '192.168.1.50',
      hint: 'IPv4 address',
    }]);

    expect(parseAddressLiteral('2001:0db8:0000:0000::50')).toEqual([{
      value: '2001:db8::50',
      label: '2001:db8::50',
      optionLabel: '2001:db8::50',
      hint: 'IPv6 address',
    }]);

    expect(parseAddressLiteral('192.168.1.50', { family: 'ipv6' })).toEqual([]);
    expect(parseAddressLiteral('2001:db8::50', { family: 'ipv4' })).toEqual([]);
    expect(parseAddressLiteral('192.168.1.50', { kind: 'network' })).toEqual([]);
    expect(parseAddressLiteral('192.168.1.50', { kind: 'group' })).toEqual([]);
  });

  it('rejects malformed addresses, leading zeros, zone IDs, and IPv4-mapped IPv6', () => {
    expect(parseAddressLiteral('192.168.01.1')).toEqual([]);
    expect(parseAddressLiteral('192.168.1.256')).toEqual([]);
    expect(parseAddressLiteral('fe80::1%eth0')).toEqual([]);
    expect(parseAddressLiteral('::ffff:192.0.2.1')).toEqual([]);
    expect(parseAddressLiteral('::ffff:c000:201')).toEqual([]);
    expect(parseAddressLiteral('web-servers')).toEqual([]);
    expect(isAddressLiteral('adr_00000001-0000-4000-8000-000000000001')).toBe(false);
  });

  it('parses masked CIDR networks including /0, /32, /128 and rejects non-zero host bits', () => {
    expect(parseAddressLiteral('192.168.10.0/24')).toEqual([{
      value: '192.168.10.0/24',
      label: '192.168.10.0/24',
      optionLabel: '192.168.10.0/24',
      hint: 'IPv4 network',
    }]);
    expect(parseAddressLiteral('0.0.0.0/0')[0]?.value).toBe('0.0.0.0/0');
    expect(parseAddressLiteral('192.168.10.5/32')[0]?.value).toBe('192.168.10.5/32');
    expect(parseAddressLiteral('::/0')[0]?.value).toBe('::/0');
    expect(parseAddressLiteral('2001:db8::1/128')[0]?.value).toBe('2001:db8::1/128');
    expect(parseAddressLiteral('192.168.10.5/24')).toEqual([]);
    expect(parseAddressLiteral('2001:db8::1/64')).toEqual([]);
    expect(parseAddressLiteral('192.168.10.0/08')).toEqual([]);
    expect(parseAddressLiteral('192.168.10.0/33')).toEqual([]);
  });

  it('parses same-family IPv4 and IPv6 ranges and rejects backwards or mixed-family ranges', () => {
    expect(parseAddressLiteral('10.20.0.10 - 10.20.0.20')).toEqual([{
      value: '10.20.0.10-10.20.0.20',
      label: '10.20.0.10-10.20.0.20',
      optionLabel: '10.20.0.10-10.20.0.20',
      hint: 'IPv4 range',
    }]);
    expect(parseAddressLiteral('2001:db8::10-2001:db8::20')[0]?.hint).toBe('IPv6 range');
    expect(parseAddressLiteral('10.20.0.20-10.20.0.10')).toEqual([]);
    expect(parseAddressLiteral('10.20.0.10-2001:db8::1')).toEqual([]);
  });
});

describe('parsePortLiteral', () => {
  it('offers TCP and UDP for bare ports/ranges, deduplicates and collapses equal bounds', () => {
    const bare = parsePortLiteral('443');
    expect(bare).toEqual([
      { value: 'tcp/443', label: 'tcp/443', optionLabel: 'TCP 443', hint: 'TCP port' },
      { value: 'udp/443', label: 'udp/443', optionLabel: 'UDP 443', hint: 'UDP port' },
    ]);

    expect(parsePortLiteral('TCP/80, 443-443, 80, 8000 - 8080')).toEqual([{
      value: 'tcp/80,443,8000-8080',
      label: 'tcp/80,443,8000-8080',
      optionLabel: 'TCP 80,443,8000-8080',
      hint: 'TCP ports',
    }]);

    expect(parsePortLiteral('udp 53')[0]?.value).toBe('udp/53');
    expect(parsePortLiteral('443', { kind: 'udp' })).toEqual([
      { value: 'udp/443', label: 'udp/443', optionLabel: 'UDP 443', hint: 'UDP port' },
    ]);
  });

  it('parses bare and typed ICMP/ICMPv6 literals and rejects invalid ports or unknown types', () => {
    expect(parseAliasLiterals('ports', 'icmp')).toEqual([{
      value: 'icmp',
      label: 'icmp',
      optionLabel: 'ICMP (any type)',
      hint: 'ICMP protocol',
    }]);
    expect(parseAliasLiterals('ports', 'icmpv6')).toEqual([{
      value: 'icmpv6',
      label: 'icmpv6',
      optionLabel: 'ICMPv6 (any type)',
      hint: 'ICMPv6 protocol',
    }]);
    expect(parseAliasLiterals('ports', 'icmp/echo-request, echo-request')).toEqual([{
      value: 'icmp/echo-request',
      label: 'icmp/echo-request',
      optionLabel: 'ICMP echo-request',
      hint: 'ICMP type',
    }]);
    expect(parseAliasLiterals('ports', 'icmpv6/packet-too-big')[0]?.value).toBe('icmpv6/packet-too-big');
    expect(parsePortLiteral('0')).toEqual([]);
    expect(parsePortLiteral('65536')).toEqual([]);
    expect(parsePortLiteral('90-80')).toEqual([]);
    expect(parsePortLiteral('icmp/router-advertisement')).toEqual([]);
    expect(parsePortLiteral('https')).toEqual([]);
    expect(isPortLiteral('tcp/443')).toBe(true);
    expect(isPortLiteral('icmp')).toBe(true);
    expect(isPortLiteral('443')).toBe(false);
    expect(isPortLiteral('svc_00000001-0000-4000-8000-000000000001')).toBe(false);
  });

  it('formats alias names, inline literals, and missing IDs distinctly', () => {
    const addrs = new Map([['adr_1', 'office']]);
    expect(formatAddressRef('adr_1', addrs)).toBe('office');
    expect(formatAddressRef('10.0.0.1', addrs)).toBe('10.0.0.1');
    expect(formatAddressRef('adr_gone', addrs)).toBe('unknown alias');

    const ports = new Map([['svc_1', 'https']]);
    expect(formatPortRef('svc_1', ports)).toBe('https');
    expect(formatPortRef('tcp/443', ports)).toBe('tcp/443');
    expect(formatPortRef('svc_gone', ports)).toBe('unknown alias');
  });
});
