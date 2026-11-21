import { computed, ref, watch } from 'vue'

export interface DownloadSourcePreset {
  id: 'direct' | 'edgeone' | 'cdn' | 'ghproxy' | 'ipv6'
  prefix: string
  label?: string
}

export const DOWNLOAD_SOURCE_PRESETS: readonly DownloadSourcePreset[] = [
  { id: 'direct', prefix: '' },
  { id: 'edgeone', prefix: 'https://edgeone.gh-proxy.org/', label: 'GH-Proxy (EdgeOne)' },
  { id: 'cdn', prefix: 'https://cdn.gh-proxy.org/', label: 'GH-Proxy (CDN)' },
  { id: 'ghproxy', prefix: 'https://gh-proxy.org/', label: 'GH-Proxy' },
  { id: 'ipv6', prefix: 'https://v6.gh-proxy.org/', label: 'GH-Proxy (IPv6)' },
] as const

export type DownloadSourcePresetID = DownloadSourcePreset['id'] | 'custom'

export type ProxyValidationResult =
  | { ok: true; value: string }
  | { ok: false; reason: 'empty' | 'scheme' | 'invalid' }

const STORAGE_KEY = 'astral:github-download-source'

export function normalizeGitHubProxy(raw: string): ProxyValidationResult {
  const value = raw.trim()
  if (!value) return { ok: true, value: '' }
  if (/\s/.test(value) || value.includes('\\')) return { ok: false, reason: 'invalid' }
  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    return { ok: false, reason: 'invalid' }
  }
  if (parsed.protocol !== 'https:') return { ok: false, reason: 'scheme' }
  if (!parsed.hostname || parsed.username || parsed.password || parsed.search || parsed.hash) {
    return { ok: false, reason: 'invalid' }
  }
  if (parsed.port) {
    const port = Number(parsed.port)
    if (!Number.isInteger(port) || port < 1 || port > 65535) return { ok: false, reason: 'invalid' }
  }
  const path = parsed.pathname && parsed.pathname !== '/' ? `${parsed.pathname.replace(/\/+$/, '')}/` : '/'
  return { ok: true, value: `${parsed.origin}${path}` }
}

function shellQuote(value: string) {
  return `'${value.replaceAll("'", "'\\''")}'`
}

function powerShellQuote(value: string) {
  return `'${value.replaceAll("'", "''")}'`
}

export function applyDownloadSource(command: string, platform: 'shell' | 'powershell', proxy: string): string {
  if (!command) return ''
  const checked = normalizeGitHubProxy(proxy)
  if (!checked.ok) return ''
  if (platform === 'powershell') {
    // Environment variables outlive a PowerShell script block, including when the installer throws.
    return `& { $previousAstralProxy = $env:ASTRAL_GITHUB_PROXY; try { $env:ASTRAL_GITHUB_PROXY = ${powerShellQuote(checked.value)}; ${command} } finally { $env:ASTRAL_GITHUB_PROXY = $previousAstralProxy } }`
  }
  // Both the bootstrap download and the installer inherit this selection within the subshell.
  return `(export ASTRAL_GITHUB_PROXY=${shellQuote(checked.value)}; ${command})`
}

interface StoredSource {
  preset: DownloadSourcePresetID
  custom: string
}

const VALID_PRESETS = new Set<string>(['direct', 'edgeone', 'cdn', 'ghproxy', 'ipv6', 'custom'])

function loadStoredSource(): StoredSource {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return { preset: 'direct', custom: '' }
    const parsed = JSON.parse(raw) as Partial<StoredSource>
    const preset = typeof parsed.preset === 'string' && VALID_PRESETS.has(parsed.preset)
      ? parsed.preset as DownloadSourcePresetID
      : 'direct'
    const custom = typeof parsed.custom === 'string' ? parsed.custom : ''
    return { preset, custom }
  } catch {
    return { preset: 'direct', custom: '' }
  }
}

const initial = loadStoredSource()
const preset = ref<DownloadSourcePresetID>(initial.preset)
const customURL = ref(initial.custom)

watch([preset, customURL], ([nextPreset, nextCustom]) => {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ preset: nextPreset, custom: nextCustom }))
  } catch {
    // Ignore storage quota or private-mode failures.
  }
})

export function resetDownloadSourceState() {
  preset.value = 'direct'
  customURL.value = ''
  try {
    localStorage.removeItem(STORAGE_KEY)
  } catch {
    // Ignore storage errors in tests.
  }
}

export function useDownloadSource() {
  const validation = computed<ProxyValidationResult>(() => {
    if (preset.value === 'custom') {
      if (!customURL.value.trim()) return { ok: false, reason: 'empty' }
      return normalizeGitHubProxy(customURL.value)
    }
    const found = DOWNLOAD_SOURCE_PRESETS.find(item => item.id === preset.value)
    return { ok: true, value: found?.prefix ?? '' }
  })
  const valid = computed(() => validation.value.ok)
  const proxy = computed(() => validation.value.ok ? validation.value.value : '')
  const isMirror = computed(() => preset.value !== 'direct')
  function displayLabel(t: (key: string) => string) {
    if (preset.value === 'direct') return t('downloadSource.direct')
    if (preset.value === 'custom') return proxy.value || customURL.value.trim() || t('downloadSource.custom')
    return DOWNLOAD_SOURCE_PRESETS.find(item => item.id === preset.value)?.label || t('downloadSource.direct')
  }
  return {
    preset,
    customURL,
    validation,
    valid,
    proxy,
    isMirror,
    displayLabel,
  }
}
