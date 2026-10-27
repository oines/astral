/* global AbortSignal, document, fetch */
// Progressive enhancement only: the static, version-labelled downloads remain
// usable when JavaScript, GitHub's API, CORS, or its public rate limit fails.
const repository = 'https://github.com/oines/astral'
const endpoint = 'https://api.github.com/repos/oines/astral/releases/latest'
const targets = [
  ['mac-arm64-dmg', 'mac-arm64.dmg'],
  ['mac-x64-dmg', 'mac-x64.dmg'],
  ['win-x64-exe', 'win-x64.exe'],
  ['win-arm64-exe', 'win-arm64.exe'],
  ['linux-x64-appimage', 'linux-x86_64.AppImage'],
  ['linux-arm64-appimage', 'linux-arm64.AppImage'],
  ['linux-x64-deb', 'linux-amd64.deb'],
  ['linux-arm64-deb', 'linux-arm64.deb'],
]

function versionParts(tag) {
  const match = typeof tag === 'string' && /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.exec(tag)
  if (!match) return undefined
  const parts = match.slice(1).map(Number)
  return parts.every(Number.isSafeInteger) ? parts : undefined
}

async function refreshDownloads() {
  const label = document.querySelector('[data-astral-release-status]')
  const notes = document.querySelector('[data-astral-release-notes]')
  if (!label || !notes || label.dataset.checked === 'true') return
  const fallback = versionParts(`v${label.dataset.fallbackVersion}`)
  if (!fallback || !label.dataset.latestPrefix) return
  const links = targets.map(([key]) => [...document.querySelectorAll(`[data-astral-download="${key}"]`)])
  if (links.some(found => found.length !== 1)) return
  label.dataset.checked = 'true'
  try {
    const response = await fetch(endpoint, {
      credentials: 'omit', mode: 'cors', redirect: 'error', cache: 'no-cache',
      headers: { Accept: 'application/vnd.github+json' },
      signal: AbortSignal.timeout(6000),
    })
    if (!response.ok) return
    const release = await response.json()
    const parts = versionParts(release?.tag_name)
    if (!parts || release.draft !== false || release.prerelease !== false ||
        typeof release.published_at !== 'string' || !Number.isFinite(Date.parse(release.published_at)) ||
        !Array.isArray(release.assets)) return
    const different = parts.findIndex((part, index) => part !== fallback[index])
    if (different !== -1 && parts[different] < fallback[different]) return
    const version = release.tag_name.slice(1)
    const urls = targets.map(([, suffix]) => {
      const name = `Astral-${version}-${suffix}`
      const expected = `${repository}/releases/download/${release.tag_name}/${name}`
      const matches = release.assets.filter(asset => asset?.name === name)
      if (matches.length !== 1) return undefined
      const asset = matches[0]
      return asset.state === 'uploaded' && Number.isSafeInteger(asset.size) && asset.size > 0 &&
        asset.browser_download_url === expected ? expected : undefined
    })
    // No partially refreshed version: every expected package must be present
    // under this repository's exact immutable tag before any DOM change.
    if (urls.some(url => !url) || !label.isConnected || document.querySelector('[data-astral-release-status]') !== label) return
    for (let index = 0; index < links.length; index++) links[index][0].href = urls[index]
    notes.href = `${repository}/releases/tag/${release.tag_name}`
    label.textContent = `${label.dataset.latestPrefix}${release.tag_name}`
  } catch { /* Keep the working static downloads and the permanent GitHub Latest link. */ }
}

void refreshDownloads()
document.addEventListener('astro:page-load', () => { void refreshDownloads() })
