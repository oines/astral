/* eslint-disable vue/one-component-per-file -- Shared controls are isolated SSR test stubs. */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, getCurrentInstance, h, reactive, ref, type Ref } from 'vue'
import { renderToString } from 'vue/server-renderer'
import WakeEndpointsPanel from './WakeEndpointsPanel.vue'
import type { WakeEndpoint, WakePatch } from '../lib/wake-endpoints'

interface Setup {
  editor?: WakeEndpoint
  draft: { title: string; prompt: string; notes: string; enabled: boolean }
  editorError: string
  begin: (endpoint: WakeEndpoint) => void
  save: () => Promise<void>
  toggle: (endpoint: WakeEndpoint, enabled: boolean) => Promise<void>
  remove: (endpoint: WakeEndpoint) => Promise<void>
  copyExample: () => Promise<void>
}
interface App {
  activeAgentID: string; authorized: boolean; displayTimezone: string
  sessions: Array<{ id: string; title: string; agent_id: string; subagent_parent_id?: string }>
  computers: Array<{ id: string; name: string }>
}
const fixture = vi.hoisted(() => ({
  setup: undefined as Setup | undefined,
  app: undefined as unknown as App,
  feed: undefined as unknown as { items: Ref<WakeEndpoint[]>; ready: Ref<boolean>; error: Ref<string>; update: ReturnType<typeof vi.fn>; remove: ReturnType<typeof vi.fn>; refresh: ReturnType<typeof vi.fn> },
  copy: vi.fn(async (_text: string) => true),
}))
vi.mock('../stores/app', () => ({ useAppStore: () => fixture.app }))
vi.mock('../composables/useWakeEndpoints', () => ({ useWakeEndpoints: () => fixture.feed }))
vi.mock('../composables/useClipboard', () => ({ useClipboard: () => ({ copy: fixture.copy, copied: false }) }))
vi.mock('@vueuse/core', () => ({ useNow: () => ref(new Date('2026-11-08T03:00:00Z')) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values: Record<string, unknown> = {}) => `${key}${Object.values(values).length ? ` ${Object.values(values).join(' ')}` : ''}`, locale: { value: 'en' } }) }))
vi.mock('@astral/ui', () => ({
  Button: defineComponent({ setup(_props, { slots, attrs }) {
    let parent = getCurrentInstance()!.parent
    while (!fixture.setup && parent) {
      const state = (parent as unknown as { setupState: Setup }).setupState
      if (typeof state.begin === 'function') fixture.setup = state
      parent = parent.parent
    }
    return () => h('button', attrs, slots.default?.())
  } }),
  SettingsSection: defineComponent({ props: { title: { type: String, default: '' } }, setup(props, { slots }) { return () => h('section', [h('h2', props.title), slots.default?.()]) } }),
  SettingsRow: defineComponent({ props: { label: { type: String, default: '' } }, setup(props, { slots }) { return () => h('div', [h('label', props.label), slots.default?.()]) } }),
  ConfirmPopover: defineComponent({ setup(_props, { slots }) { return () => h('div', slots.trigger?.()) } }),
  Switch: defineComponent({ setup() { return () => h('button') } }),
  useDelayedPending: () => false,
}))
const endpoint: WakeEndpoint = { id: 'wake-mc', agent_id: 'bot', computer_id: 'computer-mc', session_id: 'session', source_session_id: 'session', title: 'Minecraft events', prompt: 'Review the event', notes: '/opt/mc/listener.py, systemd: mc-events', enabled: true, trigger_url: 'http://127.0.0.1:57200/hooks/wake-mc', revision: 4, created_at: '2026-11-08T01:00:00Z', updated_at: '2026-11-08T01:00:00Z' }
async function render() {
  const app = createSSRApp(WakeEndpointsPanel)
  app.component('RouterLink', defineComponent({ setup(_props, { slots }) { return () => h('a', slots.default?.()) } }))
  return renderToString(app)
}
beforeEach(() => {
  fixture.setup = undefined
  fixture.app = reactive({ activeAgentID: 'bot', authorized: true, displayTimezone: 'UTC', sessions: [{ id: 'session', agent_id: 'bot', title: 'Minecraft conversation' }, { id: 'child', agent_id: 'bot', title: 'Subagent', subagent_parent_id: 'session' }], computers: [{ id: 'computer-mc', name: 'Minecraft host' }] })
  fixture.feed = { items: ref([endpoint]), ready: ref(true), error: ref(''), update: vi.fn(async (current: WakeEndpoint, patch: WakePatch) => ({ ...current, ...patch, revision: current.revision + 1 })), remove: vi.fn(async () => {}), refresh: vi.fn(async () => {}) }
  vi.clearAllMocks()
})

