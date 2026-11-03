import type { Session } from './types'

type Relationship = Pick<Session, 'id' | 'agent_id' | 'subagent_parent_id'>

export function createSessionCatalog() {
  const known = new Map<string, Relationship>()
  let listed = new Set<string>()
  function remember(sessions: Iterable<Relationship>) {
    for (const session of sessions) known.set(session.id, { id: session.id, agent_id: session.agent_id, subagent_parent_id: session.subagent_parent_id || known.get(session.id)?.subagent_parent_id })
  }
  return {
    remember,
    observe(sessions: Relationship[]) {
      const next = new Set(sessions.map(session => session.id))
      const removed = [...listed].filter(id => !next.has(id))
      // Advance before the caller clears query data; a local cleanup cannot
      // make the same catalog removal recursively announce itself.
      listed = next
      remember(sessions)
      return removed
    },
    tree(id: string) {
      const ids = new Set([id])
      let added = true
      while (added) {
        added = false
        for (const session of known.values()) if (session.subagent_parent_id && ids.has(session.subagent_parent_id) && !ids.has(session.id)) {
          ids.add(session.id)
          added = true
        }
      }
      return ids
    },
    agent(id: string) { return [...known.values()].filter(session => session.agent_id === id).map(session => session.id) },
    forget(ids: Iterable<string>) { for (const id of ids) { known.delete(id); listed.delete(id) } },
    clear() { known.clear(); listed.clear() },
  }
}
