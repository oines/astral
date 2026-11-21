/* eslint-disable vue/one-component-per-file -- UI primitives are isolated test stubs. */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, getCurrentInstance, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import ComputerMaintenance from './ComputerMaintenance.vue'
import { resetDownloadSourceState, useDownloadSource } from '../../lib/download-source'
import type { Computer } from '../../lib/types'

interface Setup {
  update: () => Promise<void>
  command: string
  confirmDescription: string
  load: () => Promise<void>
}

const fixture = vi.hoisted(() => ({
  setup: undefined as Setup | undefined,
  computerMaintenance: vi.fn(),
  updateComputerDaemon: vi.fn(),
}))

vi.mock('@astral/sdk', async importOriginal => ({
  ...await importOriginal<object>(),
  computerMaintenance: fixture.computerMaintenance,
  updateComputerDaemon: fixture.updateComputerDaemon,
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: Record<string, string>) => params?.source ? `${key}:${params.source}` : key }) }))
vi.mock('../CopyValue.vue', () => ({ default: defineComponent({ props: { value: { type: String, default: '' } }, setup(props) { return () => h('code', props.value) } }) }))
vi.mock('@astral/ui', async () => ({
  ...await import('../../../../../packages/ui/src/useDelayedPending'),
  Button: defineComponent({ setup(_props, { slots, attrs }) { return () => h('button', attrs, slots.default?.()) } }),
  ConfirmPopover: defineComponent({
    props: { description: { type: String, default: '' } },
    setup(props, { slots }) {
      let parent = getCurrentInstance()!.parent
      while (!fixture.setup && parent) {
        const state = (parent as unknown as { setupState: Setup }).setupState
        if (typeof state.update === 'function') fixture.setup = state
        parent = parent.parent
      }
      return () => h('div', { 'data-confirm-desc': props.description }, slots.trigger?.())
    },
  }),
  SettingsSection: defineComponent({ setup(_props, { slots }) { return () => h('section', slots.default?.()) } }),
  SettingsRow: defineComponent({ props: { label: { type: String, default: '' }, description: { type: String, default: '' } }, setup(props, { slots }) { return () => h('div', { 'data-label': props.label, 'data-description': props.description }, slots.default?.()) } }),
}))

const rawShell = 'curl --fail --silent --show-error --location --proto \'=https\' --proto-redir \'=https\' "${ASTRAL_GITHUB_PROXY:+${ASTRAL_GITHUB_PROXY%/}/}https://github.com/oines/astral/releases/latest/download/install-computer.sh" | sh -s -- --update'
const rawPS = "& { $astralInstallerUrl = 'https://github.com/oines/astral/releases/latest/download/install-computer.ps1'; if ($env:ASTRAL_GITHUB_PROXY) { $astralInstallerUrl = $env:ASTRAL_GITHUB_PROXY.TrimEnd('/') + '/' + $astralInstallerUrl }; & ([scriptblock]::Create(($astralInstaller -join \"`n\"))) -Update }"

beforeEach(() => {
  resetDownloadSourceState()
  fixture.setup = undefined
  fixture.computerMaintenance.mockReset().mockResolvedValue({
    data: {
      can_update: true,
      daemon_newer: false,
      manager: 'systemd-user',
      command: rawShell,
      powershell_command: rawPS,
      update: { state: '' },
    },
  })
  fixture.updateComputerDaemon.mockReset().mockResolvedValue({ data: { state: 'checking', started_at: '2026-11-21T00:00:00Z' } })
})

describe('ComputerMaintenance', () => {
  it('hides target OS selector for bare-metal remote Computers and shows host OS selector only for containers', async () => {
    const bareMetal = { id: 'c1', name: 'Win Lab', kind: 'remote', os: 'windows', online: true, revoked: false } as Computer
    const htmlBare = await renderToString(createSSRApp(ComputerMaintenance, { computer: bareMetal }))
    expect(htmlBare).not.toContain('computerCommand.targetPlatform')
    expect(htmlBare).not.toContain('computerCommand.hostPlatform')
    expect(htmlBare).toContain('downloadSource.label')

    const container = { id: 'c2', name: 'Docker Box', kind: 'container', os: 'linux', online: true, revoked: false } as Computer
    const htmlContainer = await renderToString(createSSRApp(ComputerMaintenance, { computer: container }))
    expect(htmlContainer).toContain('computerCommand.hostPlatform')
  })

  it('passes selected download mirror to one-click Daemon update and rewrites the manual update command', async () => {
    const source = useDownloadSource()
    source.preset.value = 'edgeone'
    const remoteWin = { id: 'c-win', name: 'Win Lab', kind: 'remote', os: 'windows', online: true, revoked: false } as Computer
    await renderToString(createSSRApp(ComputerMaintenance, { computer: remoteWin }))
    await fixture.setup!.load()

    expect(fixture.setup!.confirmDescription).toBe('computerMaintenance.confirmUpdateWithSource:GH-Proxy (EdgeOne)')
    expect(fixture.setup!.command).toContain("$env:ASTRAL_GITHUB_PROXY = 'https://edgeone.gh-proxy.org/';")

    await fixture.setup!.update()
    expect(fixture.updateComputerDaemon).toHaveBeenCalledWith({
      path: { id: 'c-win' },
      body: { github_proxy: 'https://edgeone.gh-proxy.org/' },
    })
  })
})
