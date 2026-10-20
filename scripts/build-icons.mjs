import { deflateSync } from 'node:zlib'
import { Buffer } from 'node:buffer'
import { mkdir, writeFile, rm } from 'node:fs/promises'
import { spawnSync } from 'node:child_process'
import path from 'node:path'
import { root } from './build-lib.mjs'

// The app mark is deliberately geometric: four points, warm white on deep green.
function crc32(bytes) {
  let value = 0xffffffff
  for (const byte of bytes) {
    value ^= byte
    for (let bit = 0; bit < 8; bit++) value = (value >>> 1) ^ ((value & 1) ? 0xedb88320 : 0)
  }
  return (value ^ 0xffffffff) >>> 0
}
function chunk(name, data) {
  const label = Buffer.from(name)
  const size = Buffer.alloc(4), crc = Buffer.alloc(4)
  size.writeUInt32BE(data.length)
  crc.writeUInt32BE(crc32(Buffer.concat([label, data])))
  return Buffer.concat([size, label, data, crc])
}
function png(size, template = false) {
  const raw = Buffer.alloc((size * 4 + 1) * size)
  const sample = 4
  for (let y = 0; y < size; y++) for (let x = 0; x < size; x++) {
    let alpha = 0, star = 0
    for (let sy = 0; sy < sample; sy++) for (let sx = 0; sx < sample; sx++) {
      const u = (x + (sx + 0.5) / sample) / size - 0.5
      const v = (y + (sy + 0.5) / sample) / size - 0.5
      const a = Math.abs(u), b = Math.abs(v)
      const inside = Math.hypot(Math.max(a - 0.29, 0), Math.max(b - 0.29, 0)) < 0.17
      // The central diamond with concave edges forms a simple four-point star.
      const inStar = a + b <= 0.13 || (a <= 0.1 * (1 - b / 0.38) && b <= 0.38) || (b <= 0.1 * (1 - a / 0.38) && a <= 0.38)
      if (template ? inStar : inside) alpha++
      if (inStar) star++
    }
    const offset = y * (size * 4 + 1) + 1 + x * 4
    const mix = template ? 0 : star / (sample * sample)
    for (let c = 0; c < 3; c++) raw[offset + c] = template ? 0 : Math.round([30, 61, 50][c] * (1 - mix) + [241, 244, 226][c] * mix)
    raw[offset + 3] = Math.round(255 * alpha / (sample * sample))
  }
  const header = Buffer.alloc(13)
  header.writeUInt32BE(size, 0); header.writeUInt32BE(size, 4); header[8] = 8; header[9] = 6
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', header), chunk('IDAT', deflateSync(raw)), chunk('IEND', Buffer.alloc(0))])
}
const resources = path.join(root, 'apps/desktop/resources')
await mkdir(resources, { recursive: true })
await writeFile(path.join(resources, 'trayTemplate.png'), png(44, true))
await writeFile(path.join(resources, 'icon.png'), png(1024))
if (process.platform === 'darwin') {
  const set = path.join(resources, 'icon.iconset')
  await mkdir(set, { recursive: true })
  for (const size of [16, 32, 128, 256, 512]) {
    await writeFile(path.join(set, `icon_${size}x${size}.png`), png(size))
    await writeFile(path.join(set, `icon_${size}x${size}@2x.png`), png(size * 2))
  }
  const result = spawnSync('iconutil', ['-c', 'icns', set, '-o', path.join(resources, 'icon.icns')], { stdio: 'inherit' })
  if (result.status !== 0) throw new Error('macOS icon generation failed.')
  await rm(set, { recursive: true })
}
