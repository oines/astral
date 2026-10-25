import { afterEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { useNetworkSettings } from './useNetworkSettings'
import { useSettingsSave } from './useSettingsSave'

const state = vi.hoisted(() => ({ app: {} as Record<string, unknown> }))
vi.mock('../stores/app', () => ({ useAppStore: () => state.app }))
vi.mock('@pinia/colada', async importOriginal => ({ ...await importOriginal<typeof import('@pinia/colada')>(), useQueryCache: () => ({}) }))

function setup() {
  const network = reactive({
    public_url: { value: 'http://astral.example:4545', saved_value: '', source: 'discovery', editable: true },
    allow_remote_management: { value: false, saved_value: false, source: 'default', editable: true },
  })
  const save = vi.fn(async (patch: { public_url?: string; allow_remote_management?: boolean }) => {
    if (patch.public_url !== undefined) {
      network.public_url.saved_value = patch.public_url
      network.public_url.value = patch.public_url || 'http://astral.example:4545'
    }
    if (patch.allow_remote_management !== undefined) network.allow_remote_management.value = patch.allow_remote_management
    return network
  })
  const refresh = vi.fn(async () => network)
  state.app = reactive({ data: { network }, networkSettings: save, refreshNetwork: refresh })
  return { editor: useNetworkSettings(), network, save, refresh }
}
afterEach(() => { delete useSettingsSave().drafts['network/public_url'] })

describe('network settings editor', () => {
  it('saves an independent access-address edit without enabling remote management', async () => {
    const { editor, save, network } = setup()
    editor.address.value = ' http://100.90.80.70:4545 '
    await editor.saveAddress()
    expect(save).toHaveBeenCalledWith({ public_url: 'http://100.90.80.70:4545' })
    expect(network.allow_remote_management.value).toBe(false)
    expect(editor.address.value).toBe('http://100.90.80.70:4545')
    editor.address.value = ''
    await editor.saveAddress()
    expect(save).toHaveBeenLastCalledWith({ public_url: '' })
    expect(network.public_url.value).toBe('http://astral.example:4545')
  })

  it('keeps a rejected address draft and restores the confirmed toggle state', async () => {
    const { editor, save, network } = setup()
    save.mockRejectedValue({ code: 'validation', message: 'Invalid address' })
    editor.address.value = 'invalid URL'
    await editor.saveAddress()
    expect(editor.address.value).toBe('invalid URL')
    expect(network.public_url.value).toBe('http://astral.example:4545')
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

  it('does not send edits for command-line or environment controlled fields', async () => {
    const { editor, network, save } = setup()
    network.public_url.editable = false
    network.public_url.source = 'environment'
    network.allow_remote_management.editable = false
    network.allow_remote_management.source = 'cli'
    expect(editor.address.value).toBe(network.public_url.value)
    await editor.saveAddress()
    await editor.setAllowRemote(true)
    expect(save).not.toHaveBeenCalled()
  })

  it('keeps newer address typing when an earlier save completes', async () => {
    const { editor, network, save } = setup()
    let finish!: () => void
    const pending = new Promise<void>(resolve => { finish = resolve })
    save.mockImplementationOnce(async patch => {
      await pending
      network.public_url.saved_value = patch.public_url!
      network.public_url.value = patch.public_url!
      return network
    })
    editor.address.value = 'http://first.example:4545'
    const saving = editor.saveAddress()
    await Promise.resolve()
    editor.address.value = 'http://newer.example:4545'
    finish()
    await saving
    expect(editor.address.value).toBe('http://newer.example:4545')
    expect(network.public_url.value).toBe('http://first.example:4545')
    await editor.saveAddress()
    expect(network.public_url.value).toBe('http://newer.example:4545')
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
    finish()
    await Promise.all([enabling, disabling])
    expect(refresh).toHaveBeenCalledOnce()
    expect(network.allow_remote_management.value).toBe(false)
    expect(editor.allowRemote.value).toBe(false)
    expect(save).toHaveBeenLastCalledWith({ allow_remote_management: false })
  })
})