describe('Agent-created event endpoint management', () => {
  it('lists endpoints and temporary protection separately from manual pause without exposing create or credential controls', async () => {
    fixture.feed.items.value = [{ ...endpoint, blocked_until: '2026-11-08T03:01:00Z', last_received_at: '2026-11-08T02:59:59Z' }, { ...endpoint, id: 'other', agent_id: 'other-bot', title: 'Another Bot' }]
    const html = await render()
    expect(html).toContain('Minecraft events')
    expect(html).toContain('Minecraft conversation')
    expect(html).toContain('wakeBlocked')
    expect(html).not.toContain('wakePaused')
    expect(html).not.toContain('Another Bot')
    expect(html).not.toMatch(/wakeCreate|wakeRotate|wakeCredential|wakeReveal/)
    expect(fixture.feed.update).not.toHaveBeenCalled()
  })

  it('edits only mutable fields and preserves independent maintenance notes', async () => {
    await render()
    fixture.setup!.begin(endpoint)
    Object.assign(fixture.setup!.draft, { title: 'MC console', prompt: 'Inspect errors', notes: '/srv/mc/events.js', enabled: false })
    await fixture.setup!.save()
    expect(fixture.feed.update).toHaveBeenCalledWith(endpoint, { title: 'MC console', prompt: 'Inspect errors', notes: '/srv/mc/events.js', enabled: false })
    expect(fixture.setup!.editor).toBeUndefined()
  })

  it('pauses without changing the fixed target or receiving Computer', async () => {
    await render()
    await fixture.setup!.toggle(endpoint, false)
    expect(fixture.feed.update).toHaveBeenCalledWith(endpoint, { enabled: false })
  })

  it('keeps a conflicting draft and refreshes the latest directory instead of overwriting', async () => {
    await render()
    fixture.setup!.begin(endpoint)
    fixture.setup!.draft.notes = 'Keep this draft'
    fixture.feed.update.mockRejectedValueOnce({ code: 'conflict', message: 'Changed elsewhere' })
    await fixture.setup!.save()
    expect(fixture.feed.update).toHaveBeenCalledOnce()
    expect(fixture.feed.refresh).toHaveBeenCalledOnce()
    expect(fixture.setup!.editor).toBeDefined()
    expect(fixture.setup!.draft.notes).toBe('Keep this draft')
    expect(fixture.setup!.editorError).toBe('wakeConflict')
  })

  it('does not save an old Bot’s editor after switching Bots', async () => {
    await render()
    fixture.setup!.begin(endpoint)
    fixture.app.activeAgentID = 'other-bot'
    await fixture.setup!.save()
    expect(fixture.feed.update).not.toHaveBeenCalled()
    expect(fixture.setup!.editor?.agent_id).toBe('bot')
  })

  it('copies a local anonymous request example with a fresh event ID and no authorization header', async () => {
    await render()
    fixture.setup!.begin(endpoint)
    await fixture.setup!.copyExample()
    const text = fixture.copy.mock.calls[0]![0]
    expect(text).toContain(endpoint.trigger_url)
    expect(text).toContain('event_id')
    expect(text).toContain('wakeExampleText')
    expect(text).not.toContain('Authorization')
    expect(text).not.toContain('ASTRAL_API_TOKEN')
  })

})
