/* eslint-disable vue/one-component-per-file -- Shared settings primitives are isolated SSR test stubs. */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { AstralInternalHttpapiNetworkSettings } from '@astral/sdk'
import { settingsUxEn } from '../../i18n/settings-ux'
import NetworkSettings from './NetworkSettings.vue'

const fixture = vi.hoisted(() => ({ network: undefined as AstralInternalHttpapiNetworkSettings | undefined }))
vi.mock('../../composables/useNetworkSettings', () => ({ useNetworkSettings: () => ({
  network: fixture.network, hostnames: fixture.network?.allowed_hostnames.value || [], allowRemote: fixture.network?.allow_remote_management.value,
  restrictHostnames: fixture.network?.restrict_hostnames.value, showHostnames: fixture.network?.restrict_hostnames.value,
  hasHostnameDraft: false, hostnameSaveState: { status: 'idle' }, canEditHostnames: Boolean(fixture.network?.restrict_hostnames.editable || fixture.network?.allowed_hostnames.editable),
  setHostname: vi.fn(), addHostname: vi.fn(), removeHostname: vi.fn(), saveHostnameSettings: vi.fn(), setRestrictHostnames: vi.fn(), setAllowRemote: vi.fn(),
}) }))
vi.mock('./SettingsFeedback.vue', () => ({ default: defineComponent({ setup: () => () => undefined }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values: Record<string, string> = {}) => {
  const value = key.split('.').reduce<unknown>((value, part) => value && typeof value === 'object' ? (value as Record<string, unknown>)[part] : undefined, settingsUxEn)
  return Object.entries(values).reduce((text, [name, value]) => text.replace(`{${name}}`, value), typeof value === 'string' ? value : key)
} }) }))
vi.mock('@astral/ui', () => ({
  SettingsSection: defineComponent({ setup(_props, { slots }) { return () => h('section', slots.default?.()) } }),
  SettingsRow: defineComponent({ props: { label: { type: String, default: '' }, description: { type: String, default: '' } }, setup(props, { slots }) {
    return () => h('div', [h('strong', props.label), props.description ? h('p', props.description) : undefined, slots.default?.()])
  } }),
  Button: defineComponent({ setup(_props, { slots }) { return () => h('button', slots.default?.()) } }),
  Switch: defineComponent({ props: { disabled: Boolean, modelValue: Boolean, label: { type: String, default: '' } }, setup(props) { return () => h('input', { type: 'checkbox', disabled: props.disabled, checked: props.modelValue, 'aria-label': props.label }) } }),
}))
beforeEach(() => {
  fixture.network = {
    addresses: [], restart_required: false,
    listen: { value: ':8080', saved_value: ':8080', source: 'default', editable: false },
    allow_remote_management: { value: true, saved_value: true, source: 'settings', editable: true },
    public_urls: { value: ['https://astral.example.com'], saved_value: ['https://astral.example.com'], source: 'settings', editable: true },
    restrict_hostnames: { value: false, saved_value: false, source: 'default', editable: true },
    allowed_hostnames: { value: [], saved_value: [], source: 'default', editable: true },
  }
})
const render = () => renderToString(createSSRApp(NetworkSettings))

describe('network setting explanations', () => {
  it('offers an independent hostname restriction without a global access address editor', async () => {
    const html = await render()
    expect(html).toContain('Turning this off keeps Computer connections and your local reverse proxy available')
    expect(html).toContain('Restrict hostnames')
    expect(html).toContain('Computer connections are unaffected')
    expect(html).not.toContain('Access addresses')
    expect(html).not.toContain('https://astral.example.com')
    expect(html).not.toContain('Allowed hostnames')
    expect(html).not.toContain('app settings')
    expect(html).not.toContain('Controlled by')
  })
  it('does not offer an off switch for an independently deployed Server', async () => {
    fixture.network!.allow_remote_management = { value: true, saved_value: false, source: 'server', editable: false }
    const html = await render()
    expect(html).not.toContain('Allow remote management')
    expect(html).toContain('Restrict hostnames')
    expect(html).not.toContain('Controlled by')
  })
  it('keeps the editable remote management switch for Desktop', async () => {
    fixture.network!.allow_remote_management = { value: false, saved_value: false, source: 'default', editable: true }
    const html = await render()
    expect(html).toContain('Allow remote management')
    const control = html.match(/<input[^>]*aria-label="Allow remote management"[^>]*>/)?.[0]
    expect(control).toBeDefined()
    expect(control).not.toContain('disabled')
  })
  it('shows the active allowed hostnames and a local recovery command', async () => {
    fixture.network!.restrict_hostnames.value = true
    fixture.network!.allowed_hostnames.value = ['astral.example.com', '192.168.1.10']
    const html = await render()
    expect(html).toContain('Allowed hostnames')
    expect(html).toContain('value="astral.example.com"')
    expect(html).toContain('value="192.168.1.10"')
    expect(html).toContain('astral config hostnames disable')
    expect(html).toContain('Add hostname')
  })
  it.each(['cli', 'environment'] as const)('explains the %s override where the settings are read-only', async source => {
    fixture.network!.allow_remote_management = { ...fixture.network!.allow_remote_management, source, editable: false }
    fixture.network!.restrict_hostnames = { ...fixture.network!.restrict_hostnames, value: true, source, editable: false }
    fixture.network!.allowed_hostnames = { ...fixture.network!.allowed_hostnames, value: ['astral.example.com'], source, editable: false }
    const html = await render()
    expect(html).toContain(`Controlled by ${settingsUxEn.networkSources[source]}; change it where Astral is started.`)
    expect(html).toContain('disabled')
    expect(html).not.toContain('Add hostname')
    expect(html).toContain('astral.example.com')
  })
})
