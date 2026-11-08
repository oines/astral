import { describe, expect, it } from 'vitest'
import { addEntry, addSubagentActivity, applyEvent, durableTranscriptGroups, emptyState, fromSnapshot, historyPageStart, liveTranscriptGroups, reconcileSnapshot, transcriptGroups } from './session-state'
import type { Entry, Snapshot } from './types'
import type { SubagentActivity } from './subagents'

function activity(sequence: number, status: SubagentActivity['status'], second = sequence): SubagentActivity {
  return { id: `activity-${sequence}`, sequence, session_id: 'parent', child_session_id: 'child', name: 'Review', run_id: sequence <= 2 ? 'first' : 'second', status, time: new Date(Date.UTC(2026, 10, 5, 0, 0, second)).toISOString() }
}
function message(id: string, second: number): Entry {
  return { id, sequence: second, version: second, session_id: 'parent', kind: 'message', time: activity(second, 'started').time, run_id: 'parent-run', message: { role: 'assistant', content: [{ type: 'text', text: id }] } }
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

  it('orders late-delivered lifecycle events by their submillisecond source times', () => {
    const state = emptyState()
    addSubagentActivity(state, [
      { ...activity(1, 'completed'), time: '2026-11-08T01:00:00.123900Z' },
      { ...activity(2, 'started'), time: '2026-11-08T09:00:00.123100+08:00' },
    ], '2')
    expect(statuses(state).map(row => row.sequence)).toEqual([2, 1])
  })

  it('places an earlier child Run completion before a later parent message in the same millisecond', () => {
    const state = emptyState()
    addEntry(state, { ...message('parent-follow-up', 1), time: '2026-11-08T01:00:00.123100Z' }, true)
    addSubagentActivity(state, [{ ...activity(1, 'completed'), run_id: 'earlier-child-run', time: '2026-11-08T01:00:00.123050Z' }], '1')
    const blocks = durableTranscriptGroups(state).flatMap(group => group.blocks)
    expect(blocks.map(block => block.kind)).toEqual(['subagent-status', 'text'])
    expect(blocks[0]).toMatchObject({ activity: { run_id: 'earlier-child-run', status: 'completed' } })
  })

  it.each([
    ['2026-11-08T01:00:00.123100001Z', ['process', 'subagent-status']],
    ['2026-11-08T09:00:00.123900+08:00', ['process', 'subagent-status']],
    ['2026-11-08T01:00:00.123100000Z', ['process', 'subagent-status']],
  ])('interleaves a complete parent tool call and child event at %s without losing source precision', (childTime, expected) => {
    const state = emptyState()
    const call = { id: 'spawn-call', name: 'spawn_agent', arguments: { task: 'Review' } }
    const invocation = { session_id: 'parent', run_id: 'parent-run', turn_id: 'turn', assistant_id: 'parent-call', execution_id: 'spawn-execution', call }
    addEntry(state, {
      ...message('parent-call', 1), time: '2026-11-08T01:00:00.123100000Z',
      message: { role: 'assistant', content: [{ type: 'tool_call', tool_call: call }] },
    }, true)
    addEntry(state, {
      id: 'parent-tool-started', sequence: 2, version: 2, session_id: 'parent', kind: 'tool_started', time: '2026-11-08T01:00:00.123100000Z', tool: { invocation },
    })
    const result: Entry = {
      id: 'parent-result', sequence: 3, version: 3, session_id: 'parent', kind: 'tool_result', time: '2026-11-08T01:00:00.124100Z',
      tool: { invocation, result: { outcome: 'succeeded', details: { session_id: 'child', status: 'queued' } } },
    }
    addEntry(state, result, true)
    addSubagentActivity(state, [{ ...activity(1, 'started'), time: childTime }], '1')
    const blocks = durableTranscriptGroups(state).flatMap(group => group.blocks)
    expect(blocks.map(block => block.kind)).toEqual(expected)
    expect(blocks.find(block => block.kind === 'process')).toMatchObject({ items: [{ kind: 'tool', key: 'spawn-execution', entry: result }] })
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

  it('keeps the independent activity tail inside the loaded conversation window while paging', () => {
    const rows = Array.from({ length: 60 }, (_, index) => activity(index + 1, index % 2 ? 'completed' : 'started'))
    const data = snapshot(rows, '60')
    data.history = { entries: [message('recent', 48), message('latest', 60)], head_id: 'latest', next_cursor: 'older' }
    const state = fromSnapshot(data)
    expect(state.historyStartTime).toBe(Date.parse(rows[47]!.time))
    expect(statuses(state).map(row => row.sequence)).toEqual(Array.from({ length: 13 }, (_, index) => index + 48))
    expect(Object.keys(state.subagentActivity ?? {})).toHaveLength(60)
    const retained = transcriptGroups(state).flatMap(group => group.blocks).find(block => block.kind === 'subagent-status' && block.activity.sequence === 60)

    addEntry(state, message('earlier-page', 30), true)
    expect(statuses(state).map(row => row.sequence)).toEqual(Array.from({ length: 31 }, (_, index) => index + 30))
    const afterPage = transcriptGroups(state).flatMap(group => group.blocks).find(block => block.kind === 'subagent-status' && block.activity.sequence === 60)
    expect(afterPage).toBe(retained)

    state.historyCursor = undefined
    expect(statuses(state)).toEqual(rows)
    expect(state.subagentActivityCursor).toBe('60')
  })

  it('does not let internal or trace-only records open the main-history activity window', () => {
    const data = snapshot([activity(1, 'started'), activity(2, 'completed')], '2')
    data.history = { entries: [
      { ...message('internal', 1), kind: 'context' },
      { id: 'run', sequence: 2, version: 2, session_id: 'parent', kind: 'run', time: activity(2, 'started').time, run: { id: 'parent-run', status: 'started' } },
    ], head_id: 'run', next_cursor: 'unread-message' }
    data.trace.entries = [message('trace-answer', 1)]
    const state = fromSnapshot(data)
    expect(state.historyStartTime).toBeUndefined()
    expect(statuses(state)).toEqual([])
    expect(Object.keys(state.subagentActivity ?? {})).toHaveLength(2)

    // The same immutable entry may first arrive in the operational trace. Its
    // later main-history page still establishes coverage despite the early return.
    const revision = state.historyRevision
    addEntry(state, data.trace.entries[0]!, true)
    expect(state.historyStartTime).toBe(Date.parse(activity(1, 'started').time))
    expect(state.historyRevision).toBe(revision + 1)
    expect(statuses(state).map(row => row.sequence)).toEqual([1, 2])
  })

  it('establishes history coverage from visible reasoning, tool calls and executions', () => {
    const reasoning: Entry = { ...message('reasoning', 8), message: { role: 'assistant', content: [{ type: 'reasoning', text: 'Reviewing' }] } }
    const call: Entry = { ...message('call', 6), message: { role: 'assistant', content: [{ type: 'tool_call', tool_call: { id: 'call', name: 'read', arguments: '{}' } }] } }
    const tool: Entry = { ...message('tool', 4), kind: 'tool_result', message: undefined, tool: { invocation: { execution_id: 'execution', session_id: 'parent', run_id: 'parent-run', turn_id: 'turn', assistant_id: 'call', call: { id: 'call', name: 'read', arguments: '{}' } } } }
    const context: Entry = { ...message('context', 1), kind: 'context' }
    expect(historyPageStart([context])).toBeUndefined()
    expect(historyPageStart([reasoning])).toBe(Date.parse(reasoning.time))
    expect(historyPageStart([reasoning, call])).toBe(Date.parse(call.time))
    expect(historyPageStart([reasoning, context, call, tool])).toBe(Date.parse(tool.time))
    expect(historyPageStart([{ ...message('legacy', 1), time: '' }])).toBe(0)
  })

  it('withholds only the unreconciled interval and releases records as the gap shrinks', () => {
    const rows = Array.from({ length: 60 }, (_, index) => activity(index + 1, 'started'))
    const state = fromSnapshot(snapshot(rows, '60'))
    const retained = transcriptGroups(state).flatMap(group => group.blocks).find(block => block.kind === 'subagent-status' && block.activity.sequence === 60)
    state.historyGaps = [{ start: Date.parse(rows[9]!.time), end: Date.parse(rows[49]!.time) }]
    expect(statuses(state).map(row => row.sequence)).toEqual([...Array.from({ length: 10 }, (_, index) => index + 1), ...Array.from({ length: 11 }, (_, index) => index + 50)])
    expect(transcriptGroups(state).flatMap(group => group.blocks).find(block => block.kind === 'subagent-status' && block.activity.sequence === 60)).toBe(retained)

    state.historyGaps[0]!.end = Date.parse(rows[29]!.time)
    expect(statuses(state).map(row => row.sequence)).toEqual([...Array.from({ length: 10 }, (_, index) => index + 1), ...Array.from({ length: 31 }, (_, index) => index + 30)])
    state.historyGaps = undefined
    expect(statuses(state)).toEqual(rows)
    expect(Object.keys(state.subagentActivity ?? {})).toHaveLength(60)
    expect(transcriptGroups(state).flatMap(group => group.blocks).find(block => block.kind === 'subagent-status' && block.activity.sequence === 60)).toBe(retained)
    expect(state.subagentActivityCursor).toBe('60')
  })

  it('retains the loaded coverage and pending gap across replacement snapshots', () => {
    const data = snapshot([activity(48, 'started')], '48')
    data.history = { entries: [message('earlier', 30)], head_id: 'earlier', next_cursor: 'older' }
    const previous = fromSnapshot(data)
    previous.historyGaps = [{ start: Date.parse(activity(35, 'started').time), end: Date.parse(activity(50, 'started').time) }]
    const fresh = snapshot([activity(60, 'completed')], '60')
    fresh.history = { entries: [message('latest', 60)], head_id: 'latest', next_cursor: 'gap' }
    const next = reconcileSnapshot(previous, fresh)
    expect(next.historyStartTime).toBe(previous.historyStartTime)
    expect(next.historyGaps).toEqual(previous.historyGaps)
    expect(statuses(next).map(row => row.sequence)).toEqual([60])

    fresh.history.entries = [message('oldest', 10)]
    expect(reconcileSnapshot(next, fresh).historyStartTime).toBe(Date.parse(activity(10, 'started').time))
  })

  it('preserves legacy activity without a valid source timestamp', () => {
    const legacy = { ...activity(1, 'started'), time: '' }
    const data = snapshot([legacy], '1')
    data.history.next_cursor = 'older'
    const state = fromSnapshot(data)
    state.historyGaps = [{ start: 0, end: Infinity }]
    expect(statuses(state)).toEqual([legacy])
  })

  it('keeps reconciled activity between independent reconnect gaps visible', () => {
    const rows = Array.from({ length: 20 }, (_, index) => activity(index + 1, 'started'))
    const state = fromSnapshot(snapshot(rows, '20'))
    state.historyGaps = [
      { start: Date.parse(rows[1]!.time), end: Date.parse(rows[5]!.time) },
      { start: Date.parse(rows[11]!.time), end: Date.parse(rows[18]!.time) },
    ]
    expect(statuses(state).map(row => row.sequence)).toEqual([1, 2, 6, 7, 8, 9, 10, 11, 12, 19, 20])
    const next = reconcileSnapshot(state, snapshot([rows[19]!], '20'))
    expect(statuses(next).map(row => row.sequence)).toEqual([1, 2, 6, 7, 8, 9, 10, 11, 12, 19, 20])
    expect(next.historyGaps).toHaveLength(2)
    expect(Object.keys(next.subagentActivity ?? {})).toHaveLength(20)
  })
})
