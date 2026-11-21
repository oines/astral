import { execFileSync, spawnSync } from 'node:child_process'
import { beforeEach, describe, expect, it } from 'vitest'
import { applyDownloadSource, normalizeGitHubProxy, resetDownloadSourceState, useDownloadSource } from './download-source'

describe('download-source', () => {
  beforeEach(() => {
    resetDownloadSourceState()
  })

  it('normalizes and validates HTTPS GitHub proxy prefixes', () => {
    expect(normalizeGitHubProxy('')).toEqual({ ok: true, value: '' })
    expect(normalizeGitHubProxy('https://edgeone.gh-proxy.org')).toEqual({ ok: true, value: 'https://edgeone.gh-proxy.org/' })
    expect(normalizeGitHubProxy('https://mirror.example.edu/github')).toEqual({ ok: true, value: 'https://mirror.example.edu/github/' })
    expect(normalizeGitHubProxy('http://mirror.example.edu/')).toEqual({ ok: false, reason: 'scheme' })
    expect(normalizeGitHubProxy('https://mirror.example.edu/?a=1')).toEqual({ ok: false, reason: 'invalid' })
    expect(normalizeGitHubProxy('https://user:pass@mirror.example.edu/')).toEqual({ ok: false, reason: 'invalid' })
    expect(normalizeGitHubProxy('not a url')).toEqual({ ok: false, reason: 'invalid' })
  })

  const official = 'https://github.com/oines/astral/releases/latest/download/install-computer.sh'
  const shell = 'curl --fail --silent --show-error --location --proto \'=https\' --proto-redir \'=https\' "${ASTRAL_GITHUB_PROXY:+${ASTRAL_GITHUB_PROXY%/}/}https://github.com/oines/astral/releases/latest/download/install-computer.sh" | sh -s -- --temporary'
  const mirror = 'https://cdn.gh-proxy.org/'
  const original = 'https://previous.example/'

  it.skipIf(process.platform === 'win32').each(['', mirror])('uses the selected source %j for both shell downloads and preserves the terminal', source => {
    const script = `
      exec 2>&1
      curl() {
        for url do :; done
        printf 'bootstrap=%s\\n' "$url" >&2
        printf '%s\\n' 'printf "installer=%s\\n" "$ASTRAL_GITHUB_PROXY"'
      }
      ${applyDownloadSource(shell, 'shell', source)}
      printf 'terminal=%s\\n' "$ASTRAL_GITHUB_PROXY"
    `
    const out = execFileSync('sh', ['-c', script], { encoding: 'utf8', env: { ...process.env, ASTRAL_GITHUB_PROXY: original } })
    expect(out.trim().split('\n')).toEqual([`bootstrap=${source}${official}`, `installer=${source}`, `terminal=${original}`])
  })

  const powershell = process.platform === 'win32' ? 'powershell.exe' : 'pwsh'
  const hasPowerShell = !spawnSync(powershell, ['-NoProfile', '-Command', 'exit 0']).error
  it.skipIf(!hasPowerShell).each(['', mirror])('restores the PowerShell terminal on success and failure for source %j', source => {
    const report = 'Write-Output ("installer=" + $env:ASTRAL_GITHUB_PROXY)'
    const script = [
      applyDownloadSource(`& { ${report} }`, 'powershell', source),
      'Write-Output ("terminal=" + $env:ASTRAL_GITHUB_PROXY)',
      `try { ${applyDownloadSource(`& { ${report}; throw 'installer failed' }`, 'powershell', source)} } catch { Write-Output $_.Exception.Message }`,
      'Write-Output ("terminal=" + $env:ASTRAL_GITHUB_PROXY)',
    ].join('\n')
    const out = execFileSync(powershell, ['-NoProfile', '-NonInteractive', '-Command', script], { encoding: 'utf8', env: { ...process.env, ASTRAL_GITHUB_PROXY: original } })
    expect(out.trim().split(/\r?\n/)).toEqual([`installer=${source}`, `terminal=${original}`, `installer=${source}`, 'installer failed', `terminal=${original}`])
  })

  it('tracks preset and custom URL validation state', () => {
    const source = useDownloadSource()
    expect(source.preset.value).toBe('direct')
    expect(source.valid.value).toBe(true)
    expect(source.proxy.value).toBe('')
    expect(source.isMirror.value).toBe(false)

    source.preset.value = 'edgeone'
    expect(source.valid.value).toBe(true)
    expect(source.proxy.value).toBe('https://edgeone.gh-proxy.org/')
    expect(source.isMirror.value).toBe(true)

    source.preset.value = 'custom'
    expect(source.valid.value).toBe(false)
    source.customURL.value = 'https://mirror.campus.edu/gh'
    expect(source.valid.value).toBe(true)
    expect(source.proxy.value).toBe('https://mirror.campus.edu/gh/')
  })
})
