import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { settingsUxEn, settingsUxZh } from '../i18n/settings-ux'
import { useNetworkSettings } from './useNetworkSettings'
import { useSettingsSave } from './useSettingsSave'

type NetworkPatch = { allowed_hostnames?: string[]; restrict_hostnames?: boolean; allow_remote_management?: boolean }
const state = vi.hoisted(() => ({ app: {} as Record<string, unknown> }))
vi.mock('../stores/app', () => ({ useAppStore: () => state.app }))
vi.mock('@pinia/colada', async importOriginal => ({ ...await importOriginal<typeof import('@pinia/colada')>(), useQueryCache: () => ({}) }))

function setup(locale: 'en' | 'zh' = 'en') {
  const network = reactive({
    public_urls: { value: ['http://astral.example:4545'], saved_value: [] as string[], source: 'discovery', editable: true },
    allowed_hostnames: { value: [] as string[], saved_value: [] as string[], source: 'default', editable: true },
    restrict_hostnames: { value: false, saved_value: false, source: 'default', editable: true },
    allow_remote_management: { value: false, saved_value: false, source: 'default', editable: true },
  })
  const apply = (patch: NetworkPatch) => {
    if (patch.allowed_hostnames !== undefined) network.allowed_hostnames.saved_value = network.allowed_hostnames.value = patch.allowed_hostnames
    if (patch.restrict_hostnames !== undefined) network.restrict_hostnames.value = patch.restrict_hostnames
    if (patch.allow_remote_management !== undefined) network.allow_remote_management.value = patch.allow_remote_management
    return network
  }
  const save = vi.fn(async (patch: NetworkPatch) => apply(patch))
  const refresh = vi.fn(async () => network)
  state.app = reactive({ data: { network }, networkSettings: save, refreshNetwork: refresh })
  const dictionary = locale === 'zh' ? settingsUxZh : settingsUxEn
  const message = (key: string, values: Record<string, string> = {}) => Object.entries(values).reduce((text, [key, value]) => text.replace(`{${key}}`, value), String(dictionary[key as keyof typeof dictionary] || key))
  return { editor: useNetworkSettings(message), network, save, refresh, apply }
}
beforeEach(() => { vi.stubGlobal('window', { location: { hostname: 'astral.example' } }) })
afterEach(() => { useSettingsSave().discardForm('network/hostnames'); vi.unstubAllGlobals() })

