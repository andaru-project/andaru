// Pure ISO/IEC 18004 QR Code encoder (Byte mode, Error Correction Level L, versions 1–20)
// with zero external dependencies and strict-CSP-safe SVG path rendering.

export interface QrMatrix {
  version: number;
  size: number;
  modules: boolean[][];
}

// Level-L block parameters for versions 1..20: [ecPerBlock, group1Blocks, group1Data, group2Blocks, group2Data].
const LEVEL_L_SPECS: ReadonlyArray<readonly [number, number, number, number, number]> = [
  [7, 1, 19, 0, 0], // v1  (total 26, data 19)
  [10, 1, 34, 0, 0], // v2  (total 44, data 34)
  [15, 1, 55, 0, 0], // v3  (total 70, data 55)
  [20, 1, 80, 0, 0], // v4  (total 100, data 80)
  [26, 1, 108, 0, 0], // v5  (total 134, data 108)
  [18, 2, 68, 0, 0], // v6  (total 172, data 136)
  [20, 2, 78, 0, 0], // v7  (total 196, data 156)
  [24, 2, 97, 0, 0], // v8  (total 242, data 194)
  [30, 2, 116, 0, 0], // v9  (total 292, data 232)
  [18, 2, 68, 2, 69], // v10 (total 346, data 274)
  [20, 4, 81, 0, 0], // v11 (total 404, data 324)
  [24, 2, 92, 2, 93], // v12 (total 466, data 370)
  [26, 4, 107, 0, 0], // v13 (total 532, data 428)
  [30, 3, 115, 1, 116], // v14 (total 581, data 461)
  [22, 5, 87, 1, 88], // v15 (total 655, data 523)
  [24, 5, 98, 1, 99], // v16 (total 733, data 589)
  [28, 1, 107, 5, 108], // v17 (total 815, data 647)
  [30, 5, 120, 1, 121], // v18 (total 901, data 721)
  [28, 3, 113, 4, 114], // v19 (total 991, data 795)
  [28, 3, 107, 5, 108], // v20 (total 1085, data 861)
];

const ALIGNMENT_CENTERS: ReadonlyArray<readonly number[]> = [
  [],
  [6, 18],
  [6, 22],
  [6, 26],
  [6, 30],
  [6, 34],
  [6, 22, 38],
  [6, 24, 42],
  [6, 26, 46],
  [6, 28, 50],
  [6, 30, 54],
  [6, 32, 58],
  [6, 34, 62],
  [6, 26, 46, 66],
  [6, 26, 48, 70],
  [6, 26, 50, 74],
  [6, 30, 54, 78],
  [6, 30, 56, 82],
  [6, 30, 58, 86],
  [6, 34, 62, 90],
];

// GF(256) multiplication modulo x^8 + x^4 + x^3 + x^2 + 1 (0x11d).
function gfMul(x: number, y: number): number {
  let z = 0;
  for (let i = 7; i >= 0; i--) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d);
    z ^= ((y >>> i) & 1) * x;
  }
  return z;
}

function rsGenerator(degree: number): Uint8Array {
  const poly = new Uint8Array(degree);
  poly[degree - 1] = 1;
  let root = 1;
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < degree; j++) {
      poly[j] = gfMul(poly[j], root);
      if (j + 1 < degree) poly[j] ^= poly[j + 1];
    }
    root = gfMul(root, 0x02);
  }
  return poly;
}

function rsRemainder(data: Uint8Array, gen: Uint8Array): Uint8Array {
  const out = new Uint8Array(gen.length);
  for (const b of data) {
    const factor = b ^ out[0];
    out.copyWithin(0, 1);
    out[out.length - 1] = 0;
    for (let i = 0; i < gen.length; i++) {
      out[i] ^= gfMul(gen[i], factor);
    }
  }
  return out;
}

function appendBits(target: number[], value: number, count: number): void {
  for (let i = count - 1; i >= 0; i--) {
    target.push((value >>> i) & 1);
  }
}

