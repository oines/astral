import { describe, expect, it } from 'vitest'
import { activeSessionTreeIDs } from './session-tree-activity'
import type { Session } from './types'

function session(id: string, extra: Partial<Session> & { status?: string } = {}): Session {
  return { id, agent_id: 'bot', title: id, source: 'web', archived: false, pinned: false, revision: 1, created_at: '', updated_at: '', ...extra }
}

describe('delegation tree activity from the existing session directory', () => {
  it('marks an idle root active while a delegated child runs', () => {
    const sessions = [session('root'), session('child', { subagent_parent_id: 'root' })]
    expect(activeSessionTreeIDs(sessions, { root: { status: 'idle' }, child: { status: 'running' } })).toEqual(new Set(['child', 'root']))
  })

  it('propagates all activity states through multiple levels independent of directory order', () => {
    const sessions = [session('leaf', { subagent_parent_id: 'middle' }), session('root'), session('middle', { subagent_parent_id: 'root' })]
    for (const status of ['running', 'stopping', 'compacting']) {
      expect(activeSessionTreeIDs(sessions, { leaf: { status } })).toEqual(new Set(['leaf', 'middle', 'root']))
    }
  })

  it('does not retain activity when all children are terminal, including failures', () => {
    const sessions = [session('root'), ...['one', 'two', 'three'].map(id => session(id, { subagent_parent_id: 'root' }))]
    const statuses = { one: { status: 'idle' }, two: { status: 'failed' }, three: { status: 'cancelled' } }
    expect(activeSessionTreeIDs(sessions, { ...statuses, one: { status: 'running' } })).toEqual(new Set(['one', 'root']))
    expect(activeSessionTreeIDs(sessions, statuses)).toEqual(new Set())
    expect(activeSessionTreeIDs(sessions, {})).toEqual(new Set())
  })

  it('ignores ordinary fork ancestry and propagates only a delegated parent', () => {
    const sessions = [session('fork-origin'), session('delegation-root'), session('fork', { parent_session_id: 'fork-origin' }), session('child', { parent_session_id: 'fork-origin', subagent_parent_id: 'delegation-root' })]
    expect(activeSessionTreeIDs(sessions, { fork: { status: 'running' }, child: { status: 'running' } })).toEqual(new Set(['fork', 'child', 'delegation-root']))
  })

  it('stops at missing or cross-Bot parents', () => {
    const sessions = [session('root'), session('foreign', { agent_id: 'other-bot', subagent_parent_id: 'root' }), session('orphan', { subagent_parent_id: 'missing' })]
    expect(activeSessionTreeIDs(sessions, { foreign: { status: 'running' }, orphan: { status: 'running' } })).toEqual(new Set(['foreign', 'orphan']))
  })

  it('excludes archived and deleting nodes and isolates branches beneath them', () => {
    for (const boundary of [{ archived: true }, { deletion: { rollout_id: 'deleted-rollout' } }]) {
      const sessions = [session('root'), session('sealed', { ...boundary, subagent_parent_id: 'root' }), session('descendant', { subagent_parent_id: 'sealed' })]
      expect(activeSessionTreeIDs(sessions, { sealed: { status: 'running' }, descendant: { status: 'running' } })).toEqual(new Set(['descendant']))
    }
  })

  it('uses directory status only when live status is absent and never mutates either input', () => {
    const sessions = Object.freeze([Object.freeze(session('root', { status: 'idle' })), Object.freeze(session('child', { status: 'running', subagent_parent_id: 'root' }))])
    expect(activeSessionTreeIDs(sessions, Object.freeze({}))).toEqual(new Set(['child', 'root']))
    expect(activeSessionTreeIDs(sessions, Object.freeze({ child: Object.freeze({ status: 'failed' }) }))).toEqual(new Set())
    expect(activeSessionTreeIDs(sessions, { child: undefined })).toEqual(new Set(['child', 'root']))
    expect(activeSessionTreeIDs(sessions, { child: { status: '' } })).toEqual(new Set())
  })

  it('handles deep shared ancestry and malformed cycles without recursion or repeated walks', () => {
    const depth = 20_000
    const sessions = Array.from({ length: depth }, (_, at) => session(String(at), at ? { subagent_parent_id: String(at - 1) } : {}))
    const active = activeSessionTreeIDs(sessions, { [String(depth - 1)]: { status: 'running' }, [String(depth - 2)]: { status: 'running' } })
    expect(active.size).toBe(depth)
    expect(active.has('0')).toBe(true)
    expect(activeSessionTreeIDs([session('a', { subagent_parent_id: 'b' }), session('b', { subagent_parent_id: 'a' })], { a: { status: 'running' } })).toEqual(new Set(['a', 'b']))
  })
})
