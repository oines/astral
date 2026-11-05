import type { Entry } from './types'
import type { AstralInternalDomainSubagentActivity } from '@astral/sdk'
import { object } from './api'

export interface SubagentReference {
  sessionID: string
  parentSessionID: string
  title: string
  status: string
}

export type SubagentActivity = AstralInternalDomainSubagentActivity

export function subagentActivityStatus(status: string) {
  if (status === 'started') return 'subagentStartedWorking'
  if (status === 'completed') return 'subagentCompletedWork'
  if (status === 'failed') return 'subagentWorkFailed'
  if (status === 'cancelled') return 'subagentWorkStopped'
}

export function spawnedSubagent(entry: Entry): SubagentReference | undefined {
  const tool = entry.tool
  if (tool?.invocation.call.name !== 'spawn_agent' || tool.result?.outcome !== 'succeeded') return
  const result = object(tool.result.details)
  if (typeof result.session_id !== 'string' || !result.session_id) return
  return {
    sessionID: result.session_id,
    parentSessionID: typeof result.parent_session_id === 'string' ? result.parent_session_id : tool.invocation.session_id,
    title: typeof result.name === 'string' && result.name.trim() ? result.name : typeof result.title === 'string' ? result.title : '',
    status: typeof result.status === 'string' ? result.status : '',
  }
}

export function subagentStatus(status?: string) {
  if (status === 'running') return 'running'
  if (status === 'stopping') return 'stopping'
  if (status === 'compacting') return 'compacting'
  if (status === 'failed' || status === 'error') return 'subagentFailed'
  if (status === 'needs_attention') return 'subagentNeedsAttention'
  if (status === 'queued' || status === 'waiting') return 'subagentQueued'
  return 'subagentIdle'
}
