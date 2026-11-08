import { describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import SessionAutomations from './SessionAutomations.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
describe('static sidebar automation markers', () => {
  it('shows both type markers without an extra button or popover', async () => {
    const html = await renderToString(createSSRApp(SessionAutomations, { items: { timed: [{ id: 'schedule', title: 'Daily', kind: 'schedule' }], events: [{ id: 'event', title: 'Build', kind: 'event' }] } }))
    expect(html).toContain('scheduleTimedTasks')
    expect(html).toContain('wakeEndpoints')
    expect(html).not.toContain('<button')
    expect(html).not.toContain('aria-haspopup')
    expect(html).not.toContain('Daily')
    expect(html).not.toContain('Build')
  })
})
