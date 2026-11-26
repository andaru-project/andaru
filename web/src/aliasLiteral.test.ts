import { describe, expect, it } from 'vitest';
import { parseAddressLiteral, parseAliasLiterals, parsePortLiteral } from './aliasLiteral';

describe('parseAddressLiteral', () => {
  it('parses canonical IPv4 and IPv6 host addresses and honors family/kind filters', () => {
    expect(parseAddressLiteral('192.168.1.50')).toEqual([{
      key: 'host:192.168.1.50',
      label: 'Use 192.168.1.50',
      hint: 'Creates host address alias "192.168.1.50"',
      payload: { name: '192.168.1.50', kind: 'host', address: '192.168.1.50' },
    }]);

    expect(parseAddressLiteral('2001:0db8:0000:0000::50')).toEqual([{
      key: 'host:2001:db8::50',
      label: 'Use 2001:db8::50',
      hint: 'Creates host address alias "2001:db8::50"',
      payload: { name: '2001:db8::50', kind: 'host', address: '2001:db8::50' },
    }]);

    expect(parseAddressLiteral('192.168.1.50', { family: 'ipv6' })).toEqual([]);
    expect(parseAddressLiteral('2001:db8::50', { family: 'ipv4' })).toEqual([]);
    expect(parseAddressLiteral('192.168.1.50', { kind: 'network' })).toEqual([]);
  });

  it('rejects malformed addresses, leading zeros, zone IDs, and IPv4-mapped IPv6', () => {
    expect(parseAddressLiteral('192.168.01.1')).toEqual([]);
    expect(parseAddressLiteral('192.168.1.256')).toEqual([]);
    expect(parseAddressLiteral('fe80::1%eth0')).toEqual([]);
    expect(parseAddressLiteral('::ffff:192.0.2.1')).toEqual([]);
    expect(parseAddressLiteral('::ffff:c000:201')).toEqual([]);
    expect(parseAddressLiteral('web-servers')).toEqual([]);
  });

  it('parses masked CIDR networks and rejects non-zero host bits', () => {
    expect(parseAddressLiteral('192.168.10.0/24')).toEqual([{
      key: 'network:192.168.10.0/24',
      label: 'Use 192.168.10.0/24',
      hint: 'Creates network alias "192.168.10.0/24"',
      payload: { name: '192.168.10.0/24', kind: 'network', prefix: '192.168.10.0/24' },
    }]);
    expect(parseAddressLiteral('2001:db8:1::/48')).toEqual([{
      key: 'network:2001:db8:1::/48',
      label: 'Use 2001:db8:1::/48',
      hint: 'Creates network alias "2001:db8:1::/48"',
      payload: { name: '2001:db8:1::/48', kind: 'network', prefix: '2001:db8:1::/48' },
    }]);
    // Host bits set -> rejected (mirrors policy.go:90)
    expect(parseAddressLiteral('192.168.10.5/24')).toEqual([]);
    expect(parseAddressLiteral('2001:db8::1/64')).toEqual([]);
    expect(parseAddressLiteral('192.168.10.0/33')).toEqual([]);
  });

  it('parses same-family ordered address ranges and rejects backwards or mixed-family ranges', () => {
    expect(parseAddressLiteral('10.20.0.10 - 10.20.0.20')).toEqual([{
      key: 'range:10.20.0.10-10.20.0.20',
      label: 'Use 10.20.0.10-10.20.0.20',
      hint: 'Creates address range alias "10.20.0.10-10.20.0.20"',
      payload: { name: '10.20.0.10-10.20.0.20', kind: 'range', first: '10.20.0.10', last: '10.20.0.20' },
    }]);
    expect(parseAddressLiteral('10.20.0.20-10.20.0.10')).toEqual([]);
    expect(parseAddressLiteral('10.20.0.10-2001:db8::1')).toEqual([]);
  });
});

describe('parsePortLiteral', () => {
  it('offers TCP and UDP for bare ports/ranges and single protocol when specified', () => {
    const bare = parsePortLiteral('443');
    expect(bare).toHaveLength(2);
    expect(bare[0].payload).toEqual({ name: 'tcp/443', protocol: 'tcp', ports: ['443'] });
    expect(bare[1].payload).toEqual({ name: 'udp/443', protocol: 'udp', ports: ['443'] });

    expect(parsePortLiteral('TCP/80, 443, 8000 - 8080')).toEqual([{
      key: 'tcp:80,443,8000-8080',
      label: 'Use TCP 80,443,8000-8080',
      hint: 'Creates TCP port alias "tcp/80,443,8000-8080"',
      payload: { name: 'tcp/80,443,8000-8080', protocol: 'tcp', ports: ['80', '443', '8000-8080'] },
    }]);

    expect(parsePortLiteral('udp:53')).toEqual([{
      key: 'udp:53',
      label: 'Use UDP 53',
      hint: 'Creates UDP port alias "udp/53"',
      payload: { name: 'udp/53', protocol: 'udp', ports: ['53'] },
    }]);

    expect(parsePortLiteral('443', { kind: 'udp' })).toHaveLength(1);
    expect(parsePortLiteral('443', { kind: 'udp' })[0].payload.protocol).toBe('udp');
  });

  it('parses known ICMP/ICMPv6 literals and rejects invalid ports or unknown ICMP types', () => {
    expect(parseAliasLiterals('ports', 'icmp/echo-request')).toEqual([{
      key: 'icmp:echo-request',
      label: 'Use ICMP echo-request',
      hint: 'Creates ICMP alias "icmp/echo-request"',
      payload: { name: 'icmp/echo-request', protocol: 'icmp', types: ['echo-request'] },
    }]);
    expect(parsePortLiteral('0')).toEqual([]);
    expect(parsePortLiteral('65536')).toEqual([]);
    expect(parsePortLiteral('90-80')).toEqual([]);
    expect(parsePortLiteral('icmp/router-advertisement')).toEqual([]);
    expect(parsePortLiteral('https')).toEqual([]);
  });
});
