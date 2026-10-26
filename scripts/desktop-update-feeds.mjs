import { createHash } from 'node:crypto'
import { createReadStream } from 'node:fs'
import { readFile, stat, writeFile } from 'node:fs/promises'
import path from 'node:path'

/** electron-builder uses target-specific names for Linux x64 artifacts. */
export function desktopArtifactName(version, platform, architecture, extension) {
  const targetArchitecture = platform === 'linux' && architecture === 'x64'
    ? { AppImage: 'x86_64', deb: 'amd64' }[extension] || architecture
    : architecture
  return `Astral-${version}-${platform}-${targetArchitecture}.${extension}`
}

/** Names match electron-updater GenericProvider with channel=latest-${process.arch}. */
export function desktopUpdateFeeds(version) {
  return ['x64', 'arm64'].flatMap(arch => [
    { name: `latest-${arch}.yml`, asset: desktopArtifactName(version, 'win', arch, 'exe') },
    { name: `latest-${arch}-mac.yml`, asset: desktopArtifactName(version, 'mac', arch, 'zip') },
    { name: `latest-${arch}-linux${arch === 'arm64' ? '-arm64' : ''}.yml`, asset: desktopArtifactName(version, 'linux', arch, 'AppImage') },
  ])
}

async function sha512(file) {
  const hash = createHash('sha512')
  for await (const chunk of createReadStream(file)) hash.update(chunk)
  return hash.digest('base64')
}

export async function writeDesktopUpdateFeeds(directory, version) {
  for (const feed of desktopUpdateFeeds(version)) {
    const file = path.join(directory, feed.asset)
    const data = { version, files: [{ url: `https://github.com/oines/astral/releases/download/v${version}/${feed.asset}`, sha512: await sha512(file), size: (await stat(file)).size }] }
    // JSON is a YAML subset and avoids a second serializer/parser dependency in
    // the credential-bearing release job. electron-updater parses this as YAML.
    await writeFile(path.join(directory, feed.name), `${JSON.stringify(data, null, 2)}\n`)
  }
}

export async function validateDesktopUpdateFeeds(directory, version) {
  for (const feed of desktopUpdateFeeds(version)) {
    const data = JSON.parse(await readFile(path.join(directory, feed.name), 'utf8'))
    if (!data || Object.keys(data).length !== 2 || data.version !== version || !Array.isArray(data.files) || data.files.length !== 1) throw new Error(`Invalid Desktop update metadata: ${feed.name}`)
    const asset = data.files[0]
    const file = path.join(directory, feed.asset)
    if (!asset || Object.keys(asset).length !== 3 || asset.url !== `https://github.com/oines/astral/releases/download/v${version}/${feed.asset}` || asset.size !== (await stat(file)).size || asset.sha512 !== await sha512(file)) throw new Error(`Desktop update metadata does not match final platform/architecture bytes: ${feed.name}`)
  }
}
