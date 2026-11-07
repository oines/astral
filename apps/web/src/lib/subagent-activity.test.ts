import { describe, expect, it } from 'vitest'
import { addSubagentActivity, applyEvent, durableTranscriptGroups, emptyState, fromSnapshot, liveTranscriptGroups, reconcileSnapshot } from './session-state'
import type { Entry, Snapshot } from './types'
import type { SubagentActivity } from './subagents'

function activity(sequence: number, status: SubagentActivity['status'], second = sequence): SubagentActivity {
  return { id: `activity-${sequence}`, sequence, session_id: 'parent', child_session_id: 'child', name: 'Review', run_id: sequence <= 2 ? 'first' : 'second', status, time: `2026-11-05T00:00:${String(second).padStart(2, '0')}Z` }
}
function snapshot(activities: SubagentActivity[], cursor: string, next?: string): Snapshot {
  return { epoch: 'server', view: { id: 'parent', event_sequence: 0 } as Snapshot['view'], history: { entries: [], head_id: '' }, trace: { entries: [], head_id: '' }, subagent_activity: activities, subagent_activity_cursor: cursor, subagent_activity_next_cursor: next }
}
function statuses(state: ReturnType<typeof emptyState>) {
  return durableTranscriptGroups(state).flatMap(group => group.blocks).filter(block => block.kind === 'subagent-status').map(block => block.activity)
}

describe('durable subagent activity in the parent transcript', () => {
  it('keeps both runs and their terminal transitions once across live replay and reconnect', () => {
    const first = activity(1, 'started')
    const state = fromSnapshot(snapshot([first], '1'))
    const remaining = [activity(2, 'completed'), activity(3, 'started'), activity(4, 'cancelled')]
    expect(applyEvent(state, { epoch: 'server', subagent_activity: remaining, subagent_activity_cursor: '4' })).toBe(true)
    const revision = state.historyRevision
    applyEvent(state, { epoch: 'server', subagent_activity: remaining, subagent_activity_cursor: '4' })
    expect(state.historyRevision).toBe(revision)
    const next = reconcileSnapshot(state, snapshot([first, ...remaining], '4'))
    expect(statuses(next).map(row => row.status)).toEqual(['started', 'completed', 'started', 'cancelled'])
    expect(next.subagentActivity?.[first.id]).toBe(state.subagentActivity?.[first.id])
    expect(next.historyRevision).toBe(revision)
  })

  it('positions late-delivered facts by source time while retaining the append cursor', () => {
    const state = emptyState()
    addSubagentActivity(state, [activity(1, 'completed', 3), activity(2, 'started', 1)], '2')
    expect(statuses(state).map(row => row.sequence)).toEqual([2, 1])
    expect(state.subagentActivityCursor).toBe('2')
    addSubagentActivity(state, [activity(1, 'completed', 3)], '1')
    expect(state.subagentActivityCursor).toBe('2')
  })

  it('inserts state changes between visible messages without exposing model-only context', () => {
    const entries: Entry[] = [
      { id: 'user', sequence: 1, version: 1, session_id: 'parent', kind: 'message', time: '2026-11-05T00:00:00Z', message: { role: 'user', content: [{ type: 'text', text: 'Review' }] } },
      { id: 'context', sequence: 2, version: 2, session_id: 'parent', kind: 'context', time: '2026-11-05T00:00:02Z', message: { role: 'user', context_kind: 'astral.subagent_update', content: [{ type: 'text', text: 'Private model report' }] } },
      { id: 'answer', sequence: 3, version: 3, session_id: 'parent', kind: 'message', time: '2026-11-05T00:00:04Z', run_id: 'parent-run', message: { role: 'assistant', content: [{ type: 'text', text: 'Done' }] } },
    ]
    const data = snapshot([activity(1, 'started'), activity(2, 'failed', 3)], '2')
    data.history.entries = entries
    const state = fromSnapshot(data)
    const groups = durableTranscriptGroups(state)
    expect(groups.flatMap(group => group.blocks).map(block => block.kind)).toEqual(['subagent-status', 'subagent-status', 'text'])
    expect(groups.flatMap(group => group.entries).map(entry => entry.id)).toEqual(['user', 'answer'])
    expect(Object.keys(state.entries)).not.toContain('activity-1')
  })

  it('retains an independent pagination boundary while filling a long reconnect gap', () => {
    const previous = fromSnapshot(snapshot([activity(2, 'completed')], '2', '2'))
    const next = reconcileSnapshot(previous, snapshot([activity(8, 'started'), activity(9, 'failed')], '9', '8'))
    expect(next.subagentActivityBoundary).toEqual({ sequence: 2, cursor: '2' })
    expect(next.subagentActivityNextCursor).toBe('8')
    expect(Object.keys(next.subagentActivity ?? {})).toEqual(['activity-2', 'activity-8', 'activity-9'])
  })

  it('also fills the reconnect gap when no child had started at the previous snapshot', () => {
    const previous = fromSnapshot(snapshot([], '0'))
    const next = reconcileSnapshot(previous, snapshot([activity(8, 'started')], '8', '8'))
    expect(previous.subagentActivityCursor).toBe('0')
    expect(next.subagentActivityBoundary).toEqual({ sequence: 0, cursor: undefined })
  })

  it('keeps historical status blocks unchanged during model streaming', () => {
    const state = fromSnapshot(snapshot([activity(1, 'started')], '1'))
    state.view = { ...state.view!, run_id: 'parent-run', status: 'running' }
    state.previews.preview = { runID: 'parent-run', parts: { 0: { type: 'text_delta', text: 'One' } } }
    const durable = durableTranscriptGroups(state)
    const first = liveTranscriptGroups(state, durable).flatMap(group => group.blocks).find(block => block.kind === 'subagent-status')
    state.previews.preview.parts[0]!.text = 'One more'
    const second = liveTranscriptGroups(state, durable).flatMap(group => group.blocks).find(block => block.kind === 'subagent-status')
    expect(second).toBe(first)
  })
})
