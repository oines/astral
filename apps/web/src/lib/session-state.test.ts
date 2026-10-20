import { describe, expect, it } from 'vitest'
import { addEntry, applyEvent, emptyState, fromSnapshot, messageText, transcriptRows, visibleEntry } from './session-state'
import type { CoreEvent, Entry, SessionView, Snapshot } from './types'

function entry(id: string, sequence: number, rest: Partial<Entry>): Entry {
  return { id, sequence, kind: 'message', session_id: 'session', version: 7, time: '2026-10-21T00:00:00Z', ...rest }
}
const view: SessionView = {
  id: 'session', agent_id: 'agent', revision: 1, created_at: '', updated_at: '', title: 'Work',
  archived: false, pinned: false, source: 'web', rollout_id: 'rollout', head_id: 'head',
  status: 'running', run_id: 'run', options: {}, approvals: [], pending: [], unresolved: [],
}
function snapshot(history: Entry[] = [], trace: Entry[] = []): Snapshot {
  return { epoch: 'server-a', view, history: { entries: history, head_id: 'head' }, trace: { entries: trace, head_id: 'head' } }
}
function event(sequence: number, rest: Partial<CoreEvent>) {
  return { epoch: 'server-a', core: { sequence, session_id: 'session', run_id: 'run', type: 'message_update', attempt_id: 'attempt', ...rest } }
}

describe('reconciling live previews with durable history', () => {
  it('discards failed text and reasoning, tolerates buffered late deltas, and commits only the successful retry', () => {
    const state = fromSnapshot(snapshot())
    applyEvent(state, event(1, { model: { type: 'text_delta', content_index: 0, text: 'FAILED GHOST' } }))
    applyEvent(state, event(2, { model: { type: 'reasoning_delta', content_index: 1, text: 'unfinished reasoning' } }))
    expect(Object.keys(state.previews)).toEqual(['attempt'])
    applyEvent(state, event(3, { type: 'attempt_failed' }))
    applyEvent(state, event(4, { model: { type: 'text_delta', content_index: 0, text: 'late' } }))
    expect(state.previews).toEqual({})
    applyEvent(state, event(5, { attempt_id: 'retry', model: { type: 'text_delta', content_index: 0, text: 'Actual answer' } }))
    const committed = entry('answer', 12, { run_id: 'run', message: { role: 'assistant', content: [{ type: 'text', text: 'Actual answer' }] } })
    applyEvent(state, { ...event(6, { type: 'message_end', attempt_id: 'retry' }), entry: committed })
    applyEvent(state, { ...event(6, { type: 'message_end', attempt_id: 'retry' }), entry: committed })
    expect(state.previews).toEqual({})
    expect(Object.values(state.entries).map(messageText)).toEqual(['Actual answer'])
  })

  it('applies a reconnect snapshot before queued events and never resurrects a completed attempt or run', () => {
    const response = entry('response', 10, { kind: 'model_response', model_response: { attempt_id: 'attempt', purpose: 'run', error: 'disconnected', reply: { message: { role: 'assistant' } } } })
    const runEnd = entry('end', 11, { kind: 'run', run: { id: 'run', status: 'failed' } })
    const state = fromSnapshot(snapshot([], [runEnd, response]))
    applyEvent(state, event(21, { model: { type: 'text_delta', content_index: 0, text: 'stale snapshot race' } }))
    applyEvent(state, event(22, { attempt_id: 'unseen-attempt', model: { type: 'text_delta', content_index: 0, text: 'stale run race' } }))
    expect(state.previews).toEqual({})
    expect(applyEvent(state, { ...event(1, {}), epoch: 'server-b' })).toBe(false)
    expect(state.sequence).toBe(22)
  })

  it('keeps native tool-argument preparation distinct from actual execution', () => {
    const state = fromSnapshot(snapshot())
    applyEvent(state, event(1, { model: { type: 'tool_call_delta', content_index: 0, text: '{"cmd":"write file"}' } }))
    expect(state.tools).toEqual({})
    applyEvent(state, event(2, { type: 'tool_execution_start', execution_id: 'exec' }))
    applyEvent(state, event(3, { type: 'tool_execution_update', execution_id: 'exec', progress: { text: 'real output\n' } }))
    expect(state.tools.exec).toEqual({ name: '', text: 'real output\n', started: true, ended: false })
    applyEvent(state, event(4, { type: 'tool_execution_end', execution_id: 'exec' }))
    expect(state.tools.exec?.ended).toBe(true)
  })
})

describe('transcript facts', () => {
  it('places silent completion before later user input without an empty assistant message', () => {
    const state = emptyState()
    addEntry(state, entry('first', 1, { message: { role: 'user', content: [{ type: 'text', text: 'Observe quietly' }] } }))
    addEntry(state, entry('silent', 4, { kind: 'run', run: { id: 'silent-run', status: 'completed', end_reason: 'no_reply' } }))
    addEntry(state, entry('second', 5, { message: { role: 'user', content: [{ type: 'text', text: 'Now respond' }] } }))
    addEntry(state, entry('answer', 8, { run_id: 'second-run', message: { role: 'assistant', content: [{ type: 'text', text: 'Response' }] } }))
    const rows = transcriptRows(state).map(row => row.kind === 'message' ? row.entry.id : row.runID)
    expect(rows).toEqual(['first', 'silent-run', 'second', 'second-run', 'answer'])
  })

  it('shows user presentation metadata and frozen image entries, but hides trigger XML and failed answers', () => {
    const user = entry('user', 1, { message: { role: 'user', metadata: { display_text: 'Hello <team>' }, content: [{ type: 'text', text: '<prompt>internal envelope</prompt>' }] } })
    expect(messageText(user)).toBe('Hello <team>')
    expect(visibleEntry(entry('image', 2, { message: { role: 'user', content: [{ type: 'image', media_type: 'image/png' }] } }))).toBe(true)
    expect(visibleEntry(entry('trigger', 3, { message: { role: 'user', metadata: { source: 'im.trigger' }, content: [{ type: 'text', text: '<trigger/>' }] } }))).toBe(false)
    expect(visibleEntry(entry('failure', 4, { message: { role: 'assistant', finish_reason: 'error', content: [{ type: 'text', text: 'Do not show' }] } }))).toBe(false)
  })
})
