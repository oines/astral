import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, getCurrentInstance, ref } from 'vue'
import { renderToString } from 'vue/server-renderer'
import AddComputerDialog from './AddComputerDialog.vue'

interface Setup {
  chooseMode: (mode: 'local' | 'container' | 'remote') => void
  local: () => Promise<void>
  localContainer: () => Promise<void>
  remote: () => Promise<void>
  localAPIEnabled: boolean
  localAPIDisabled: boolean
  containerAPIEnabled: boolean
  remoteAPIEnabled: boolean
  serverAddress: string
}

const fixture = vi.hoisted(() => ({
  setup: undefined as Setup | undefined,
  createLocalContainer: vi.fn(),
  enrollComputer: vi.fn(),
  getComputerOperation: vi.fn(),
  useLocalComputer: vi.fn(),
  computers: [] as { id: string; local: boolean; revoked: boolean; computer_api_disabled?: boolean }[],
}))
vi.mock('@astral/sdk', async importOriginal => ({
  ...await importOriginal<object>(),
  createLocalContainer: fixture.createLocalContainer,
  enrollComputer: fixture.enrollComputer,
  getComputerOperation: fixture.getComputerOperation,
  useLocalComputer: fixture.useLocalComputer,
}))
vi.mock('@astral/sdk/colada', () => ({ listComputersQuery: () => ({}) }))
vi.mock('@pinia/colada', () => ({ useQuery: () => ({ data: ref(fixture.computers), refetch: async () => {} }) }))
vi.mock('../../stores/app', () => ({ useAppStore: () => ({ refresh: async () => {} }) }))
vi.mock('../../composables/useClipboard', () => ({ useClipboard: () => ({ copied: ref(false) }) }))
vi.mock('../../composables/useContainerRuntimeCheck', () => ({ useContainerRuntimeCheck: () => ({ check: async () => {} }) }))
vi.mock('@vueuse/core', () => ({ useIntervalFn: () => {} }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@astral/ui', () => ({
  Button: {}, SettingsRow: {}, SettingsSection: {}, Switch: {},
  Modal: defineComponent({ setup() {
    fixture.setup = (getCurrentInstance()!.parent as unknown as { setupState: Setup }).setupState
    return () => null
  } }),
}))

beforeEach(() => {
  fixture.setup = undefined
  fixture.computers = []
  fixture.createLocalContainer.mockReset().mockResolvedValue({ data: { id: 'operation', state: 'starting' } })
  fixture.enrollComputer.mockReset().mockResolvedValue({ data: { enrollment: { computer_api_disabled: true } } })
  fixture.getComputerOperation.mockReset()
  fixture.useLocalComputer.mockReset().mockResolvedValue({ data: { id: 'local' } })
  vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => {} })
})
afterEach(() => vi.unstubAllGlobals())

async function dialog() {
  await renderToString(createSSRApp(AddComputerDialog, { open: true }))
  return fixture.setup!
}

describe('adding a Computer configures Astral API before connecting', () => {
  it('defaults to enabled on the Server machine and its local container', async () => {
    const setup = await dialog()
    setup.chooseMode('local')
    expect(fixture.useLocalComputer).not.toHaveBeenCalled()
    await setup.local()
    expect(fixture.useLocalComputer).toHaveBeenCalledWith({ query: { computer_api_disabled: false } })
    setup.chooseMode('container')
    expect(fixture.createLocalContainer).not.toHaveBeenCalled()
    await setup.localContainer()
    expect(fixture.createLocalContainer).toHaveBeenCalledWith({ body: { id: expect.any(String), computer_api_disabled: false } })
  })

  it('reuses the first local container request and policy when retrying', async () => {
    const setup = await dialog()
    setup.chooseMode('container')
    setup.containerAPIEnabled = false
    fixture.createLocalContainer.mockRejectedValueOnce(new Error('Response lost'))
    await setup.localContainer()
    setup.containerAPIEnabled = true
    await setup.localContainer()
    const first = fixture.createLocalContainer.mock.calls[0]?.[0]
    expect(first).toEqual({ body: { id: expect.any(String), computer_api_disabled: true } })
    expect(fixture.createLocalContainer).toHaveBeenNthCalledWith(2, first)
  })

  it('uses the saved policy when the Server machine already exists', async () => {
    fixture.computers = [{ id: 'local', local: true, revoked: false, computer_api_disabled: true }]
    const setup = await dialog()
    setup.chooseMode('local')
    expect(setup.localAPIEnabled).toBe(true)
    expect(setup.localAPIDisabled).toBe(true)
    await setup.local()
    expect(fixture.useLocalComputer).toHaveBeenCalledWith({ query: { computer_api_disabled: true } })
  })

  it('retries a restored local container with the provisioning policy from the Server', async () => {
    vi.stubGlobal('localStorage', { getItem: () => JSON.stringify({ id: 'operation', intent: 'original' }), setItem: () => {} })
    fixture.getComputerOperation.mockResolvedValue({ data: { id: 'operation', state: 'failed', computer_api_disabled: true } })
    const setup = await dialog()
    await setup.localContainer()
    expect(fixture.createLocalContainer).toHaveBeenCalledWith({ body: { id: 'original', computer_api_disabled: true } })
  })

  it('defaults remote commands to disabled and displays the enrollment policy', async () => {
    const setup = await dialog()
    setup.chooseMode('remote')
    setup.serverAddress = 'https://astral.example'
    await setup.remote()
    expect(fixture.enrollComputer).toHaveBeenCalledWith({ body: { container: false, server_url: 'https://astral.example', computer_api_disabled: true } })
    expect(setup.remoteAPIEnabled).toBe(false)
    setup.remoteAPIEnabled = true
    fixture.enrollComputer.mockResolvedValueOnce({ data: { enrollment: {} } })
    await setup.remote()
    expect(fixture.enrollComputer).toHaveBeenLastCalledWith({ body: { container: false, server_url: 'https://astral.example', computer_api_disabled: false } })
    expect(setup.remoteAPIEnabled).toBe(true)
  })
})
