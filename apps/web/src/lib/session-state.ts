import type { Entry, SessionEvent, SessionView, Snapshot } from './types'
import { object } from './api'

export interface PreviewPart { type: string; text: string }
export interface Preview { runID: string; parts: Record<number, PreviewPart> }
export interface LiveTool { name: string; text: string; started: boolean; ended: boolean }
export interface SessionState {
  epoch: string
  sequence: number
  view?: SessionView
  entries: Record<string, Entry>
  trace: Record<string, Entry>
  previews: Record<string, Preview>
  tools: Record<string, LiveTool>
  completedAttempts: Set<string>
  completedRuns: Set<string>
  historyCursor?: string
  traceCursor?: string
}
export function emptyState(): SessionState {
  return { epoch: '', sequence: 0, entries: {}, trace: {}, previews: {}, tools: {}, completedAttempts: new Set(), completedRuns: new Set() }
}
export function addEntry(state: SessionState, entry: Entry, history = false) {
  state.trace[entry.id] = entry
  if (history || ['message', 'context', 'compaction'].includes(entry.kind)) state.entries[entry.id] = entry
  const attempt = entry.model_response?.attempt_id
  if (attempt) { state.completedAttempts.add(attempt); delete state.previews[attempt] }
  if (entry.run && entry.run.status !== 'started') {
    state.completedRuns.add(entry.run.id)
    for (const [id, p] of Object.entries(state.previews)) if (p.runID === entry.run.id) delete state.previews[id]
  }
  if (entry.tool) {
    const id = entry.tool.invocation.execution_id
    const previous = state.tools[id]
    state.tools[id] = {
      name: entry.tool.invocation.call.name,
      text: previous?.text ?? '',
      started: previous?.started || entry.kind === 'tool_started' || Boolean(entry.tool.result),
      ended: previous?.ended || Boolean(entry.tool.result) || Boolean(entry.tool.control),
    }
  }
}
export function fromSnapshot(snapshot: Snapshot): SessionState {
  const state = emptyState()
  state.epoch = snapshot.epoch
  state.view = snapshot.view
  state.sequence = snapshot.view.event_sequence
  state.historyCursor = snapshot.history.next_cursor
  state.traceCursor = snapshot.trace.next_cursor
  for (const entry of snapshot.trace.entries ?? []) addEntry(state, entry)
  for (const entry of snapshot.history.entries ?? []) addEntry(state, entry, true)
  return state
}
export function applyEvent(state: SessionState, envelope: SessionEvent): boolean {
  if (envelope.epoch !== state.epoch) return false
  const e = envelope.core
  if (e && e.sequence <= state.sequence) return true
  if (envelope.view && envelope.view.event_sequence >= (state.view?.event_sequence ?? 0)) state.view = envelope.view
  if (envelope.entry) addEntry(state, envelope.entry)
  if (!e) return true
  state.sequence = e.sequence
  if (e.attempt && state.view && e.sequence >= state.view.event_sequence && !state.completedRuns.has(e.run_id ?? '')) {
    state.view = { ...state.view, attempt: e.attempt, event_sequence: e.sequence }
  }
  if (e.type === 'run_end' && e.run_id) {
    state.completedRuns.add(e.run_id)
    for (const [id, p] of Object.entries(state.previews)) if (p.runID === e.run_id) delete state.previews[id]
  }
  if (e.attempt_id && ['attempt_failed', 'attempt_end', 'message_end', 'retry'].includes(e.type)) {
    state.completedAttempts.add(e.attempt_id)
    delete state.previews[e.attempt_id]
  }
  if (e.type === 'message_update' && e.attempt_id && e.model && !state.completedAttempts.has(e.attempt_id) && !state.completedRuns.has(e.run_id ?? '')) {
    const p = state.previews[e.attempt_id] ??= { runID: e.run_id ?? '', parts: {} }
    const m = e.model
    if (m.content_index !== undefined && ['text_delta', 'reasoning_delta', 'tool_call_delta'].includes(m.type)) {
      const part = p.parts[m.content_index] ??= { type: m.type, text: '' }
      part.text += m.text ?? ''
    }
  }
  if (e.execution_id && e.type.startsWith('tool_execution')) {
    const tool = state.tools[e.execution_id] ??= { name: '', text: '', started: false, ended: false }
    if (e.type === 'tool_execution_start') tool.started = true
    if (e.type === 'tool_execution_end') tool.ended = true
    if (e.progress?.text) tool.text = (tool.text + e.progress.text).slice(-128_000)
  }
  return true
}
export function messageText(entry: Entry): string {
  const m = entry.message
  if (!m || m.is_error || (m.role === 'assistant' && ['error', 'cancelled'].includes(m.finish_reason ?? ''))) return ''
  const metadata = object(m.metadata)
  if (m.role === 'user' && typeof metadata.display_text === 'string') return metadata.display_text
  return (m.content ?? []).filter(p => p.type === 'text').map(p => p.text ?? '').join('\n')
}
export function visibleEntry(entry: Entry) {
  if (!['message', 'context'].includes(entry.kind) || !entry.message || entry.message.is_error) return false
  if (entry.message.role === 'assistant') return Boolean(messageText(entry))
  if (entry.message.role !== 'user') return false
  // IM trigger prompts reference observations already shown; never repeat the XML.
  return object(entry.message.metadata).source !== 'im.trigger' && (Boolean(messageText(entry)) || Boolean(entry.message.content?.some(p => p.type === 'image')))
}
export function sortedEntries(entries: Record<string, Entry>): Entry[] {
  return Object.values(entries).sort((a, b) => a.sequence - b.sequence)
}
export type TranscriptRow = { kind: 'message'; entry: Entry } | { kind: 'activity'; runID: string }
export function transcriptRows(state: SessionState): TranscriptRow[] {
  const messages = sortedEntries(state.entries).filter(visibleEntry)
  const activities = new Map<string, number>()
  for (const entry of messages) {
    if (entry.message?.role === 'assistant' && entry.run_id && !activities.has(entry.run_id)) activities.set(entry.run_id, entry.sequence - 0.5)
  }
  for (const entry of sortedEntries(state.trace)) {
    if (entry.run && entry.run.status !== 'started' && !activities.has(entry.run.id)) activities.set(entry.run.id, entry.sequence)
  }
  if (state.view?.run_id && !activities.has(state.view.run_id)) activities.set(state.view.run_id, Infinity)
  return [
    ...messages.map(entry => ({ order: entry.sequence, row: { kind: 'message' as const, entry } })),
    ...Array.from(activities, ([runID, order]) => ({ order, row: { kind: 'activity' as const, runID } })),
  ].sort((a, b) => a.order - b.order).map(item => item.row)
}

