/* eslint-disable vue/one-component-per-file -- Shared settings primitives are isolated SSR test stubs. */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { AstralInternalHttpapiNetworkSettings } from '@astral/sdk'
import { settingsUxEn } from '../../i18n/settings-ux'
import NetworkSettings from './NetworkSettings.vue'

const fixture = vi.hoisted(() => ({ network: undefined as AstralInternalHttpapiNetworkSettings | undefined }))
vi.mock('../../composables/useNetworkSettings', () => ({ useNetworkSettings: () => ({
  network: fixture.network, addresses: fixture.network?.public_urls.value || [], allowRemote: fixture.network?.allow_remote_management.value,
  setAddress: vi.fn(), addAddress: vi.fn(), removeAddress: vi.fn(), saveAddresses: vi.fn(), setAllowRemote: vi.fn(),
}) }))
vi.mock('../../composables/useClipboard', () => ({ useClipboard: () => ({ copy: vi.fn(), copied: false }) }))
vi.mock('./SettingsFeedback.vue', () => ({ default: defineComponent({ setup: () => () => undefined }) }))
vi.mock('@astral/icons', () => ({ Copy: defineComponent({ setup: () => () => h('svg') }) }))
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
  Switch: defineComponent({ props: { disabled: Boolean }, setup(props) { return () => h('input', { type: 'checkbox', disabled: props.disabled }) } }),
}))
beforeEach(() => {
  fixture.network = {
    addresses: [], restart_required: false,
    listen: { value: ':8080', saved_value: ':8080', source: 'default', editable: false },
    allow_remote_management: { value: true, saved_value: true, source: 'settings', editable: true },
    public_urls: { value: ['https://astral.example.com'], saved_value: ['https://astral.example.com'], source: 'settings', editable: true },
  }
})
const render = () => renderToString(createSSRApp(NetworkSettings))

describe('network setting explanations', () => {
  it('keeps the access scope while omitting routine configuration sources', async () => {
    const html = await render()
    expect(html).toContain('Turning this off does not stop Computer connections')
    expect(html).toContain('Full HTTP or HTTPS URLs for other devices')
    expect(html).not.toContain('app settings')
    expect(html).not.toContain('Controlled by')
  })
  it.each(['cli', 'environment'] as const)('explains the %s override where the settings are read-only', async source => {
    fixture.network!.allow_remote_management = { ...fixture.network!.allow_remote_management, source, editable: false }
    fixture.network!.public_urls = { ...fixture.network!.public_urls, source, editable: false }
    const html = await render()
    expect(html).toContain(`Controlled by ${settingsUxEn.networkSources[source]}; change it where Astral is started.`)
    expect(html).toContain('disabled')
    expect(html).not.toContain('Add address')
    expect(html).toContain('https://astral.example.com')
  })
})
