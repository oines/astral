import { describe, expect, it } from 'vitest'
import { indexSessionAutomations, sidebarGroups as groupSessions, visibleSidebarSessions } from './sidebar-groups'
import type { Session } from './types'
import type { Schedule } from './schedules'
import type { WakeEndpoint } from './wake-endpoints'

const sidebarGroups = (sessions: Session[], schedules: Schedule[], active: (session: Session) => boolean, endpoints: WakeEndpoint[] = []) => groupSessions(sessions, indexSessionAutomations(schedules, endpoints), active)

function session(id: string, extra: Partial<Session> = {}): Session {
  return { id, agent_id: 'bot', title: id, source: 'web', archived: false, pinned: false, revision: 1, created_at: '', updated_at: '', ...extra }
}
function task(sessionID: string, extra: Partial<Schedule> = {}): Schedule {
  return { id: `task-${sessionID}`, agent_id: 'bot', session_mode: 'existing', session_id: sessionID, title: sessionID, prompt: 'Prompt', enabled: true, timing: { kind: 'cron', cron: '0 9 * * *' }, next_run_at: '2026-11-01T09:00:00Z', revision: 1, created_at: '', updated_at: '', ...extra }
}
describe('sidebar conversation groups', () => {
  it('hides every subagent even when selected, running or scheduled, while retaining ordinary forks', () => {
    const sessions = [
      session('root'),
      session('shared-child', { subagent_parent_id: 'root', parent_session_id: 'root' }),
      session('detached-child', { subagent_parent_id: 'root' }),
      session('nested-child', { subagent_parent_id: 'shared-child' }),
      session('fork', { parent_session_id: 'root' }),
    ]
    const plans = [task('detached-child')]
    const active = (value: Session) => value.id === 'shared-child'
    expect(visibleSidebarSessions(sessions).map(value => value.id)).toEqual(['root', 'fork'])
    expect(sidebarGroups(sessions, plans, active).flatMap(group => group.items.map(value => value.id))).toEqual(['root', 'fork'])
  })
  it('does not treat a new-session task or its latest generated session as a fixed planned conversation', () => {
    const tasks = [task('', { session_mode: 'new', session_id: undefined, last_session_id: 'latest' })]
    const sessions = [session('latest'), session('archived-latest', { archived: true })]
    const visible = visibleSidebarSessions(sessions)
    expect(visible.map(session => session.id)).toEqual(['latest'])
    expect(sidebarGroups(visible, tasks, () => false).map(group => [group.title, group.items.map(item => item.id)])).toEqual([['recent', ['latest']]])
  })
  it('hides sealed archives even when selected, running, pinned or scheduled', () => {
    const sessions = ['scheduled', 'running', 'selected', 'pinned'].map(id => session(id, { archived: true, pinned: true }))
    expect(visibleSidebarSessions(sessions)).toEqual([])
    expect(sidebarGroups(sessions, [task('scheduled')], value => value.id === 'running')).toEqual([])
  })
  it('marks enabled event endpoints as planned and retains both automation kinds while running', () => {
    const endpoint = (id: string, target: string, enabled = true): WakeEndpoint => ({ id, agent_id: 'bot', session_id: target, source_session_id: target, computer_id: 'pc', title: id, enabled, revision: 1, created_at: '', updated_at: '' })
    const endpoints = [endpoint('event-live', 'live'), { ...endpoint('event-only', 'event'), blocked_until: '2099-01-01T00:00:00Z' }, endpoint('disabled', 'paused', false)]
    const index = indexSessionAutomations([task('live'), task('ended', { next_run_at: undefined })], endpoints)
    expect(index.get('live')).toMatchObject({ timed: [{ title: 'live' }], events: [{ title: 'event-live' }] })
    expect(index.has('ended')).toBe(false)
    expect(index.has('paused')).toBe(false)
    expect(groupSessions([session('live'), session('event'), session('paused')], index, value => value.id === 'live').map(group => [group.title, group.items.map(item => item.id)])).toEqual([['now', ['live']], ['planned', ['event']], ['recent', ['paused']]])
  })
  it('gives running and planned sessions priority without duplicating any row', () => {
    const sessions = [session('live', { binding_id: 'binding', channel_platform: 'telegram', pinned: true }), session('daily', { binding_id: 'other', channel_platform: 'qq', pinned: true }), session('saved', { pinned: true }), session('web')]
    const groups = sidebarGroups(sessions, [task('live'), task('daily')], session => session.id === 'live')
    expect(groups.map(group => [group.title, group.items.map(item => item.id)])).toEqual([
      ['now', ['live']], ['planned', ['daily']], ['pinned', ['saved']], ['recent', ['web']],
    ])
    expect(new Set(groups.flatMap(group => group.items.map(item => item.id))).size).toBe(sessions.length)
  })
  it('groups only current bindings by platform, combining both QQ adapters and omitting empty groups', () => {
    const groups = sidebarGroups([
      session('napcat', { binding_id: '1', channel_platform: 'qq.napcat', pinned: true }),
      session('official', { binding_id: '2', channel_platform: 'qq.bot' }),
      session('telegram', { binding_id: '3', channel_platform: 'telegram' }),
      session('weixin', { binding_id: '4', channel_platform: 'weixin' }),
      session('feishu', { binding_id: '5', channel_platform: 'feishu' }),
      session('unbound', { source: 'im', channel_platform: 'telegram' }),
    ], [], () => false)
    expect(groups.map(group => [group.title, group.items.map(item => item.id)])).toEqual([
      ['platform_telegram', ['telegram']], ['platform_qq', ['napcat', 'official']], ['platform_weixin', ['weixin']], ['platform_feishu', ['feishu']], ['recent', ['unbound']],
    ])
    expect(sidebarGroups([], [], () => false)).toEqual([])
  })
  it('does not keep paused or completed plans in the scheduled group', () => {
    const groups = sidebarGroups([session('paused'), session('ended'), session('next')], [task('paused', { enabled: false }), task('ended', { next_run_at: undefined, last_status: 'delivered' }), task('next')], () => false)
    expect(groups.map(group => [group.title, group.items.map(item => item.id)])).toEqual([['planned', ['next']], ['recent', ['paused', 'ended']]])
  })
})
