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

  it('rewrites shell and PowerShell commands in place for mirrors and leaves direct untouched', () => {
    const shell = 'curl --fail --silent --show-error --location --proto \'=https\' --proto-redir \'=https\' "${ASTRAL_GITHUB_PROXY:+${ASTRAL_GITHUB_PROXY%/}/}https://github.com/oines/astral/releases/latest/download/install-computer.sh" | sh -s -- --server \'https://astral.example\' --temporary'
    expect(applyDownloadSource(shell, 'shell', '')).toBe(shell)
    expect(applyDownloadSource(shell, 'shell', 'https://edgeone.gh-proxy.org/')).toBe(
      "curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' 'https://edgeone.gh-proxy.org/https://github.com/oines/astral/releases/latest/download/install-computer.sh' | ASTRAL_GITHUB_PROXY='https://edgeone.gh-proxy.org/' sh -s -- --server 'https://astral.example' --temporary",
    )

    const ps = "& { $astralInstallerUrl = 'https://github.com/oines/astral/releases/latest/download/install-computer.ps1'; if ($env:ASTRAL_GITHUB_PROXY) { $astralInstallerUrl = $env:ASTRAL_GITHUB_PROXY.TrimEnd('/') + '/' + $astralInstallerUrl }; & ([scriptblock]::Create(($astralInstaller -join \"`n\"))) -Temporary }"
    expect(applyDownloadSource(ps, 'powershell', '')).toBe(ps)
    expect(applyDownloadSource(ps, 'powershell', 'https://cdn.gh-proxy.org/')).toBe(
      "& { $env:ASTRAL_GITHUB_PROXY = 'https://cdn.gh-proxy.org/'; $astralInstallerUrl = 'https://github.com/oines/astral/releases/latest/download/install-computer.ps1'; if ($env:ASTRAL_GITHUB_PROXY) { $astralInstallerUrl = $env:ASTRAL_GITHUB_PROXY.TrimEnd('/') + '/' + $astralInstallerUrl }; & ([scriptblock]::Create(($astralInstaller -join \"`n\"))) -Temporary }",
    )
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
