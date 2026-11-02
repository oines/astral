import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useSurfaceStore } from '../stores/surface'
import { spawnedSubagent, subagentStatus } from './subagents'
import { addEntry, emptyState, transcriptGroups } from './session-state'
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
  it('accepts only successful spawn results and leaves failures as ordinary tool details', () => {
    expect(spawnedSubagent(spawn('spawn', 1))).toEqual({ sessionID: 'child', parentSessionID: 'parent', title: 'Recovery check', status: 'running' })
    expect(spawnedSubagent(spawn('failed', 1, 'failed'))).toBeUndefined()
    const other = spawn('other', 1)
    other.tool!.invocation.call.name = 'exec_command'
    expect(spawnedSubagent(other)).toBeUndefined()
    const pending = spawn('pending', 1)
    delete pending.tool!.result
    expect(spawnedSubagent(pending)).toBeUndefined()
  })

  it('prefers the stable agent name while accepting older title-only results', () => {
    const entry = spawn('named', 1)
    const details = entry.tool!.result!.details as Record<string, unknown>
    details.name = 'Source check'
    expect(spawnedSubagent(entry)?.title).toBe('Source check')
    delete details.title
    expect(spawnedSubagent(entry)?.title).toBe('Source check')
    details.title = 'Recovery check'
    details.name = '  '
    expect(spawnedSubagent(entry)?.title).toBe('Recovery check')
    delete details.name
    expect(spawnedSubagent(entry)?.title).toBe('Recovery check')
  })

  it('shows a spawned child once outside collapsed tool processes and retains its stable identity', () => {
    const state = emptyState()
    const starting = spawn('starting', 1)
    starting.kind = 'tool_started'
    delete starting.tool!.result
    addEntry(state, starting)
    transcriptGroups(state)
    const result = spawn('result', 2)
    addEntry(state, result)
    const group = transcriptGroups(state)[0]!
    expect(group.blocks).toEqual([{ kind: 'subagent', key: 'spawn', entry: result }])
    const key = group.key
    addEntry(state, { id: 'answer', sequence: 3, version: 3, session_id: 'parent', time: '', kind: 'message', run_id: 'run', message: { role: 'assistant', content: [{ type: 'text', text: 'Reviewing the other files.' }] } })
    const after = transcriptGroups(state)[0]!
    expect(after.key).toBe(key)
    expect(after.blocks[0]).toBe(group.blocks[0])
    expect(after.blocks.map(block => block.kind)).toEqual(['subagent', 'text'])
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
