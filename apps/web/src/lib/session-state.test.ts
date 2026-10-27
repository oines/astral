import { describe, expect, it } from 'vitest'
import { addEntry, applyEvent, emptyState, fromSnapshot, messageText, transcriptGroups, transcriptRows, visibleEntry } from './session-state'
import type { CoreEvent, Entry, SessionView, Snapshot } from './types'

function entry(id: string, sequence: number, rest: Partial<Entry>): Entry {
  return { id, sequence, kind: 'message', session_id: 'session', version: 7, time: '2026-10-21T00:00:00Z', ...rest }
}
const view: SessionView = {
  id: 'session', agent_id: 'agent', revision: 1, created_at: '', updated_at: '', title: 'Work',
  archived: false, pinned: false, source: 'web', rollout_id: 'rollout', head_id: 'head',
  status: 'running', can_continue: false, model_available: true, run_id: 'run', selection: { id: 'model' }, approvals: [], pending: [], unresolved: [], event_sequence: 0,
}
function snapshot(history: Entry[] = [], trace: Entry[] = []): Snapshot {
  return { epoch: 'server-a', view, history: { entries: history, head_id: 'head' }, trace: { entries: trace, head_id: 'head' } }
}
function event(sequence: number, rest: Partial<CoreEvent>) {
  return { epoch: 'server-a', core: { sequence, session_id: 'session', run_id: 'run', type: 'message_update', attempt_id: 'attempt', ...rest } }
}