export type ProcessItem =
  | { kind: 'reasoning'; key: string; text: string; streaming?: boolean }
  | { kind: 'tool'; key: string; entry: Entry }
export type TranscriptBlock =
  | { kind: 'text'; key: string; text: string; streaming?: boolean }
  | { kind: 'process'; key: string; items: ProcessItem[] }
export interface TranscriptGroup {
  key: string
  role: 'user' | 'assistant'
  runID?: string
  entries: Entry[]
  blocks: TranscriptBlock[]
  status?: string
  statusParams?: Record<string, string | number>
  active?: boolean
}

export function executionStatus(view: SessionView, fallback = 'working'): Pick<TranscriptGroup, 'status' | 'statusParams'> {
  if (view.status === 'stopping') return { status: 'stopping' }
  const attempt = view.attempt
  if (attempt?.state === 'retrying') return {
    status: attempt.purpose === 'compaction' ? 'retryingCompaction' : 'retryingModel',
    statusParams: { attempt: attempt.attempt + 1, max: attempt.max_attempts, time: attempt.next_retry_at ?? '' },
  }
  if (attempt?.state === 'running') return {
    status: attempt.purpose === 'compaction' ? 'compactionAttempt' : 'modelAttempt',
    statusParams: { attempt: attempt.attempt, max: attempt.max_attempts },
  }
  return { status: fallback }
}
export function transcriptGroups(state: SessionState): TranscriptGroup[] {
  // Durable message content is the ordering authority. Model responses may be
  // failed attempts, while tool lifecycle entries repeat the same invocation.
  const entries = sortedEntries({ ...state.trace, ...state.entries })
  const tools = new Map<string, Entry>()
  for (const entry of entries) if (entry.tool) tools.set(entry.tool.invocation.execution_id, entry)
  const calls = new Map<string, Entry>()
  for (const entry of tools.values()) {
    const invocation = entry.tool!.invocation
    calls.set(`${invocation.assistant_id}:${invocation.call.id}`, entry)
  }
  const groups: TranscriptGroup[] = []
  const shownTools = new Set<string>()
  function assistant(runID: string | undefined, key: string) {
    let group = groups.at(-1)
    if (group?.role !== 'assistant' || group.runID !== runID) {
      group = { key, role: 'assistant', runID, entries: [], blocks: [] }
      groups.push(group)
    }
    return group
  }
  function process(group: TranscriptGroup, item: ProcessItem) {
    const last = group.blocks.at(-1)
    if (last?.kind === 'process') last.items.push(item)
    else group.blocks.push({ kind: 'process', key: item.key, items: [item] })
  }
  function tool(entry: Entry) {
    const invocation = entry.tool!.invocation
    if (shownTools.has(invocation.execution_id) || invocation.call.name === 'no_reply') return
    shownTools.add(invocation.execution_id)
    process(assistant(invocation.run_id, `run:${invocation.run_id}:${entry.id}`), { kind: 'tool', key: invocation.execution_id, entry })
  }
  for (const entry of entries) {
    const message = entry.message
    if (message?.role === 'user' && visibleEntry(entry)) {
      groups.push({ key: entry.id, role: 'user', entries: [entry], blocks: [] })
    } else if (entry.kind === 'message' && message?.role === 'assistant' && !message.is_error && !['error', 'cancelled'].includes(message.finish_reason ?? '')) {
      const group = assistant(entry.run_id, `run:${entry.run_id}:${entry.id}`)
      group.entries.push(entry)
      for (const [index, part] of (message.content ?? []).entries()) {
        const key = `${entry.id}:${index}`
        if (part.type === 'text' && part.text) group.blocks.push({ kind: 'text', key, text: part.text })
        else if (part.type === 'reasoning' && part.text) process(group, { kind: 'reasoning', key, text: part.text })
        else if (part.type === 'tool_call' && part.tool_call) {
          const execution = calls.get(`${entry.id}:${part.tool_call.id}`)
          if (execution) tool(execution)
        }
      }
    } else if (entry.tool) {
      tool(tools.get(entry.tool.invocation.execution_id)!)
    } else if (entry.run && entry.run.status !== 'started') {
      const status = entry.run.status === 'failed' ? 'needs_attention' : entry.run.status === 'cancelled' ? 'stopped' : entry.run.end_reason === 'no_reply' ? 'endedSilently' : undefined
      if (status) assistant(entry.run.id, `run:${entry.run.id}:${entry.id}`).status = status
    }
  }
  for (const [attempt, preview] of Object.entries(state.previews)) {
    const group = assistant(preview.runID, `run:${preview.runID}:live`)
    for (const [index, part] of Object.entries(preview.parts).sort(([a], [b]) => Number(a) - Number(b))) {
      const key = `${attempt}:${index}`
      if (part.type === 'text_delta' && part.text) group.blocks.push({ kind: 'text', key, text: part.text, streaming: true })
      else if (part.type === 'reasoning_delta' && part.text) process(group, { kind: 'reasoning', key, text: part.text, streaming: true })
    }
  }
  const runID = state.view?.run_id
  if (runID && !state.completedRuns.has(runID)) {
    const group = assistant(runID, `run:${runID}:live`)
    group.active = true
    const preparing = Object.values(state.previews).some(p => p.runID === runID && Object.values(p.parts).some(part => part.type === 'tool_call_delta'))
    Object.assign(group, executionStatus(state.view!, preparing ? 'preparing' : 'working'))
  } else if (state.view && ['compacting', 'stopping'].includes(state.view.status) && !runID) {
    groups.push({ key: 'maintenance', role: 'assistant', entries: [], blocks: [], active: true, ...executionStatus(state.view, 'compacting') })
  }
  return groups.filter(group => group.role === 'user' || group.blocks.length || group.status)
}
