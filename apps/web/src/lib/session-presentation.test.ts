import { describe, expect, it } from 'vitest'
import { sessionWithMetadata } from './session-presentation'
import type { SessionView } from './types'

const live: SessionView = { id: 'session', agent_id: 'bot', title: 'Old title', revision: 1, created_at: '', updated_at: '', archived: false, pinned: false, source: 'im', binding_id: 'binding', conversation_id: 'chat', channel_platform: 'telegram', rollout_id: 'rollout', head_id: 'head', status: 'running', run_id: 'run', event_sequence: 3, can_continue: false, model_available: true, selection: { id: 'model' }, approvals: [], pending: [], unresolved: [] }
describe('catalog and live session presentation', () => {
  it('applies Gateway metadata updates without discarding current run progress or retaining a removed binding', () => {
    const catalog = { ...live, title: 'New title', revision: 2, archived: true, binding_id: undefined, conversation_id: undefined, channel_platform: undefined, status: 'idle', run_id: undefined, event_sequence: 1 }
    expect(sessionWithMetadata(catalog, live)).toMatchObject({ title: 'New title', archived: true, binding_id: undefined, conversation_id: undefined, channel_platform: undefined, status: 'running', run_id: 'run', event_sequence: 3 })
  })
  it('keeps an even newer SSE metadata change until the catalog catches up', () => {
    const newer = { ...live, revision: 3, title: 'Newest' }
    expect(sessionWithMetadata({ ...live, revision: 2 }, newer).title).toBe('Newest')
  })
})
