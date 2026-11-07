import type { AstralInternalDomainSubagentActivity } from '@astral/sdk'

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

export function subagentStatus(status?: string) {
  if (status === 'running') return 'running'
  if (status === 'stopping') return 'stopping'
  if (status === 'compacting') return 'compacting'
  if (status === 'failed' || status === 'error') return 'subagentFailed'
  if (status === 'needs_attention') return 'subagentNeedsAttention'
  if (status === 'queued' || status === 'waiting') return 'subagentQueued'
  return 'subagentIdle'
}
