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

  it('round-trips the ISO 18004 format bits, mask, and Byte-mode UTF-8 payload for version 1', () => {
    const payload = 'Hello, WireGuard!';
    const qr = encodeQr(payload);
    expect(qr.version).toBe(1);
    const { size, modules } = qr;

    // Read 15-bit format info from both redundant copies (ISO 18004 Figure 25) and verify they match.
    let fmtA = 0;
    for (let i = 0; i <= 5; i++) fmtA |= (modules[i][8] ? 1 : 0) << i;
    fmtA |= (modules[7][8] ? 1 : 0) << 6;
    fmtA |= (modules[8][8] ? 1 : 0) << 7;
    fmtA |= (modules[8][7] ? 1 : 0) << 8;
    for (let i = 9; i < 15; i++) fmtA |= (modules[8][14 - i] ? 1 : 0) << i;

    let fmtB = 0;
    for (let i = 0; i < 8; i++) fmtB |= (modules[8][size - 1 - i] ? 1 : 0) << i;
    for (let i = 8; i < 15; i++) fmtB |= (modules[size - 15 + i][8] ? 1 : 0) << i;
    expect(fmtA).toBe(fmtB);

    const unmaskedFmt = fmtA ^ 0x5412;
    const ecLevel = (unmaskedFmt >>> 13) & 0b11;
    const mask = (unmaskedFmt >>> 10) & 0b111;
    expect(ecLevel).toBe(0b01); // Level L

    // Unmask data modules in standard zigzag order (version 1 has no alignment patterns or version info).
    const isFunc = (r: number, c: number) =>
      (r <= 8 && c <= 8)
      || (r <= 8 && c >= size - 8)
      || (r >= size - 8 && c <= 8)
      || r === 6
      || c === 6;
    const applyMask = (r: number, c: number) => {
      switch (mask) {
        case 0: return (r + c) % 2 === 0;
        case 1: return r % 2 === 0;
        case 2: return c % 3 === 0;
        case 3: return (r + c) % 3 === 0;
        case 4: return (Math.floor(r / 2) + Math.floor(c / 3)) % 2 === 0;
        case 5: return ((r * c) % 2) + ((r * c) % 3) === 0;
        case 6: return (((r * c) % 2) + ((r * c) % 3)) % 2 === 0;
        case 7: return (((r + c) % 2) + ((r * c) % 3)) % 2 === 0;
        default: return false;
      }
    };

    const bits: number[] = [];
    for (let right = size - 1; right >= 1; right -= 2) {
      if (right === 6) right = 5;
      const upward = ((right + 1) & 2) === 0;
      for (let step = 0; step < size; step++) {
        const row = upward ? size - 1 - step : step;
        for (let j = 0; j < 2; j++) {
          const col = right - j;
          if (isFunc(row, col)) continue;
          bits.push((modules[row][col] !== applyMask(row, col)) ? 1 : 0);
        }
      }
    }

    const readBits = (start: number, count: number) => {
      let v = 0;
      for (let i = 0; i < count; i++) v = (v << 1) | bits[start + i];
      return v;
    };
    expect(readBits(0, 4)).toBe(0b0100); // Byte mode
    const len = readBits(4, 8);
    expect(len).toBe(payload.length);
    const decoded = new Uint8Array(len);
    for (let i = 0; i < len; i++) decoded[i] = readBits(12 + i * 8, 8);
    expect(new TextDecoder().decode(decoded)).toBe(payload);
  });
});
