import type { Session } from './types'

// Catalog metadata can change through Gateway calls while an opened Session
// keeps a live runtime view. Never let that cached view hide a newer title,
// archive flag or removed IM binding; runtime progress still comes from SSE.
export function sessionWithMetadata<T extends Session>(catalog: T, live?: T): T {
  if (!live) return catalog
  const source = catalog.revision >= live.revision ? catalog : live
  return {
    ...live,
    id: source.id, agent_id: source.agent_id, revision: source.revision, created_at: source.created_at, updated_at: source.updated_at,
    title: source.title, title_source: source.title_source, source: source.source, pinned: source.pinned, archived: source.archived, private: source.private,
    binding_id: source.binding_id, conversation_id: source.conversation_id, channel_platform: source.channel_platform,
    computer_id: source.computer_id, cwd: source.cwd, deletion: source.deletion, parent_session_id: source.parent_session_id, fork_entry_id: source.fork_entry_id,
  }
}
