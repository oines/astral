import { afterEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { useNetworkSettings } from './useNetworkSettings'
import { useSettingsSave } from './useSettingsSave'

const state = vi.hoisted(() => ({ app: {} as Record<string, unknown> }))
vi.mock('../stores/app', () => ({ useAppStore: () => state.app }))
vi.mock('@pinia/colada', async importOriginal => ({ ...await importOriginal<typeof import('@pinia/colada')>(), useQueryCache: () => ({}) }))

function setup() {
  const network = reactive({
    public_urls: { value: ['http://astral.example:4545'], saved_value: [] as string[], source: 'discovery', editable: true },
    allow_remote_management: { value: false, saved_value: false, source: 'default', editable: true },
  })
  const save = vi.fn(async (patch: { public_urls?: string[]; allow_remote_management?: boolean }) => {
    if (patch.public_urls !== undefined) {
      network.public_urls.saved_value = patch.public_urls
      network.public_urls.value = patch.public_urls.length ? patch.public_urls : ['http://astral.example:4545']
    }
    if (patch.allow_remote_management !== undefined) network.allow_remote_management.value = patch.allow_remote_management
    return network
  })
  const refresh = vi.fn(async () => network)
  state.app = reactive({ data: { network }, networkSettings: save, refreshNetwork: refresh })
  return { editor: useNetworkSettings(), network, save, refresh }
}
afterEach(() => { useSettingsSave().discardForm('network/public_urls') })

describe('network address list editor', () => {
  it('preserves priority order and saves addresses without enabling remote management', async () => {
    const { editor, save, network } = setup()
    editor.addAddress(); editor.setAddress(0, ' https://astral.example/ ')
    editor.addAddress(); editor.setAddress(1, 'http://100.90.80.70:4545')
    await editor.saveAddresses()
    expect(save).toHaveBeenCalledWith({ public_urls: ['https://astral.example', 'http://100.90.80.70:4545'] })
    expect(network.allow_remote_management.value).toBe(false)
    await editor.removeAddress(0)
    expect(network.public_urls.saved_value).toEqual(['http://100.90.80.70:4545'])
    await editor.removeAddress(0)
    expect(save).toHaveBeenLastCalledWith({ public_urls: [] })
    expect(network.public_urls.value).toEqual(['http://astral.example:4545'])
  })

  it('keeps rejected list edits and restores the confirmed toggle state', async () => {
    const { editor, save, network } = setup()
    save.mockRejectedValue({ code: 'validation', message: 'Invalid address' })
    editor.addAddress(); editor.setAddress(0, 'invalid URL')
    await editor.saveAddresses()
    expect(editor.addresses.value).toEqual(['invalid URL'])
    expect(network.public_urls.saved_value).toEqual([])
    await editor.setAllowRemote(true)
    expect(editor.allowRemote.value).toBe(false)
  })

  it('reconciles a lost response before reporting a committed toggle as failed', async () => {
    const { editor, network, save, refresh } = setup()
    save.mockImplementation(async () => { network.allow_remote_management.value = true; throw new TypeError('connection lost') })
    await editor.setAllowRemote(true)
    expect(refresh).toHaveBeenCalledOnce()
    expect(editor.allowRemote.value).toBe(true)
    expect(save).toHaveBeenCalledOnce()
  })

  it('recognizes the normalized list after a committed response is lost', async () => {
    const { editor, network, save, refresh } = setup()
    save.mockImplementationOnce(async () => {
      network.public_urls.saved_value = network.public_urls.value = ['https://custom.example']
      throw new TypeError('connection lost')
    })
    editor.addAddress(); editor.setAddress(0, 'https://custom.example/')
    await editor.saveAddresses()
    expect(refresh).toHaveBeenCalledOnce()
    expect(editor.addresses.value).toEqual(['https://custom.example'])
    expect(useSettingsSave().state('network/public_urls').status).toBe('saved')
  })

  it('does not send edits for command-line or environment controlled fields', async () => {
    const { editor, network, save } = setup()
    network.public_urls.editable = false
    network.public_urls.source = 'environment'
    network.allow_remote_management.editable = false
    network.allow_remote_management.source = 'cli'
    expect(editor.addresses.value).toEqual(network.public_urls.value)
    editor.addAddress()
    await editor.saveAddresses(); await editor.removeAddress(0); await editor.setAllowRemote(true)
    expect(save).not.toHaveBeenCalled()
  })

  it('keeps newer typing when an earlier list save completes', async () => {
    const { editor, network, save } = setup()
    let finish!: () => void
    const pending = new Promise<void>(resolve => { finish = resolve })
    save.mockImplementationOnce(async patch => {
      await pending
      network.public_urls.saved_value = network.public_urls.value = patch.public_urls!
      return network
    })
    editor.addAddress(); editor.setAddress(0, 'http://first.example:4545')
    const saving = editor.saveAddresses()
    await Promise.resolve()
    editor.setAddress(0, 'http://newer.example:4545')
    finish(); await saving
    expect(editor.addresses.value).toEqual(['http://newer.example:4545'])
    expect(network.public_urls.value).toEqual(['http://first.example:4545'])
    await editor.saveAddresses()
    expect(network.public_urls.value).toEqual(['http://newer.example:4545'])
  })

  it('keeps the latest toggle intent while reconciling an earlier lost response', async () => {
    const { editor, network, save, refresh } = setup()
    let finish!: () => void
    const pending = new Promise<void>(resolve => { finish = resolve })
    save.mockImplementationOnce(async () => {
      network.allow_remote_management.value = true
      await pending
      throw new TypeError('lost response')
    })
    const enabling = editor.setAllowRemote(true)
    await Promise.resolve()
    const disabling = editor.setAllowRemote(false)
    expect(editor.allowRemote.value).toBe(false)
    finish(); await Promise.all([enabling, disabling])
    expect(refresh).toHaveBeenCalledOnce()
    expect(network.allow_remote_management.value).toBe(false)
    expect(editor.allowRemote.value).toBe(false)
    expect(save).toHaveBeenLastCalledWith({ allow_remote_management: false })
  })
})
