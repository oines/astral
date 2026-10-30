/* eslint-disable vue/one-component-per-file -- Shared menu primitives are isolated test stubs. */
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { Session } from '../lib/types'
import SessionMenu from './SessionMenu.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@astral/ui', () => ({
  Menu: defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()) } }),
  MenuItem: defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()) } }),
  Button: defineComponent({ setup(_props, { slots }) { return () => h('button', slots.default?.()) } }),
}))
const session: Session = { id: 'session', agent_id: 'bot', title: 'Work', archived: false, pinned: false, source: 'web', revision: 1, created_at: '', updated_at: '' }
describe('bound IM conversation actions', () => {
  it('hides rename and archive while preserving ordinary conversation actions', async () => {
    const html = await renderToString(createSSRApp(SessionMenu, { session: { ...session, binding_id: 'binding' }, busy: false }))
    expect(html).not.toContain('rename')
    expect(html).not.toContain('archive')
    expect(html).toContain('pin')
    expect(html).toContain('export')
    expect(html).toContain('makeSessionPrivate')
  })
  it('offers rename and archive after an IM conversation is unbound', async () => {
    const html = await renderToString(createSSRApp(SessionMenu, { session: { ...session, source: 'im' }, busy: false }))
    expect(html).toContain('rename')
    expect(html).toContain('archive')
  })
  it('allows the owner to turn private mode off on an IM-bound session', async () => {
    const html = await renderToString(createSSRApp(SessionMenu, { session: { ...session, private: true, binding_id: 'binding' }, busy: false }))
    expect(html).toContain('makeSessionNonPrivate')
    expect(html).not.toContain('makeSessionPrivate')
    expect(html).not.toContain('archive')
  })
})
