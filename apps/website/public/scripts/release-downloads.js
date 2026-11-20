/* global AbortSignal, document, fetch, navigator */
// Progressive enhancement only: the static, version-labelled downloads remain
// usable when JavaScript, GitHub's API, CORS, or its public rate limit fails.
const repository = 'https://github.com/oines/astral'
const endpoint = 'https://api.github.com/repos/oines/astral/releases/latest'
const mirrorPrefix = 'https://gh-proxy.org/'
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

const recommendations = {
  mac: {
    zh: { heading: '检测到 macOS', note: 'Apple M 系列芯片请选择 Apple 芯片版本；Intel Mac 请选择 Intel 版本。', primary: ['mac-arm64-dmg', '下载 macOS (Apple 芯片 · DMG)'], secondary: ['mac-x64-dmg', 'Intel Mac (DMG)'] },
    en: { heading: 'Detected macOS', note: 'Choose Apple silicon for M-series Macs, or Intel for Intel-based Macs.', primary: ['mac-arm64-dmg', 'Download for macOS (Apple silicon)'], secondary: ['mac-x64-dmg', 'Intel Mac (DMG)'] },
  },
  win: {
    zh: { heading: '检测到 Windows', note: '大多数 Intel / AMD 电脑选择 x64；ARM 设备选择 ARM64。', primary: ['win-x64-exe', '下载 Windows (x64 · EXE)'], secondary: ['win-arm64-exe', 'Windows ARM64 (EXE)'] },
    en: { heading: 'Detected Windows', note: 'Use x64 for most Intel or AMD PCs, or ARM64 for ARM devices.', primary: ['win-x64-exe', 'Download for Windows (x64)'], secondary: ['win-arm64-exe', 'Windows ARM64 (EXE)'] },
  },
  linux: {
    zh: { heading: '检测到 Linux', note: 'AppImage 支持应用内更新；Debian / Ubuntu 可选 DEB 安装包。', primary: ['linux-x64-appimage', '下载 Linux (x86_64 · AppImage)'], secondary: ['linux-x64-deb', 'Debian / Ubuntu (DEB)'] },
    en: { heading: 'Detected Linux', note: 'AppImage supports in-app updates; choose DEB for Debian or Ubuntu package installers.', primary: ['linux-x64-appimage', 'Download for Linux (x86_64 AppImage)'], secondary: ['linux-x64-deb', 'Debian / Ubuntu (DEB)'] },
  },
}

function versionParts(tag) {
  const match = typeof tag === 'string' && /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.exec(tag)
  if (!match) return undefined
  const parts = match.slice(1).map(Number)
  return parts.every(Number.isSafeInteger) ? parts : undefined
}

function detectPlatform() {
  if (typeof navigator === 'undefined') return undefined
  const ua = `${navigator.userAgentData?.platform || ''} ${navigator.platform || ''} ${navigator.userAgent || ''}`.toLowerCase()
  if (/iphone|ipad|ipod|android/.test(ua)) return undefined
  if (/mac/.test(ua)) return 'mac'
  if (/win/.test(ua)) return 'win'
  if (/linux|x11/.test(ua)) return 'linux'
  return undefined
}

function applyDownloadUrls(linkMap, baseUrls, useMirror, panel) {
  const format = url => (useMirror ? `${mirrorPrefix}${url}` : url)
  for (const [key, link] of linkMap.entries()) {
    const base = baseUrls.get(key)
    if (base) link.href = format(base)
  }
  if (!panel) return
  for (const anchor of panel.querySelectorAll?.('[data-astral-recommended-target]') || []) {
    const key = anchor.dataset?.astralRecommendedTarget
    const base = key ? baseUrls.get(key) : undefined
    if (base) anchor.href = format(base)
  }
}

function setupEnhancements(linkMap, baseUrls) {
  const mirrorCheckbox = document.querySelector?.('[data-astral-mirror]')
  const mirrorWrap = document.querySelector?.('[data-astral-mirror-wrap]')
  const panel = document.querySelector?.('[data-astral-download-panel]')

  if (panel && typeof document.createElement === 'function') {
    const platform = detectPlatform()
    const locale = panel.dataset?.locale === 'zh-cn' ? 'zh' : 'en'
    const config = platform ? recommendations[platform]?.[locale] : undefined
    if (config && baseUrls.has(config.primary[0]) && baseUrls.has(config.secondary[0])) {
      const heading = panel.querySelector('[data-astral-panel-heading]')
      const note = panel.querySelector('[data-astral-panel-note]')
      const primary = panel.querySelector('[data-astral-panel-primary]')
      const secondary = panel.querySelector('[data-astral-panel-secondary]')
      if (heading && note && primary && secondary) {
        heading.textContent = config.heading
        note.textContent = config.note
        primary.dataset.astralRecommendedTarget = config.primary[0]
        primary.textContent = config.primary[1]
        secondary.dataset.astralRecommendedTarget = config.secondary[0]
        secondary.textContent = config.secondary[1]
        panel.hidden = false
      }
    }
  }

  if (mirrorWrap) mirrorWrap.hidden = false
  const sync = () => applyDownloadUrls(linkMap, baseUrls, Boolean(mirrorCheckbox?.checked), panel)
  if (mirrorCheckbox && mirrorCheckbox.dataset?.bound !== 'true') {
    if (mirrorCheckbox.dataset) mirrorCheckbox.dataset.bound = 'true'
    mirrorCheckbox.addEventListener?.('change', sync)
  }
  sync()
  return { panel, sync }
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

  const linkMap = new Map(targets.map(([key], index) => [key, links[index][0]]))
  const baseUrls = new Map(targets.map(([key], index) => [key, links[index][0].href]))
  const { sync } = setupEnhancements(linkMap, baseUrls)

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
    for (let index = 0; index < targets.length; index++) {
      baseUrls.set(targets[index][0], urls[index])
    }
    sync()
    notes.href = `${repository}/releases/tag/${release.tag_name}`
    label.textContent = `${label.dataset.latestPrefix}${release.tag_name}`
  } catch { /* Keep the working static downloads and the permanent GitHub Latest link. */ }
}

void refreshDownloads()
document.addEventListener('astro:page-load', () => { void refreshDownloads() })
