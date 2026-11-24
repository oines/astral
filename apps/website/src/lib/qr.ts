// Minimal ISO/IEC 18004 QR Code encoder (Byte mode, Error Correction Level L, Versions 1–4).
// Generates an exact SVG <path> string at build time with zero external dependencies.

const VERSIONS = [
  { version: 1, size: 21, dataCodewords: 19, ecCodewords: 7, align: [] as number[] },
  { version: 2, size: 25, dataCodewords: 34, ecCodewords: 10, align: [6, 18] },
  { version: 3, size: 29, dataCodewords: 55, ecCodewords: 15, align: [6, 22] },
  { version: 4, size: 33, dataCodewords: 80, ecCodewords: 20, align: [6, 26] },
]

const EXP = new Uint8Array(512)
const LOG = new Uint8Array(256)
{
  let x = 1
  for (let i = 0; i < 255; i++) {
    EXP[i] = x
    LOG[x] = i
    x <<= 1
    if (x & 0x100) x ^= 0x11d
  }
  for (let i = 255; i < 512; i++) EXP[i] = EXP[i - 255]
}

function gfMul(a: number, b: number): number {
  return a === 0 || b === 0 ? 0 : EXP[LOG[a] + LOG[b]]
}

function rsGenerator(degree: number): Uint8Array {
  let poly = new Uint8Array([1])
  for (let i = 0; i < degree; i++) {
    const next = new Uint8Array(poly.length + 1)
    const factor = EXP[i]
    for (let j = 0; j < poly.length; j++) {
      next[j] ^= poly[j]
      next[j + 1] ^= gfMul(poly[j], factor)
    }
    poly = next
  }
  return poly
}

function rsEncode(data: Uint8Array, ecCount: number): Uint8Array {
  const gen = rsGenerator(ecCount)
  const rem = new Uint8Array(ecCount)
  for (const byte of data) {
    const factor = byte ^ rem[0]
    rem.copyWithin(0, 1)
    rem[ecCount - 1] = 0
    for (let i = 0; i < ecCount; i++) rem[i] ^= gfMul(gen[i + 1], factor)
  }
  return rem
}

function bchFormat(mask: number): number {
  const data = (0b01 << 3) | mask // ECC L = 01
  let rem = data << 10
  for (let i = 14; i >= 10; i--) {
    if ((rem >> i) & 1) rem ^= 0x537 << (i - 10)
  }
  return ((data << 10) | rem) ^ 0x5412
}

export function qrSvgData(text: string): { size: number; path: string } {
  const bytes = new TextEncoder().encode(text)
  const spec = VERSIONS.find((v) => bytes.length + 2 <= v.dataCodewords)
  if (!spec) throw new Error(`QR payload too long: ${text}`)

  const bits: number[] = []
  const push = (val: number, len: number) => {
    for (let i = len - 1; i >= 0; i--) bits.push((val >> i) & 1)
  }
  push(0b0100, 4) // Byte mode
  push(bytes.length, 8)
  for (const b of bytes) push(b, 8)
  const capBits = spec.dataCodewords * 8
  push(0, Math.min(4, capBits - bits.length))
  while (bits.length % 8 !== 0) bits.push(0)
  const padBytes = [0xec, 0x11]
  let pi = 0
  while (bits.length < capBits) {
    push(padBytes[pi++ & 1], 8)
  }

  const data = new Uint8Array(spec.dataCodewords)
  for (let i = 0; i < data.length; i++) {
    let v = 0
    for (let b = 0; b < 8; b++) v = (v << 1) | bits[i * 8 + b]
    data[i] = v
  }
  const ec = rsEncode(data, spec.ecCodewords)
  const codewords = new Uint8Array(data.length + ec.length)
  codewords.set(data, 0)
  codewords.set(ec, data.length)

  const n = spec.size
  const grid = Array.from({ length: n }, () => new Int8Array(n)) // 0=empty, 1=dark, -1=light
  const fixed = Array.from({ length: n }, () => new Uint8Array(n))

  const setFixed = (r: number, c: number, dark: boolean) => {
    if (r < 0 || r >= n || c < 0 || c >= n) return
    grid[r][c] = dark ? 1 : -1
    fixed[r][c] = 1
  }

  const drawFinder = (r0: number, c0: number) => {
    for (let dr = -1; dr <= 7; dr++) {
      for (let dc = -1; dc <= 7; dc++) {
        const inside = dr >= 0 && dr <= 6 && dc >= 0 && dc <= 6
        const dark = inside && (dr === 0 || dr === 6 || dc === 0 || dc === 6 || (dr >= 2 && dr <= 4 && dc >= 2 && dc <= 4))
        setFixed(r0 + dr, c0 + dc, dark)
      }
    }
  }
  drawFinder(0, 0)
  drawFinder(0, n - 7)
  drawFinder(n - 7, 0)

  for (let i = 8; i < n - 8; i++) {
    setFixed(6, i, i % 2 === 0)
    setFixed(i, 6, i % 2 === 0)
  }

  for (const ar of spec.align) {
    for (const ac of spec.align) {
      if (fixed[ar][ac]) continue
      for (let dr = -2; dr <= 2; dr++) {
        for (let dc = -2; dc <= 2; dc++) {
          const dark = Math.max(Math.abs(dr), Math.abs(dc)) !== 1
          setFixed(ar + dr, ac + dc, dark)
        }
      }
    }
  }

  // Reserve format info cells & dark module
  for (let i = 0; i <= 8; i++) {
    if (!fixed[8][i]) fixed[8][i] = 1
    if (!fixed[i][8]) fixed[i][8] = 1
  }
  for (let i = 0; i < 8; i++) {
    fixed[8][n - 1 - i] = 1
    fixed[n - 1 - i][8] = 1
  }
  setFixed(n - 8, 8, true)

  // Place data bits in upward/downward zigzag columns
  let bitIdx = 0
  const totalBits = codewords.length * 8
  for (let right = n - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5
    const upward = ((n - 1 - right) >> 1) % 2 === 0
    for (let step = 0; step < n; step++) {
      const r = upward ? n - 1 - step : step
      for (let dx = 0; dx < 2; dx++) {
        const c = right - dx
        if (fixed[r][c]) continue
        const bit = bitIdx < totalBits ? (codewords[bitIdx >> 3] >> (7 - (bitIdx & 7))) & 1 : 0
        bitIdx++
        // Mask 0: (r + c) % 2 === 0
        const masked = ((r + c) & 1) === 0 ? bit ^ 1 : bit
        grid[r][c] = masked ? 1 : -1
      }
    }
  }

  // Write format info for ECC L + Mask 0
  const fmt = bchFormat(0)
  const fbit = (i: number) => ((fmt >> i) & 1) === 1
  for (let i = 0; i <= 5; i++) grid[8][i] = fbit(14 - i) ? 1 : -1
  grid[8][7] = fbit(8) ? 1 : -1
  grid[8][8] = fbit(7) ? 1 : -1
  grid[7][8] = fbit(6) ? 1 : -1
  for (let i = 0; i <= 5; i++) grid[5 - i][8] = fbit(5 - i) ? 1 : -1
  for (let i = 0; i <= 7; i++) grid[8][n - 1 - i] = fbit(i) ? 1 : -1
  for (let i = 0; i <= 6; i++) grid[n - 1 - i][8] = fbit(14 - i) ? 1 : -1

  let path = ''
  for (let r = 0; r < n; r++) {
    for (let c = 0; c < n; c++) {
      if (grid[r][c] === 1) path += `M${c} ${r}h1v1h-1z`
    }
  }
  return { size: n, path }
}