function formatBits(mask: number): number {
  // Error correction level L has 2-bit indicator 0b01.
  const data = (0b01 << 3) | mask;
  let rem = data;
  for (let i = 0; i < 10; i++) {
    rem = (rem << 1) ^ ((rem >>> 9) * 0x537);
  }
  return ((data << 10) | rem) ^ 0x5412;
}

function versionBits(version: number): number {
  let rem = version;
  for (let i = 0; i < 12; i++) {
    rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25);
  }
  return (version << 12) | rem;
}

function maskBit(mask: number, row: number, col: number): boolean {
  switch (mask) {
    case 0: return (row + col) % 2 === 0;
    case 1: return row % 2 === 0;
    case 2: return col % 3 === 0;
    case 3: return (row + col) % 3 === 0;
    case 4: return (Math.floor(row / 2) + Math.floor(col / 3)) % 2 === 0;
    case 5: return ((row * col) % 2) + ((row * col) % 3) === 0;
    case 6: return (((row * col) % 2) + ((row * col) % 3)) % 2 === 0;
    case 7: return (((row + col) % 2) + ((row * col) % 3)) % 2 === 0;
    default: return false;
  }
}

function penaltyScore(modules: boolean[][]): number {
  const size = modules.length;
  let penalty = 0;

  // Rule 1: adjacent modules in row/column in same color.
  const runPenalty = (seq: boolean[]): number => {
    let score = 0;
    let runColor = seq[0];
    let runLen = 1;
    for (let i = 1; i < seq.length; i++) {
      if (seq[i] === runColor) {
        runLen++;
      } else {
        if (runLen >= 5) score += runLen - 2;
        runColor = seq[i];
        runLen = 1;
      }
    }
    if (runLen >= 5) score += runLen - 2;
    return score;
  };
  for (let r = 0; r < size; r++) penalty += runPenalty(modules[r]);
  for (let c = 0; c < size; c++) {
    const col: boolean[] = [];
    for (let r = 0; r < size; r++) col.push(modules[r][c]);
    penalty += runPenalty(col);
  }

  // Rule 2: 2x2 blocks of same color.
  for (let r = 0; r < size - 1; r++) {
    for (let c = 0; c < size - 1; c++) {
      const color = modules[r][c];
      if (color === modules[r][c + 1] && color === modules[r + 1][c] && color === modules[r + 1][c + 1]) {
        penalty += 3;
      }
    }
  }

  // Rule 3: finder-like 1:1:3:1:1 patterns with 4 light modules on either side.
  const isFinderLike = (seq: boolean[]): number => {
    let count = 0;
    for (let i = 0; i <= seq.length - 11; i++) {
      const patA =
        seq[i] && !seq[i + 1] && seq[i + 2] && seq[i + 3] && seq[i + 4] && !seq[i + 5] && seq[i + 6]
        && !seq[i + 7] && !seq[i + 8] && !seq[i + 9] && !seq[i + 10];
      const patB =
        !seq[i] && !seq[i + 1] && !seq[i + 2] && !seq[i + 3]
        && seq[i + 4] && !seq[i + 5] && seq[i + 6] && seq[i + 7] && seq[i + 8] && !seq[i + 9] && seq[i + 10];
      if (patA || patB) count += 40;
    }
    return count;
  };
  for (let r = 0; r < size; r++) penalty += isFinderLike(modules[r]);
  for (let c = 0; c < size; c++) {
    const col: boolean[] = [];
    for (let r = 0; r < size; r++) col.push(modules[r][c]);
    penalty += isFinderLike(col);
  }

  // Rule 4: balance of dark and light modules.
  let dark = 0;
  for (let r = 0; r < size; r++) {
    for (let c = 0; c < size; c++) {
      if (modules[r][c]) dark++;
    }
  }
  const total = size * size;
  const k = Math.floor(Math.abs(dark * 20 - total * 10) / total);
  penalty += k * 10;

  return penalty;
}

