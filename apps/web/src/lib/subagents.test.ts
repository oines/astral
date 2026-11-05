import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useSurfaceStore } from '../stores/surface'
import { subagentStatus } from './subagents'
import { addEntry, addSubagentActivity, emptyState, transcriptGroups } from './session-state'
import type { Entry } from './types'

function spawn(id: string, sequence: number, outcome = 'succeeded'): Entry {
  return {
    id, sequence, version: sequence, session_id: 'parent', time: '', kind: 'tool_result',
    tool: {
      invocation: { session_id: 'parent', run_id: 'run', turn_id: 'turn', assistant_id: 'assistant', execution_id: 'spawn', call: { id: 'call', name: 'spawn_agent', arguments: { task: 'Check recovery' } } },
      result: { outcome, details: { session_id: 'child', parent_session_id: 'parent', title: 'Recovery check', status: 'running' } },
    },
  }
}

describe('subagent conversation entry points', () => {
  it.each(['succeeded', 'failed'])('keeps a %s spawn result in the ordinary tool details', outcome => {
    const state = emptyState()
    const result = spawn('result', 1, outcome)
    addEntry(state, result)
    expect(transcriptGroups(state)[0]!.blocks).toMatchObject([{ kind: 'process', items: [{ kind: 'tool', key: 'spawn', entry: result }] }])
  })

  it('keeps one spawn tool inside the collapsed process with stable identity', () => {
    const state = emptyState()
    const starting = spawn('starting', 1)
    starting.kind = 'tool_started'
    delete starting.tool!.result
    addEntry(state, starting)
    const processKey = transcriptGroups(state)[0]!.blocks[0]!.key
    const result = spawn('result', 2)
    addEntry(state, result)
    const group = transcriptGroups(state)[0]!
    expect(group.blocks).toMatchObject([{ kind: 'process', items: [{ kind: 'tool', key: 'spawn', entry: result }] }])
    expect(group.blocks[0]!.key).toBe(processKey)
    const key = group.key
    addEntry(state, { id: 'answer', sequence: 3, version: 3, session_id: 'parent', time: '', kind: 'message', run_id: 'run', message: { role: 'assistant', content: [{ type: 'text', text: 'Reviewing the other files.' }] } })
    const after = transcriptGroups(state)[0]!
    expect(after.key).toBe(key)
    expect(after.blocks[0]).toBe(group.blocks[0])
    expect(after.blocks.map(block => block.kind)).toEqual(['process', 'text'])
  })

  it('shows one lifecycle row without duplicating the creation result as a card', () => {
    const state = emptyState()
    addEntry(state, { id: 'assistant', sequence: 1, version: 1, session_id: 'parent', run_id: 'run', time: '2026-11-05T00:00:00Z', kind: 'message', message: { role: 'assistant', content: [{ type: 'tool_call', tool_call: { id: 'call', name: 'spawn_agent', arguments: {} } }] } })
    const starting = spawn('starting', 2)
    starting.kind = 'tool_started'
    starting.time = '2026-11-05T00:00:01Z'
    delete starting.tool!.result
    addEntry(state, starting)
    const result = spawn('result', 3)
    result.time = '2026-11-05T00:00:02Z'
    addEntry(state, result)
    addSubagentActivity(state, [{ id: 'started', sequence: 1, session_id: 'parent', child_session_id: 'child', name: 'Recovery check', run_id: 'child-run', status: 'started', time: '2026-11-05T00:00:03Z' }], '1')
    const groups = transcriptGroups(state)
    expect(groups).toHaveLength(1)
    expect(groups[0]!.blocks.map(block => block.kind)).toEqual(['process', 'subagent-status'])
    const process = groups[0]!.blocks[0]!
    expect(process).toMatchObject({ kind: 'process', items: [{ kind: 'tool', key: 'spawn', entry: result }] })
    if (process.kind === 'process') expect(process.items).toHaveLength(1)
  })

  it('keeps failures and stopped executions distinct from a destroyed child session', () => {
    expect(subagentStatus('running')).toBe('running')
    expect(subagentStatus('failed')).toBe('subagentFailed')
    expect(subagentStatus('idle')).toBe('subagentIdle')
    expect(subagentStatus('queued')).toBe('subagentQueued')
  })
})

describe('one-panel subagent navigation', () => {
  beforeEach(() => { setActivePinia(createPinia()) })
  it('opens descendants in the same surface and returns through the parent path', () => {
    const surface = useSurfaceStore()
    surface.openSubagent({ sessionID: 'child', parentSessionID: 'root', title: 'Backend', status: 'running' }, { id: 'root', title: 'Review' })
    surface.openSubagent({ sessionID: 'grandchild', parentSessionID: 'child', title: 'Recovery', status: 'running' }, { id: 'child', title: 'Backend' })
    expect(surface.current).toMatchObject({ kind: 'subagent', id: 'grandchild', path: [{ id: 'root' }, { id: 'child' }, { id: 'grandchild' }] })
    surface.backToSession(1)
    expect(surface.current).toMatchObject({ id: 'child', path: [{ id: 'root' }, { id: 'child' }] })
    surface.backToSession(0)
    expect(surface.current).toBeUndefined()
  })

  it('replaces the panel path when a different main conversation opens a child', () => {
    const surface = useSurfaceStore()
    surface.openSubagent({ sessionID: 'child', parentSessionID: 'root', title: 'Backend', status: 'running' }, { id: 'root', title: 'Review' })
    surface.openSubagent({ sessionID: 'other-child', parentSessionID: 'other-root', title: 'Other task', status: 'idle' }, { id: 'other-root', title: 'Other conversation' })
    expect(surface.current).toMatchObject({ id: 'other-child', path: [{ id: 'other-root' }, { id: 'other-child' }] })
  })

  it('uses the original parent for a spawn result inherited in copied history', () => {
    const surface = useSurfaceStore()
    surface.openSubagent({ sessionID: 'child', parentSessionID: 'root', title: 'Backend', status: 'running' }, { id: 'root', title: 'Review' })
    surface.openSubagent({ sessionID: 'sibling', parentSessionID: 'root', title: 'Frontend', status: 'idle' }, { id: 'root', title: 'Review' })
    expect(surface.current).toMatchObject({ id: 'sibling', path: [{ id: 'root' }, { id: 'sibling' }] })
  })
})
