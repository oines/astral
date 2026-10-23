import { mkdir, readFile, writeFile, rm } from 'node:fs/promises'
import { spawnSync } from 'node:child_process'
import path from 'node:path'
import { Resvg } from '@resvg/resvg-js'
import { root } from './build-lib.mjs'

const assets = path.join(root, 'packages/icons/assets')
const [mark, tray] = await Promise.all([
  readFile(path.join(assets, 'astral-mark.svg'), 'utf8'),
  readFile(path.join(assets, 'astral-tray.svg'), 'utf8'),
])

function png(svg, size) {
  return new Resvg(svg, {
    fitTo: { mode: 'width', value: size },
    font: { loadSystemFonts: false },
  }).render().asPng()
}

function ico(svg) {
  const sizes = [16, 24, 32, 48, 64, 128, 256]
  const images = sizes.map(size => png(svg, size))
  const header = Buffer.alloc(6 + sizes.length * 16)
  header.writeUInt16LE(1, 2)
  header.writeUInt16LE(sizes.length, 4)
  let offset = header.length
  for (const [index, size] of sizes.entries()) {
    const entry = 6 + index * 16
    header[entry] = size === 256 ? 0 : size
    header[entry + 1] = size === 256 ? 0 : size
    header.writeUInt16LE(1, entry + 4)
    header.writeUInt16LE(32, entry + 6)
    header.writeUInt32LE(images[index].length, entry + 8)
    header.writeUInt32LE(offset, entry + 12)
    offset += images[index].length
  }
  return Buffer.concat([header, ...images])
}

function appIcon(dark = false) {
  // Match the 824px tile and 100px inset used by the macOS ICNS grid.
  const logo = mark.replace(/<svg\b[^>]*>/, '<svg x="128" y="170" width="768" height="768" viewBox="0 0 24 24" fill="none">')
  return `<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024" role="img" aria-label="Astral ${dark ? 'Dark' : 'Light'} App Icon">
  <defs>
    <filter id="appTileShadow" x="-15%" y="-15%" width="130%" height="135%" color-interpolation-filters="sRGB">
      <feDropShadow dx="0" dy="12" stdDeviation="12" flood-color="#000" flood-opacity="${dark ? '.24' : '.16'}"/>
    </filter>
  </defs>
  <rect x="100" y="100" width="824" height="824" rx="185.4" fill="${dark ? '#25252b' : '#ffffff'}" filter="url(#appTileShadow)"/>
  <rect x="101" y="101" width="822" height="822" rx="184.4" fill="none" stroke="${dark ? '#ffffff' : '#000000'}" stroke-opacity="${dark ? '.10' : '.05'}" stroke-width="2"/>
  ${logo}
</svg>
`
}

// The small monochrome mark stays legible on both browser tab themes.
const web = path.join(root, 'apps/web/public')
await mkdir(web, { recursive: true })
await writeFile(path.join(web, 'favicon.svg'), tray.replace('</svg>', `<style>
  svg { color: #171a26; }
  @media (prefers-color-scheme: dark) { svg { color: #f7f6fc; } }
</style></svg>`))

if (!process.argv.includes('--web')) {
  const resources = path.join(root, 'apps/desktop/resources')
  const icons = [['icon', appIcon()], ['icon-dark', appIcon(true)]]
  await mkdir(resources, { recursive: true })
  await Promise.all([
    writeFile(path.join(resources, 'trayTemplate.png'), png(tray, 22)),
    writeFile(path.join(resources, 'trayTemplate@2x.png'), png(tray, 44)),
    writeFile(path.join(resources, 'icon.ico'), ico(appIcon())),
  ])
  for (const [name, svg] of icons) {
    await writeFile(path.join(resources, `${name}.svg`), svg)
    await writeFile(path.join(resources, `${name}.png`), png(svg, 1024))
    if (process.platform === 'darwin') {
      const set = path.join(resources, 'icon.iconset')
      await mkdir(set, { recursive: true })
      for (const size of [16, 32, 128, 256, 512]) {
        await writeFile(path.join(set, `icon_${size}x${size}.png`), png(svg, size))
        await writeFile(path.join(set, `icon_${size}x${size}@2x.png`), png(svg, size * 2))
      }
      const result = spawnSync('iconutil', ['-c', 'icns', set, '-o', path.join(resources, `${name}.icns`)], { stdio: 'inherit' })
      if (result.error) throw result.error
      if (result.status !== 0) throw new Error('macOS icon generation failed.')
      await rm(set, { recursive: true })
    }
  }
}
