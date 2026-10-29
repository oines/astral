import type { Session } from './types'
import { hasScheduledSession, type Schedule } from './schedules'

export const platformGroups = ['telegram', 'qq', 'weixin', 'feishu'] as const
export function platformGroup(platform?: string) {
  if (!platform) return undefined
  return platformGroups.find(value => platform === value || platform.startsWith(`${value}.`))
}
export function visibleSidebarSessions(sessions: Session[], schedules: Schedule[], isActive: (session: Session) => boolean, selectedID = '') {
  const planned = new Set(schedules.filter(hasScheduledSession).map(schedule => schedule.session_id))
  return sessions.filter(session => !session.archived || session.id === selectedID || isActive(session) || planned.has(session.id))
}
export function sidebarGroups(sessions: Session[], schedules: Schedule[], isActive: (session: Session) => boolean) {
  const planned = new Set(schedules.filter(hasScheduledSession).map(schedule => schedule.session_id))
  const groups = ['now', 'planned', ...platformGroups.map(platform => `platform_${platform}`), 'pinned', 'recent'].map(title => ({ title, items: [] as Session[] }))
  for (const session of sessions) {
    const platform = session.binding_id ? platformGroup(session.channel_platform) : undefined
    const title = isActive(session) ? 'now' : planned.has(session.id) ? 'planned' : platform ? `platform_${platform}` : session.pinned ? 'pinned' : 'recent'
    groups.find(group => group.title === title)!.items.push(session)
  }
  return groups.filter(group => group.items.length)
}