function drawFormat(modules: boolean[][], isFunc: boolean[][], mask: number): void {
  const size = modules.length;
  const bits = formatBits(mask);
  const set = (r: number, c: number, dark: boolean) => {
    modules[r][c] = dark;
    isFunc[r][c] = true;
  };

  for (let i = 0; i <= 5; i++) set(i, 8, ((bits >>> i) & 1) !== 0);
  set(7, 8, ((bits >>> 6) & 1) !== 0);
  set(8, 8, ((bits >>> 7) & 1) !== 0);
  set(8, 7, ((bits >>> 8) & 1) !== 0);
  for (let i = 9; i < 15; i++) set(8, 14 - i, ((bits >>> i) & 1) !== 0);

  for (let i = 0; i < 8; i++) set(8, size - 1 - i, ((bits >>> i) & 1) !== 0);
  for (let i = 8; i < 15; i++) set(size - 15 + i, 8, ((bits >>> i) & 1) !== 0);
  set(size - 8, 8, true); // Dark module.
}

function drawFunctionPatterns(version: number, modules: boolean[][], isFunc: boolean[][]): void {
  const size = modules.length;
  const set = (r: number, c: number, dark: boolean) => {
    modules[r][c] = dark;
    isFunc[r][c] = true;
  };

  // Timing patterns.
  for (let i = 0; i < size; i++) {
    set(6, i, i % 2 === 0);
    set(i, 6, i % 2 === 0);
  }

  // Three finder patterns + 1-module white separator.
  const drawFinder = (top: number, left: number) => {
    for (let dy = -1; dy <= 7; dy++) {
      for (let dx = -1; dx <= 7; dx++) {
        const r = top + dy;
        const c = left + dx;
        if (r < 0 || r >= size || c < 0 || c >= size) continue;
        const dist = Math.max(Math.abs(dx - 3), Math.abs(dy - 3));
        set(r, c, dist !== 2 && dist !== 4);
      }
    }
  };
  drawFinder(0, 0);
  drawFinder(0, size - 7);
  drawFinder(size - 7, 0);

  // Alignment patterns (version >= 2).
  const centers = ALIGNMENT_CENTERS[version - 1];
  for (let i = 0; i < centers.length; i++) {
    for (let j = 0; j < centers.length; j++) {
      if ((i === 0 && j === 0) || (i === 0 && j === centers.length - 1) || (i === centers.length - 1 && j === 0)) {
        continue;
      }
      const cr = centers[i];
      const cc = centers[j];
      for (let dy = -2; dy <= 2; dy++) {
        for (let dx = -2; dx <= 2; dx++) {
          set(cr + dy, cc + dx, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
        }
      }
    }
  }

  // Reserve format info cells before data placement.
  drawFormat(modules, isFunc, 0);

  // Version info blocks (version >= 7).
  if (version >= 7) {
    const bits = versionBits(version);
    for (let i = 0; i < 18; i++) {
      const dark = ((bits >>> i) & 1) !== 0;
      const a = size - 11 + (i % 3);
      const b = Math.floor(i / 3);
      set(a, b, dark);
      set(b, a, dark);
    }
  }
}

export function encodeQr(text: string): QrMatrix {
  const bytes = new TextEncoder().encode(text);

  let version = 0;
  for (let v = 1; v <= LEVEL_L_SPECS.length; v++) {
    const [, g1Count, g1Data, g2Count, g2Data] = LEVEL_L_SPECS[v - 1];
    const capacityBytes = g1Count * g1Data + g2Count * g2Data;
    const countBits = v <= 9 ? 8 : 16;
    const neededBits = 4 + countBits + bytes.length * 8;
    if (neededBits <= capacityBytes * 8) {
      version = v;
      break;
    }
  }
  if (version === 0) {
    throw new Error('Content is too long for QR code.');
  }

  const [ecLen, g1Count, g1Data, g2Count, g2Data] = LEVEL_L_SPECS[version - 1];
  const dataCapacityBytes = g1Count * g1Data + g2Count * g2Data;
  const dataCapacityBits = dataCapacityBytes * 8;
  const countBits = version <= 9 ? 8 : 16;

  // Build bit stream: Byte mode (0100), length, payload, terminator, byte padding, pad codewords.
  const bits: number[] = [];
  appendBits(bits, 0b0100, 4);
  appendBits(bits, bytes.length, countBits);
  for (const b of bytes) appendBits(bits, b, 8);
  appendBits(bits, 0, Math.min(4, dataCapacityBits - bits.length));
  while (bits.length % 8 !== 0) bits.push(0);

  const dataBytes = new Uint8Array(dataCapacityBytes);
  for (let i = 0; i < bits.length / 8; i++) {
    let val = 0;
    for (let b = 0; b < 8; b++) val = (val << 1) | bits[i * 8 + b];
    dataBytes[i] = val;
  }
  for (let i = bits.length / 8, pad = 0; i < dataCapacityBytes; i++, pad++) {
    dataBytes[i] = pad % 2 === 0 ? 0xec : 0x11;
  }

  // Split into RS blocks and interleave data + error-correction codewords.
  const gen = rsGenerator(ecLen);
  const dataBlocks: Uint8Array[] = [];
  const ecBlocks: Uint8Array[] = [];
  let offset = 0;
  for (const [blockCount, blockLen] of [[g1Count, g1Data], [g2Count, g2Data]] as const) {
    for (let i = 0; i < blockCount; i++) {
      const block = dataBytes.subarray(offset, offset + blockLen);
      offset += blockLen;
      dataBlocks.push(block);
      ecBlocks.push(rsRemainder(block, gen));
    }
  }

  const maxDataLen = Math.max(g1Data, g2Data);
  const codewords: number[] = [];
  for (let i = 0; i < maxDataLen; i++) {
    for (const block of dataBlocks) {
      if (i < block.length) codewords.push(block[i]);
    }
  }
  for (let i = 0; i < ecLen; i++) {
    for (const ec of ecBlocks) {
      codewords.push(ec[i]);
    }
  }

  const size = version * 4 + 17;
  const baseModules = Array.from({ length: size }, () => Array<boolean>(size).fill(false));
  const isFunc = Array.from({ length: size }, () => Array<boolean>(size).fill(false));
  drawFunctionPatterns(version, baseModules, isFunc);

  // Place interleaved codeword bits in upward/downward two-column zigzag.
  let bitIndex = 0;
  const totalBits = codewords.length * 8;
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5;
    const upward = ((right + 1) & 2) === 0;
    for (let step = 0; step < size; step++) {
      const row = upward ? size - 1 - step : step;
      for (let j = 0; j < 2; j++) {
        const col = right - j;
        if (isFunc[row][col]) continue;
        let dark = false;
        if (bitIndex < totalBits) {
          dark = ((codewords[bitIndex >>> 3] >>> (7 - (bitIndex & 7))) & 1) !== 0;
          bitIndex++;
        }
        baseModules[row][col] = dark;
      }
    }
  }

  // Evaluate all 8 masks and keep the one with the lowest ISO penalty score.
  let bestModules = baseModules;
  let bestPenalty = Number.POSITIVE_INFINITY;
  for (let mask = 0; mask < 8; mask++) {
    const candidate = baseModules.map((row) => [...row]);
    for (let r = 0; r < size; r++) {
      for (let c = 0; c < size; c++) {
        if (!isFunc[r][c] && maskBit(mask, r, c)) {
          candidate[r][c] = !candidate[r][c];
        }
      }
    }
    drawFormat(candidate, isFunc, mask);
    const score = penaltyScore(candidate);
    if (score < bestPenalty) {
      bestPenalty = score;
      bestModules = candidate;
    }
  }

  return { version, size, modules: bestModules };
}

export function qrSvgPath(matrix: QrMatrix, quietZone = 4): { viewBoxSize: number; path: string } {
  const parts: string[] = [];
  for (let r = 0; r < matrix.size; r++) {
    for (let c = 0; c < matrix.size; c++) {
      if (matrix.modules[r][c]) {
        parts.push(`M${c + quietZone},${r + quietZone}h1v1h-1z`);
      }
    }
  }
  return {
    viewBoxSize: matrix.size + quietZone * 2,
    path: parts.join(''),
  };
}
