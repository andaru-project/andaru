import { describe, expect, it } from 'vitest';
import { encodeQr, qrSvgPath } from './qr';

describe('encodeQr', () => {
  it('encodes short text as version 1 with valid finder, timing, and dark-module invariants', () => {
    const qr = encodeQr('Hello, WireGuard!');
    expect(qr.version).toBe(1);
    expect(qr.size).toBe(21);
    expect(qr.modules).toHaveLength(21);

    // Top-left 7x7 finder outer ring and 3x3 center must be dark, inner ring light.
    expect(qr.modules[0][0]).toBe(true);
    expect(qr.modules[1][1]).toBe(false);
    expect(qr.modules[3][3]).toBe(true);
    expect(qr.modules[7][0]).toBe(false); // Separator.

    // Top-right and bottom-left finders.
    expect(qr.modules[0][20]).toBe(true);
    expect(qr.modules[3][17]).toBe(true);
    expect(qr.modules[20][0]).toBe(true);
    expect(qr.modules[17][3]).toBe(true);

    // Timing patterns on row 6 and col 6 between finders.
    for (let i = 8; i <= 12; i++) {
      expect(qr.modules[6][i]).toBe(i % 2 === 0);
      expect(qr.modules[i][6]).toBe(i % 2 === 0);
    }

    // Fixed dark module at (size - 8, 8).
    expect(qr.modules[qr.size - 8][8]).toBe(true);
  });

  it('encodes a realistic WireGuard client configuration and produces a non-empty SVG path', () => {
    const conf = [
      '[Interface]',
      'PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',
      'Address = 10.200.0.2/32, fd00:200::2/128',
      'DNS = 10.200.0.1',
      '',
      '[Peer]',
      'PublicKey = BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=',
      'PresharedKey = CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=',
      'AllowedIPs = 0.0.0.0/0, ::/0',
      'Endpoint = vpn.example.com:51820',
      'PersistentKeepalive = 25',
      '',
    ].join('\n');

    const qr = encodeQr(conf);
    expect(qr.version).toBeGreaterThanOrEqual(10);
    expect(qr.version).toBeLessThanOrEqual(14);
    expect(qr.size).toBe(qr.version * 4 + 17);

    // Alignment pattern center at (size - 7, size - 7) must be dark.
    expect(qr.modules[qr.size - 7][qr.size - 7]).toBe(true);

    const svg = qrSvgPath(qr, 4);
    expect(svg.viewBoxSize).toBe(qr.size + 8);
    expect(svg.path).toContain('M4,4h1v1h-1z');
  });

  it('throws when content exceeds version 20 Level-L byte capacity', () => {
    expect(() => encodeQr('x'.repeat(900))).toThrow('too long');
  });

  it('produces valid dimensions across versions 1 through 20 including version-info versions', () => {
    for (const len of [5, 25, 60, 120, 200, 350, 600, 850]) {
      const qr = encodeQr('A'.repeat(len));
      expect(qr.size).toBe(qr.version * 4 + 17);
      expect(qr.modules).toHaveLength(qr.size);
      expect(qr.modules[qr.size - 8][8]).toBe(true);
    }
  });
});