describe('hostname settings draft', () => {
  it('does not apply editing, adding, removing or toggling until the entire group is explicitly saved', async () => {
    const { editor, save, network } = setup()
    editor.setRestrictHostnames(true)
    editor.addHostname(); editor.setHostname(1, ' extra.example ')
    editor.addHostname(); editor.setHostname(2, 'temporary.example'); editor.removeHostname(2)
    await Promise.resolve()
    expect(editor.restrictHostnames.value).toBe(true)
    expect(editor.hostnames.value).toEqual(['astral.example', ' extra.example '])
    expect(network.restrict_hostnames.value).toBe(false)
    expect(network.allowed_hostnames.value).toEqual([])
    expect(save).not.toHaveBeenCalled()
    await editor.saveHostnameSettings()
    expect(save).toHaveBeenCalledExactlyOnceWith({ restrict_hostnames: true, allowed_hostnames: ['astral.example', 'extra.example'] })
    expect(editor.hasHostnameDraft.value).toBe(false)
    expect(network.restrict_hostnames.value).toBe(true)
    expect(network.allowed_hostnames.value).toEqual(['astral.example', 'extra.example'])
    expect(network.allow_remote_management.value).toBe(false)
    expect(network.public_urls.value).toEqual(['http://astral.example:4545'])
  })

  it('can save disabling the restriction and clearing its list as one change', async () => {
    const { editor, save, network } = setup()
    network.restrict_hostnames.value = true
    network.allowed_hostnames.value = ['astral.example']
    editor.setRestrictHostnames(false); editor.removeHostname(0)
    expect(network.restrict_hostnames.value).toBe(true)
    expect(network.allowed_hostnames.value).toEqual(['astral.example'])
    expect(save).not.toHaveBeenCalled()
    await editor.saveHostnameSettings()
    expect(save).toHaveBeenCalledExactlyOnceWith({ restrict_hostnames: false, allowed_hostnames: [] })
  })

  it.each([
    { values: [] as string[], part: '至少一个' },
    { values: ['other.example'], part: 'astral.example' },
    { values: ['https://astral.example/path'], part: '不要包含协议、端口或路径' },
  ])('keeps the entire rejected group draft and gives a local explanation for $values', async ({ values, part }) => {
    const { editor, save, network } = setup('zh')
    editor.setRestrictHostnames(true)
    editor.removeHostname(0)
    for (const [index, value] of values.entries()) { editor.addHostname(); editor.setHostname(index, value) }
    await editor.saveHostnameSettings()
    expect(save).not.toHaveBeenCalled()
    expect(editor.restrictHostnames.value).toBe(true)
    expect(editor.hostnames.value).toEqual(values)
    expect(editor.hasHostnameDraft.value).toBe(true)
    expect(network.restrict_hostnames.value).toBe(false)
    expect(network.allowed_hostnames.value).toEqual([])
    expect(editor.hostnameSaveState.value).toMatchObject({ status: 'error', error: expect.stringContaining(part) })
  })

  it('preserves a Server-rejected group and translates its known hostname format error', async () => {
    const { editor, save, network } = setup('zh')
    editor.setRestrictHostnames(true)
    editor.addHostname(); editor.setHostname(1, 'invalid_label.example')
    save.mockRejectedValueOnce({ code: 'validation', message: 'Enter a hostname or IP address without a protocol, port, path or wildcard.' })
    await editor.saveHostnameSettings()
    expect(editor.hostnames.value).toEqual(['astral.example', 'invalid_label.example'])
    expect(editor.restrictHostnames.value).toBe(true)
    expect(network.restrict_hostnames.value).toBe(false)
    expect(editor.hostnameSaveState.value.error).toContain('请填写域名或 IP')
  })

  it('reconciles both fields after a committed group response is lost without resubmitting', async () => {
    const { editor, network, save, refresh, apply } = setup()
    editor.setRestrictHostnames(true)
    editor.addHostname(); editor.setHostname(1, 'BÜCHER.example.')
    save.mockImplementationOnce(async patch => {
      apply({ ...patch, allowed_hostnames: ['astral.example', 'xn--bcher-kva.example'] })
      throw new TypeError('connection lost')
    })
    await editor.saveHostnameSettings()
    expect(refresh).toHaveBeenCalledOnce()
    expect(save).toHaveBeenCalledOnce()
    expect(editor.hasHostnameDraft.value).toBe(false)
    expect(editor.restrictHostnames.value).toBe(true)
    expect(editor.hostnames.value).toEqual(network.allowed_hostnames.value)
    expect(editor.hostnameSaveState.value.status).toBe('saved')
  })

  it('keeps newer edits after a save finishes and waits for another explicit save', async () => {
    const { editor, network, save, apply } = setup()
    let finish!: () => void
    const pending = new Promise<void>(resolve => { finish = resolve })
    save.mockImplementationOnce(async patch => { await pending; return apply(patch) })
    editor.setRestrictHostnames(true); editor.addHostname(); editor.setHostname(1, 'first.example')
    const saving = editor.saveHostnameSettings()
    await Promise.resolve()
    editor.setHostname(1, 'newer.example')
    finish(); await saving
    expect(editor.hostnames.value).toEqual(['astral.example', 'newer.example'])
    expect(network.allowed_hostnames.value).toEqual(['astral.example', 'first.example'])
    expect(save).toHaveBeenCalledOnce()
    await editor.saveHostnameSettings()
    expect(save).toHaveBeenCalledTimes(2)
    expect(network.allowed_hostnames.value).toEqual(['astral.example', 'newer.example'])
  })

  it('keeps controlled fields read-only and submits only editable members of the group', async () => {
    const { editor, network, save } = setup()
    network.allowed_hostnames.editable = false
    network.allowed_hostnames.value = ['astral.example']
    network.restrict_hostnames.editable = false
    editor.addHostname(); editor.setHostname(0, 'other.example'); editor.removeHostname(0); editor.setRestrictHostnames(true)
    await editor.saveHostnameSettings()
    expect(editor.hasHostnameDraft.value).toBe(false)
    expect(save).not.toHaveBeenCalled()
    network.restrict_hostnames.editable = true
    editor.setRestrictHostnames(true)
    await editor.saveHostnameSettings()
    expect(save).toHaveBeenCalledExactlyOnceWith({ restrict_hostnames: true })
  })

  it('leaves Desktop remote management automatic and separate from the hostname draft', async () => {
    const { editor, network, save } = setup()
    editor.setRestrictHostnames(true)
    await editor.setAllowRemote(true)
    expect(save).toHaveBeenCalledExactlyOnceWith({ allow_remote_management: true })
    expect(network.restrict_hostnames.value).toBe(false)
    expect(editor.hasHostnameDraft.value).toBe(true)
    await editor.saveHostnameSettings()
    expect(save).toHaveBeenLastCalledWith({ restrict_hostnames: true, allowed_hostnames: ['astral.example'] })
  })
})
