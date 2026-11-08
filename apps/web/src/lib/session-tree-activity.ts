import type { Session } from './types'

type SessionWithStatus = Session & { status?: string }
const activeStatuses = new Set(['running', 'stopping', 'compacting'])

// One directory pass plus an iterative upward walk visits every eligible node
// at most once. Fork history never contributes to delegation-tree activity.
export function activeSessionTreeIDs(sessions: readonly Session[], statuses: Readonly<Record<string, { status?: string } | undefined>>): Set<string> {
  const eligible = new Map<string, Session>()
  const active = new Set<string>()
  const pending: Session[] = []
  for (const session of sessions) {
    if (session.archived || session.deletion) continue
    eligible.set(session.id, session)
    const status = statuses[session.id]?.status ?? (session as SessionWithStatus).status
    if (status && activeStatuses.has(status) && !active.has(session.id)) {
      active.add(session.id)
      pending.push(session)
    }
  }
  for (let index = 0; index < pending.length; index++) {
    const child = pending[index]!
    if (!child.subagent_parent_id) continue
    const parent = eligible.get(child.subagent_parent_id)
    if (!parent || parent.agent_id !== child.agent_id || active.has(parent.id)) continue
    active.add(parent.id)
    pending.push(parent)
  }
  return active
}
