import type { SessionAutomation } from './sidebar-groups'

export interface AutomationTarget { agentID: string; kind: SessionAutomation['kind']; id: string }
export function automationLocation(agentID: string, entry: Pick<SessionAutomation, 'kind' | 'id'>) {
  return { path: '/schedules', query: { agent: agentID, [entry.kind === 'schedule' ? 'schedule' : 'endpoint']: entry.id } }
}
export function automationTarget(query: Record<string, unknown>): AutomationTarget | undefined {
  if (typeof query.agent !== 'string' || !query.agent) return
  if (typeof query.schedule === 'string' && query.schedule && query.endpoint === undefined) return { agentID: query.agent, kind: 'schedule', id: query.schedule }
  if (typeof query.endpoint === 'string' && query.endpoint && query.schedule === undefined) return { agentID: query.agent, kind: 'event', id: query.endpoint }
}
