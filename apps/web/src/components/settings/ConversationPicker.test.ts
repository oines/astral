/* eslint-disable vue/one-component-per-file -- Shared UI primitives are isolated SSR stubs. */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, getCurrentInstance, h, reactive, ref, type Ref } from 'vue'
import { renderToString } from 'vue/server-renderer'
import ConversationPicker from './ConversationPicker.vue'

interface PickerState { sessions: Array<{ id: string }>; selectedConversationID: string; sessionID: string; save: () => Promise<void> }
interface Query { data: Ref<Array<Record<string, unknown>>>; refetch: () => Promise<void> }
const fixture = vi.hoisted(() => ({
  app: undefined as unknown as { sessions: Array<{ id: string; agent_id: string; binding_id?: string; archived: boolean; title: string }>; refresh: () => Promise<void> },
  queries: {} as Record<string, Query>, action: undefined as unknown as { busy: Ref<boolean>; error: Ref<string> },
  state: undefined as PickerState | undefined, bind: vi.fn(async () => ({ data: { ok: true } })),
}))
vi.mock('@astral/sdk/colada', () => ({ listConnectionsQuery: () => ({ kind: 'connections' }), listConversationsQuery: () => ({ kind: 'conversations' }), listBindingsQuery: () => ({ kind: 'bindings' }) }))
vi.mock('@pinia/colada', () => ({ useQuery: ({ kind }: { kind: string }) => fixture.queries[kind] }))
vi.mock('@astral/sdk', () => ({ bindConversation: fixture.bind, resolveConversation: vi.fn() }))
vi.mock('../../stores/app', () => ({ useAppStore: () => fixture.app }))
vi.mock('../../composables/useAction', () => ({ useAction: () => fixture.action }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@astral/ui', () => ({
  Modal: defineComponent({ setup(_props, { slots }) { fixture.state = (getCurrentInstance()!.parent! as unknown as { setupState: PickerState }).setupState; return () => h('div', slots.default?.()) } }),
  Button: defineComponent({ setup(_props, { slots, attrs }) { return () => h('button', attrs, slots.default?.()) } }),
  Select: defineComponent({ setup() { return () => h('select') } }),
}))
beforeEach(() => {
  fixture.app = reactive({ sessions: [{ id: 'available', agent_id: 'bot', archived: false, title: 'Available' }, { id: 'sealed', agent_id: 'bot', archived: true, title: 'Archived' }], refresh: vi.fn(async () => {}) })
  fixture.queries = {
    connections: { data: ref([{ id: 'connection', agent_id: 'bot', adapter: 'telegram.bot', capabilities: {} }]), refetch: vi.fn(async () => {}) },
    conversations: { data: ref([{ id: 'chat', connection_id: 'connection', title: 'Chat', kind: 'private' }]), refetch: vi.fn(async () => {}) },
    bindings: { data: ref([]), refetch: vi.fn(async () => {}) },
  }
  fixture.action = { busy: ref(false), error: ref('') }; fixture.state = undefined; fixture.bind.mockClear()
})
async function render() { return renderToString(createSSRApp(ConversationPicker, { open: true, agentID: 'bot', connectionID: 'connection', conversationID: 'chat' })) }

describe('chat binding targets respect session archival', () => {
  it('excludes archived sessions from target choices while retaining available sessions', async () => {
    await render()
    expect(fixture.state!.sessions.map(session => session.id)).toEqual(['available'])
  })
  it('blocks a selected target archived after the picker opened without implicitly restoring it', async () => {
    await render()
    fixture.state!.selectedConversationID = 'chat'; fixture.state!.sessionID = 'available'
    fixture.app.sessions[0]!.archived = true
    await fixture.state!.save()
    expect(fixture.bind).not.toHaveBeenCalled()
    expect(fixture.action.error.value).toBe('bindArchivedTarget')
    expect(fixture.app.refresh).not.toHaveBeenCalled()
  })
})
