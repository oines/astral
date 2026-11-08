/* eslint-disable vue/one-component-per-file -- Shared controls are isolated SSR stubs. */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, getCurrentInstance, h, ref, type Ref } from 'vue'
import { renderToString } from 'vue/server-renderer'
import ComputersSettings from './ComputersSettings.vue'
import type { Computer } from '../../lib/types'

interface Setup { edit: (computer: Computer) => void; confirmRemoval: () => void; remove: () => Promise<void>; revoke: () => Promise<void>; removing: boolean }
const fixture = vi.hoisted(() => ({
  setup: undefined as Setup | undefined,
  list: undefined as unknown as { data: Ref<Computer[]> },
  app: { agents: [], refresh: vi.fn(async () => {}) },
  remove: vi.fn(async () => ({ data: { ok: true } })), revoke: vi.fn(async () => ({ data: { ok: true } })), forgetComputer: vi.fn(), cache: vi.fn(),
}))
vi.mock('../../stores/app', () => ({ useAppStore: () => fixture.app }))
vi.mock('../../stores/wake-endpoints', () => ({ useWakeEndpointStore: () => ({ forgetComputer: fixture.forgetComputer }) }))
vi.mock('../../composables/useSettingsSave', () => ({ useSettingsSave: () => ({}) }))
vi.mock('@astral/sdk', async importOriginal => ({ ...await importOriginal<object>(), deleteComputer: fixture.remove, revokeComputer: fixture.revoke }))
vi.mock('@astral/sdk/colada', () => ({ listComputersQuery: () => ({}), listComputersQueryKey: () => ['computers'] }))
vi.mock('@pinia/colada', () => ({ useQuery: () => fixture.list, useQueryCache: () => ({ setQueryData: fixture.cache }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('./ResourceDelete.vue', () => ({ default: { render: () => null } }))
vi.mock('./AddComputerDialog.vue', () => ({ default: { render: () => null } }))
vi.mock('./ComputerMaintenance.vue', () => ({ default: { render: () => null } }))
vi.mock('./SettingsText.vue', () => ({ default: { render: () => null } }))
vi.mock('./SettingsFeedback.vue', () => ({ default: { render: () => null } }))
vi.mock('../CopyValue.vue', () => ({ default: { render: () => null } }))
vi.mock('@astral/ui', () => ({
  Button: defineComponent({ setup(_props, { slots, attrs }) {
    let parent = getCurrentInstance()!.parent
    while (!fixture.setup && parent) {
      const state = (parent as unknown as { setupState: Setup }).setupState
      if (typeof state.confirmRemoval === 'function') fixture.setup = state
      parent = parent.parent
    }
    return () => h('button', attrs, slots.default?.())
  } }),
  Modal: defineComponent({ setup() { return () => null } }),
  SettingsSection: defineComponent({ setup(_props, { slots }) { return () => h('section', slots.default?.()) } }),
  SettingsRow: defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()) } }),
}))
const computer = { id: 'retired', revision: 3, name: 'Retired Computer', revoked: true, online: false, kind: 'remote', created_at: '', updated_at: '' } as Computer
beforeEach(() => {
  fixture.setup = undefined; fixture.list = { data: ref([computer]) }
  fixture.app.refresh.mockReset().mockResolvedValue(undefined)
  fixture.remove.mockReset().mockResolvedValue({ data: { ok: true } })
  fixture.revoke.mockReset().mockResolvedValue({ data: { ok: true } })
  fixture.forgetComputer.mockClear(); fixture.cache.mockClear()
})
async function render() { await renderToString(createSSRApp(ComputersSettings)); fixture.setup!.edit(computer) }

describe('permanent Computer removal updates the shared endpoint directory', () => {
  it('forgets the deleted Computer immediately after confirmed deletion even if later catalog refresh fails', async () => {
    await render(); fixture.setup!.confirmRemoval()
    fixture.app.refresh.mockRejectedValueOnce(new Error('Catalog unavailable'))
    await fixture.setup!.remove()
    expect(fixture.remove).toHaveBeenCalledWith({ path: { id: computer.id }, query: { revision: computer.revision } })
    expect(fixture.forgetComputer).toHaveBeenCalledWith(computer.id)
    expect(fixture.cache).toHaveBeenCalledWith(['computers'], [])
    expect(fixture.setup!.removing).toBe(false)
  })
  it('keeps endpoints and the confirmation when deletion fails', async () => {
    await render(); fixture.setup!.confirmRemoval()
    fixture.remove.mockRejectedValueOnce(new Error('Deletion failed'))
    await fixture.setup!.remove()
    expect(fixture.forgetComputer).not.toHaveBeenCalled()
    expect(fixture.cache).not.toHaveBeenCalled()
    expect(fixture.setup!.removing).toBe(true)
  })
  it('does not remove endpoints on connection revocation', async () => {
    await render(); await fixture.setup!.revoke()
    expect(fixture.revoke).toHaveBeenCalledOnce()
    expect(fixture.remove).not.toHaveBeenCalled()
    expect(fixture.forgetComputer).not.toHaveBeenCalled()
  })
})
