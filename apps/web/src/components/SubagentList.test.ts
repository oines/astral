import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, reactive } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createPinia } from 'pinia'
import SubagentList from './SubagentList.vue'
import type { SubagentActivity } from '../lib/subagents'

interface Child {
  id: string
  title: string
  subagent_parent_id?: string
  status?: string
  run_id?: string
  created_at: string
  updated_at: string
}
const fixture = vi.hoisted(() => ({ app: undefined as unknown as { sessions: Child[]; sessionViews: Record<string, Child>; deletedSessions: Set<string> } }))
vi.mock('../stores/app', () => ({ useAppStore: () => fixture.app }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const time = (minute: number) => new Date(Date.UTC(2026, 10, 8, 0, minute)).toISOString()
function child(id: string, status?: string, minute = 1, parent = 'root'): Child {
  return { id, title: id, status, subagent_parent_id: parent, created_at: time(0), updated_at: time(minute) }
}
function activity(id: string, minute: number, sequence: number, status: SubagentActivity['status'] = 'completed'): SubagentActivity {
  return { id: `${id}-${sequence}`, sequence, session_id: 'root', child_session_id: id, name: id, run_id: `${id}-run`, status, time: time(minute) }
}
async function render(activities: SubagentActivity[] = []) {
  return renderToString(createSSRApp(SubagentList, { rootID: 'root', activities: Object.fromEntries(activities.map(value => [value.id, value])) }).use(createPinia()))
}
function groupTitles(html: string, group: 'Running' | 'Ended') {
  const content = html.match(new RegExp(`<section[^>]*aria-label="workspaceSubagents${group}"[^>]*>([\\s\\S]*?)</section>`))?.[1] ?? ''
  return [...content.matchAll(/<strong[^>]*>([^<]*)<\/strong>/g)].map(match => match[1])
}
beforeEach(() => {
  fixture.app = reactive({ sessions: [], sessionViews: {}, deletedSessions: new Set<string>() })
})

describe('subagent list groups', () => {
  it('places running work first and sorts each group by recent activity, including failures among ended work', async () => {
    fixture.app.sessions = [child('older-running', 'running'), child('newer-running', 'running'), child('older-ended', 'idle'), child('newer-failed', 'failed'), child('middle-ended', 'idle')]
    const html = await render([
      activity('older-running', 10, 1, 'started'), activity('newer-running', 20, 2, 'started'),
      activity('older-ended', 30, 3), activity('newer-failed', 50, 4, 'failed'), activity('middle-ended', 40, 5, 'cancelled'),
    ])
    expect(html.indexOf('aria-label="workspaceSubagentsRunning"')).toBeLessThan(html.indexOf('aria-label="workspaceSubagentsEnded"'))
    expect(groupTitles(html, 'Running')).toEqual(['newer-running', 'older-running'])
    expect(groupTitles(html, 'Ended')).toEqual(['newer-failed', 'middle-ended', 'older-ended'])
    expect(html).toContain('subagentFailed')
  })

  it('uses source event time rather than delivery order or a later metadata edit', async () => {
    fixture.app.sessions = [child('late-old-start'), child('newer-ended', 'idle'), child('renamed-older', 'idle', 100)]
    const html = await render([
      activity('late-old-start', 30, 1), activity('late-old-start', 20, 9, 'started'),
      activity('newer-ended', 50, 3), activity('renamed-older', 40, 4),
    ])
    expect(groupTitles(html, 'Running')).toEqual([])
    expect(groupTitles(html, 'Ended')).toEqual(['newer-ended', 'renamed-older', 'late-old-start'])
  })

  it('preserves submillisecond precision for current status and recency order', async () => {
    fixture.app.sessions = [child('earlier-ended'), child('later-ended')]
    const html = await render([
      { ...activity('earlier-ended', 0, 1), time: '2026-11-08T00:00:00.123100Z' },
      { ...activity('later-ended', 0, 2), time: '2026-11-08T08:00:00.123900001+08:00' },
      { ...activity('later-ended', 0, 99, 'started'), time: '2026-11-08T00:00:00.123900000Z' },
    ])
    expect(groupTitles(html, 'Running')).toEqual([])
    expect(groupTitles(html, 'Ended')).toEqual(['later-ended', 'earlier-ended'])
  })

  it('keeps work that is stopping, compacting, queued or awaiting approval with the running group', async () => {
    fixture.app.sessions = [child('stopping', 'stopping', 10), child('compacting', 'compacting', 20), child('queued', 'queued', 30), child('approval', 'needs_attention', 40), child('failed', 'needs_attention', 50)]
    fixture.app.sessionViews.approval = { ...child('approval', 'needs_attention', 40), run_id: 'live-run' }
    const html = await render()
    expect(groupTitles(html, 'Running')).toEqual(['approval', 'queued', 'compacting', 'stopping'])
    expect(groupTitles(html, 'Ended')).toEqual(['failed'])
  })

  it('keeps descendant ownership and deletion filtering while falling back to session timestamps', async () => {
    fixture.app.sessions = [child('direct', 'idle', 10), child('descendant', 'running', 20, 'direct'), child('foreign', 'running', 50, 'other-root'), child('deleted', 'running', 60)]
    fixture.app.deletedSessions.add('deleted')
    const html = await render()
    expect(groupTitles(html, 'Running')).toEqual(['descendant'])
    expect(groupTitles(html, 'Ended')).toEqual(['direct'])
    expect(html).not.toContain('<strong>foreign</strong>')
    expect(html).not.toContain('<strong>deleted</strong>')
  })

  it('shows the existing empty state without empty group headings', async () => {
    const html = await render()
    expect(html).toContain('workspaceNoSubagents')
    expect(html).not.toMatch(/<h3(?:\s|>)/)
  })
})