describe('reconciling live previews with durable history', () => {
  it('does not invent assistant turns for failed retries that produced no output', () => {
    const history = [
      entry('question', 1, { run_id: 'first', message: { role: 'user', content: [{ type: 'text', text: 'Run it' }] } }),
      entry('answer', 2, { run_id: 'first', message: { role: 'assistant', content: [{ type: 'text', text: 'Work completed' }] } }),
    ]
    const failures = Array.from({ length: 5 }, (_, i) => entry(`failure-${i}`, i + 3, { kind: 'run', run: { id: i === 0 ? 'first' : `retry-${i}`, status: 'failed', error: 'delivery failed' } }))
    const restored = snapshot(history, failures)
    restored.view = { ...view, run_id: undefined, status: 'needs_attention', error: 'delivery failed' }
    const groups = transcriptGroups(fromSnapshot(restored))
    expect(groups).toHaveLength(2)
    expect(groups[1]).toMatchObject({ role: 'assistant' })
    expect(groups[1]?.blocks).toEqual([{ kind: 'text', key: 'answer:0', text: 'Work completed' }])
  })

  it('shows retry progress, replaces it on the next attempt, and rejects stale view payloads', () => {
    const state = fromSnapshot(snapshot())
    const attempt = { attempt_id: 'attempt', run_id: 'run', turn_id: 'turn', purpose: 'generation', attempt: 1, max_attempts: 3, state: 'retrying', next_retry_at: '2026-10-22T00:00:10Z' }
    applyEvent(state, event(10, { type: 'retry', attempt }))
    expect(transcriptGroups(state).at(-1)).toMatchObject({ active: true, status: 'retryingModel', statusParams: { attempt: 2, max: 3, time: attempt.next_retry_at } })
    applyEvent(state, event(11, { type: 'attempt_start', attempt_id: 'next', attempt: { ...attempt, attempt_id: 'next', attempt: 2, state: 'running', next_retry_at: undefined } }))
    expect(transcriptGroups(state).at(-1)).toMatchObject({ status: 'modelAttempt', statusParams: { attempt: 2, max: 3 } })
    applyEvent(state, { ...event(9, { type: 'retry', attempt }), view: { ...view, event_sequence: 9, attempt } })
    expect(state.view?.attempt?.attempt_id).toBe('next')
  })

  it('restores summary retry progress and ignores events preceding the snapshot boundary', () => {
    const restored = snapshot()
    restored.view = { ...view, run_id: undefined, status: 'compacting', event_sequence: 40, attempt: { attempt_id: 'summary', purpose: 'compaction', attempt: 2, max_attempts: 3, state: 'retrying', next_retry_at: '2026-10-22T00:00:10Z' } }
    const state = fromSnapshot(restored)
    applyEvent(state, { ...event(39, { type: 'attempt_start' }), view: { ...view, event_sequence: 39 } })
    expect(transcriptGroups(state).at(-1)).toMatchObject({ status: 'retryingCompaction', statusParams: { attempt: 3, max: 3 } })
    expect(state.view?.status).toBe('compacting')
    expect(state.sequence).toBe(40)
  })
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
    const response = entry('response', 10, { generation: { attempt_id: 'attempt', configuration_id: 'configuration', context_digest: 'digest', model: { id: 'model', context_window: 10000, max_output_tokens: 1000 } }, message: { role: 'assistant', content: [{ type: 'text', text: 'accepted' }] } })
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
  it('shows the current result after an interrupted tool call is completed', () => {
    const invocation = { session_id: 'session', execution_id: 'send', run_id: 'run', turn_id: 'turn', assistant_id: 'assistant', call: { id: 'call', name: 'send_message', arguments: { text: 'hello' } } }
    const state = fromSnapshot(snapshot([], [
      entry('result', 1, { kind: 'tool_result', tool: { invocation, needs_resolution: true, result: { outcome: 'unknown', is_error: true } } }),
      entry('run-end', 2, { kind: 'run', run: { id: 'run', status: 'failed' } }),
      entry('completed', 3, { kind: 'tool_resolution', tool: { invocation, result: { outcome: 'failed', is_error: true, content: [{ type: 'text', text: 'Request interrupted' }] } } }),
    ]))
    state.view = { ...view, run_id: 'next-run', last_run: { id: 'next-run', status: 'started' } }
    const group = transcriptGroups(state)[0]
    expect(group?.status).toBeUndefined()
    expect(group?.blocks[0]).toMatchObject({ kind: 'process', items: [{ kind: 'tool', entry: { id: 'completed' } }] })
  })

  it('groups a response with its process while preserving input order and silent turns', () => {
    const state = emptyState()
    addEntry(state, entry('question', 1, { message: { role: 'user', content: [{ type: 'text', text: 'Inspect the file' }] } }))
    addEntry(state, entry('commentary', 3, { run_id: 'run', message: { role: 'assistant', content: [{ type: 'text', text: 'Checking it now.' }] } }))
    addEntry(state, entry('answer', 6, { run_id: 'run', message: { role: 'assistant', content: [{ type: 'text', text: 'The file is valid.' }] } }))
    addEntry(state, entry('follow-up', 8, { message: { role: 'user', content: [{ type: 'text', text: 'Observe quietly.' }] } }))
    addEntry(state, entry('silent', 12, { kind: 'run', run: { id: 'silent-run', status: 'completed' } }))
    const groups = transcriptGroups(state)
    expect(groups.map(group => [group.role, group.runID, group.entries.map(entry => entry.id)])).toEqual([
      ['user', undefined, ['question']], ['assistant', 'run', ['commentary', 'answer']],
      ['user', undefined, ['follow-up']],
    ])
    state.view = { ...view, run_id: 'live-run' }
    expect(transcriptGroups(state).at(-1)).toMatchObject({ role: 'assistant', runID: 'live-run', entries: [] })
  })

  it('places silent completion before later user input without an empty assistant message', () => {
    const state = emptyState()
    addEntry(state, entry('first', 1, { message: { role: 'user', content: [{ type: 'text', text: 'Observe quietly' }] } }))
    addEntry(state, entry('silent', 4, { kind: 'run', run: { id: 'silent-run', status: 'completed' } }))
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

  it.each(['user', 'assistant'])('keeps passive context out of the transcript even with role %s and displayable content', role => {
    const context = [
      entry('environment', 1, { kind: 'context', message: { role, context_kind: 'astral.environment', content: [{ type: 'text', text: 'Current Astral environment snapshot: {}' }] } }),
      entry('observation', 2, { kind: 'context', message: { role, context_kind: 'im.observation', metadata: { source: 'im', display_text: 'A quiet group message' }, content: [{ type: 'text', text: '<message>internal envelope</message>' }, { type: 'image', media_type: 'image/png' }] } }),
      entry('untagged', 3, { kind: 'context', message: { role, content: [{ type: 'text', text: 'Other passive context' }] } }),
      entry('tagged-message', 4, { message: { role, context_kind: 'summary', content: [{ type: 'text', text: 'Model-only summary' }] } }),
    ]
    const restored = snapshot(context, context)
    restored.view = { ...view, status: 'idle', can_continue: false, run_id: undefined }
    const state = fromSnapshot(restored)
    expect(transcriptGroups(state)).toEqual([])
    expect(transcriptRows(state)).toEqual([])
    // Filtering is a presentation rule; history remains available unchanged.
    expect(Object.values(state.entries)).toEqual(context)
    expect(Object.values(state.trace)).toEqual(context)
  })

  it('filters live and paginated context without splitting replies or matching user text', () => {
    const question = entry('question', 3, { message: { role: 'user', content: [{ type: 'text', text: 'Current Astral environment snapshot: explain this label' }] } })
    const commentary = entry('commentary', 4, { run_id: 'run', message: { role: 'assistant', content: [{ type: 'text', text: 'Checking now.' }] } })
    const state = fromSnapshot(snapshot([question, commentary]))
    applyEvent(state, { ...event(1, { type: 'context_applied' }), entry: entry('live-context', 5, { kind: 'context', message: { role: 'user', context_kind: 'im.observation', metadata: { display_text: 'Quiet observation' }, content: [{ type: 'image', media_type: 'image/png' }] } }) })
    applyEvent(state, { ...event(2, { type: 'message_end' }), entry: entry('answer', 6, { run_id: 'run', message: { role: 'assistant', content: [{ type: 'text', text: 'Done.' }] } }) })
    addEntry(state, entry('older-context', 1, { kind: 'context', message: { role: 'user', context_kind: 'astral.environment', content: [{ type: 'text', text: 'Old snapshot' }] } }), true)
    addEntry(state, entry('trigger', 2, { message: { role: 'user', metadata: { source: 'im.trigger', display_text: 'Internal activation' }, content: [{ type: 'text', text: '<im_trigger/>' }] } }), true)
    expect(transcriptGroups(state).map(group => group.entries.map(entry => entry.id))).toEqual([['question'], ['commentary', 'answer']])
    expect(transcriptRows(state).filter(row => row.kind === 'message').map(row => row.entry.id)).toEqual(['question', 'commentary', 'answer'])
    expect(messageText(question)).toBe('Current Astral environment snapshot: explain this label')
  })
})

describe('ordered transcript process', () => {
  function toolEntry(id: string, sequence: number, assistantID: string, executionID: string, kind = 'tool_result'): Entry {
    return entry(id, sequence, { kind, run_id: 'run', tool: { invocation: { session_id: 'session', run_id: 'run', turn_id: 'turn', assistant_id: assistantID, execution_id: executionID, call: { id: 'reused-call-id', name: 'exec_command', arguments: { cmd: 'pwd' } } }, ...(kind === 'tool_result' ? { result: { outcome: 'succeeded', content: [{ type: 'text', text: 'output' }] } } : {}) } })
  }

  it('keeps the current aggregate mounted through streaming, commits and the next text boundary', () => {
    const state = fromSnapshot(snapshot())
    const initial = transcriptGroups(state)[0]!
    const initialKey = initial.blocks[0]!.key
    applyEvent(state, event(1, { model: { type: 'reasoning_delta', content_index: 0, text: 'Inspecting the workspace' } }))
    expect(transcriptGroups(state)[0]).toMatchObject({ key: initial.key, blocks: [{ key: initialKey, active: true }] })
    applyEvent(state, {
      ...event(2, { type: 'message_end' }),
      entry: entry('thought', 2, { run_id: 'run', message: { role: 'assistant', content: [{ type: 'reasoning', text: 'Inspecting the workspace' }] } }),
    })
    expect(transcriptGroups(state)[0]!.blocks[0]).toMatchObject({ key: initialKey, active: true })
    applyEvent(state, event(3, { attempt_id: 'next', model: { type: 'text_delta', content_index: 0, text: 'I found the project.' } }))
    const split = transcriptGroups(state)[0]!.blocks
    expect(split.map(block => block.kind)).toEqual(['process', 'text', 'process'])
    expect(split[0]).not.toHaveProperty('active')
    expect(split[2]).toMatchObject({ active: true, items: [] })
    applyEvent(state, {
      ...event(4, { type: 'message_end', attempt_id: 'next' }),
      entry: entry('commentary', 4, { run_id: 'run', message: { role: 'assistant', content: [{ type: 'text', text: 'I found the project.' }] } }),
    })
    expect(transcriptGroups(state)[0]!.blocks[2]!.key).toBe(split[2]!.key)
  })

  it('keeps earlier shell items live without adding later items across an assistant text boundary', () => {
    const state = fromSnapshot(snapshot())
    addEntry(state, toolEntry('old-start', 1, 'first', 'old-shell', 'tool_started'))
    addEntry(state, entry('commentary', 2, { run_id: 'run', message: { role: 'assistant', content: [{ type: 'text', text: 'The command is still running; checking another file.' }] } }))
    addEntry(state, toolEntry('new-start', 3, 'second', 'new-shell', 'tool_started'))
    const before = transcriptGroups(state)[0]!.blocks
    expect(before.map(block => block.kind === 'process' ? block.items.map(item => item.key) : block.text)).toEqual([
      ['old-shell'], 'The command is still running; checking another file.', ['new-shell'],
    ])
    applyEvent(state, event(4, { type: 'tool_execution_update', execution_id: 'old-shell', progress: { text: 'Command is making progress\n' } }))
    expect(state.tools['old-shell']).toMatchObject({ text: 'Command is making progress\n', started: true, ended: false })
    addEntry(state, toolEntry('old-result', 5, 'first', 'old-shell'))
    const after = transcriptGroups(state)[0]!.blocks
    expect(after.map(block => block.key)).toEqual(before.map(block => block.key))
    expect(after[0]).toMatchObject({ items: [{ key: 'old-shell', entry: { id: 'old-result', tool: { result: { outcome: 'succeeded' } } } }] })
    expect(after[2]).toMatchObject({ active: true, items: [{ key: 'new-shell' }] })
    expect(state.tools['old-shell']).toMatchObject({ text: 'Command is making progress\n', ended: true })
    expect(state.tools['new-shell']).toMatchObject({ started: true, ended: false })
  })

  it('keeps reasoning, commentary, tools and final text in their original order, with one item per execution', () => {
    const state = emptyState()
    addEntry(state, entry('first', 2, { run_id: 'run', message: { role: 'assistant', content: [
      { type: 'reasoning', text: 'First thought' }, { type: 'text', text: 'Checking now.' },
      { type: 'tool_call', tool_call: { id: 'reused-call-id', name: 'exec_command', arguments: {} } },
    ] } }))
    addEntry(state, toolEntry('start', 3, 'first', 'exec-1', 'tool_started'))
    addEntry(state, toolEntry('result', 4, 'first', 'exec-1'))
    addEntry(state, entry('second', 5, { run_id: 'run', message: { role: 'assistant', content: [
      { type: 'reasoning', text: 'Next thought' },
      { type: 'tool_call', tool_call: { id: 'reused-call-id', name: 'exec_command', arguments: {} } },
      { type: 'text', text: 'Done.' },
    ] } }))
    addEntry(state, toolEntry('result-2', 6, 'second', 'exec-2'))
    const blocks = transcriptGroups(state)[0]!.blocks
    expect(blocks.map(block => block.kind === 'text' ? block.text : block.items.map(item => item.kind === 'reasoning' ? item.text : item.entry.id))).toEqual([
      ['First thought'], 'Checking now.', ['result', 'Next thought', 'result-2'], 'Done.',
    ])
  })

  it('discards failed attempts and hides opaque reasoning while preserving streaming content order', () => {
    const state = fromSnapshot(snapshot())
    applyEvent(state, event(1, { attempt_id: 'failed-attempt', model: { type: 'reasoning_delta', content_index: 0, text: 'FAILED REASONING' } }))
    applyEvent(state, event(2, { type: 'attempt_failed', attempt_id: 'failed-attempt' }))
    addEntry(state, entry('opaque', 2, { run_id: 'run', message: { role: 'assistant', content: [{ type: 'reasoning' }] } }))
    applyEvent(state, event(3, { model: { type: 'reasoning_delta', content_index: 0, text: 'First' } }))
    applyEvent(state, event(4, { model: { type: 'text_delta', content_index: 1, text: 'Visible text' } }))
    applyEvent(state, event(5, { model: { type: 'reasoning_delta', content_index: 2, text: 'Second' } }))
    const group = transcriptGroups(state)[0]!
    expect(group.blocks.map(block => block.kind)).toEqual(['process', 'text', 'process'])
    expect(JSON.stringify(group.blocks)).not.toContain('FAILED REASONING')
    expect(group).toMatchObject({ active: true, status: 'working' })
  })

  it('uses history tool results when lifecycle trace has been paginated away', () => {
    const historyTool = toolEntry('result-only', 7, 'earlier', 'exec')
    const state = fromSnapshot(snapshot([historyTool], []))
    const group = transcriptGroups(state)[0]!
    expect(group.blocks[0]).toMatchObject({ kind: 'process', items: [{ kind: 'tool', entry: { id: 'result-only' } }] })
  })
})
