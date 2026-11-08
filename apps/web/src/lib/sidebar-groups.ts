import type { Session } from './types'
import { hasScheduledSession, type Schedule } from './schedules'
import type { WakeEndpoint } from './wake-endpoints'

export interface SessionAutomation { id: string; title: string; kind: 'schedule' | 'event' }
export interface SessionAutomations { timed: SessionAutomation[]; events: SessionAutomation[] }
export function previewSessionAutomations(items: SessionAutomations, limit = 6) {
  const result: SessionAutomation[] = []
  for (let index = 0; result.length < limit && index < Math.max(items.timed.length, items.events.length); index++) {
    if (items.timed[index]) result.push(items.timed[index]!)
    if (items.events[index] && result.length < limit) result.push(items.events[index]!)
  }
  return result
}
export function indexSessionAutomations(schedules: Schedule[], endpoints: WakeEndpoint[]) {
  const index = new Map<string, SessionAutomations>()
  function add(sessionID: string, item: SessionAutomation) {
    let value = index.get(sessionID)
    if (!value) { value = { timed: [], events: [] }; index.set(sessionID, value) }
    value[item.kind === 'schedule' ? 'timed' : 'events'].push(item)
  }
  for (const schedule of schedules) if (hasScheduledSession(schedule)) add(schedule.session_id!, { id: schedule.id, title: schedule.title, kind: 'schedule' })
  for (const endpoint of endpoints) if (endpoint.enabled) add(endpoint.session_id, { id: endpoint.id, title: endpoint.title, kind: 'event' })
  return index
}

export const platformGroups = ['telegram', 'qq', 'weixin', 'feishu'] as const
export function platformGroup(platform?: string) {
  if (!platform) return undefined
  return platformGroups.find(value => platform === value || platform.startsWith(`${value}.`))
}
export function visibleSidebarSessions(sessions: Session[]) {
  return sessions.filter(session => !session.subagent_parent_id && !session.archived)
}
export function sidebarGroups(sessions: Session[], automations: ReadonlyMap<string, SessionAutomations>, isActive: (session: Session) => boolean) {
  const groups = ['now', 'planned', ...platformGroups.map(platform => `platform_${platform}`), 'pinned', 'recent'].map(title => ({ title, items: [] as Session[] }))
  for (const session of sessions) {
    if (session.subagent_parent_id || session.archived) continue
    const platform = session.binding_id ? platformGroup(session.channel_platform) : undefined
    const title = isActive(session) ? 'now' : automations.has(session.id) ? 'planned' : platform ? `platform_${platform}` : session.pinned ? 'pinned' : 'recent'
    groups.find(group => group.title === title)!.items.push(session)
  }
  return groups.filter(group => group.items.length)
}
