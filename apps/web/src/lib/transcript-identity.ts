import { toRaw } from 'vue'
import type { ProcessItem, SessionState, TranscriptBlock, TranscriptGroup } from './session-state'

interface Presentation { groups: TranscriptGroup[]; inputs: TranscriptGroup[]; serial: number }
const presentations = new WeakMap<SessionState, Presentation>()

export function carryTranscriptIdentity(previous: SessionState, next: SessionState) {
  const presentation = presentations.get(toRaw(previous))
  if (presentation) presentations.set(toRaw(next), presentation)
}
function sameArray<T>(a: T[], b: T[]) { return a.length === b.length && a.every((item, index) => item === b[index]) }
function sameParams(a: TranscriptGroup['statusParams'], b: TranscriptGroup['statusParams']) {
  return a === b || (!!a && !!b && Object.keys(a).length === Object.keys(b).length && Object.keys(a).every(key => a[key] === b[key]))
}
function itemIdentity(group: TranscriptGroup) {
  return [...group.entries.map(entry => `entry:${entry.id}`), ...group.blocks.flatMap(block => block.kind === 'text' ? [`text:${block.key}`] : block.items.map(item => `item:${item.key}`))]
}
function sameItem(a: ProcessItem, b: ProcessItem | undefined) {
  return b && a.kind === b.kind && a.key === b.key && (a.kind === 'tool' && b.kind === 'tool' ? a.entry === b.entry : a.kind === 'reasoning' && b.kind === 'reasoning' && a.text === b.text && a.streaming === b.streaming)
}
function processBoundaries(blocks: TranscriptBlock[]) {
  let boundary = 'start'
  return blocks.map(block => { if (block.kind === 'text') boundary = block.key; return boundary })
}

function reconcileBlocks(group: TranscriptGroup, previous: TranscriptGroup | undefined, newKey: (key: string) => string) {
  if (!previous) return group.blocks.map(block => block.kind === 'process' ? { ...block, key: newKey(`${group.key}:process`) } : block)
  const oldProcesses = previous.blocks.filter(block => block.kind === 'process')
  const byItem = new Map(oldProcesses.flatMap(block => block.items.map(item => [item.key, block] as const)))
  const matches = new Map<TranscriptBlock, TranscriptBlock>()
  const used = new Set<TranscriptBlock>()
  // Claim content matches before assigning empty status placeholders. Loading an
  // earlier process must never steal an already expanded process's identity.
  for (const block of group.blocks) {
    if (block.kind !== 'process') continue
    const old = block.items.map(item => byItem.get(item.key)).find(candidate => candidate && !used.has(candidate))
    if (old) { matches.set(block, old); used.add(old) }
  }
  const oldBoundaries = processBoundaries(previous.blocks)
  const boundaries = processBoundaries(group.blocks)
  for (const [index, block] of group.blocks.entries()) {
    if (block.kind !== 'process' || matches.has(block)) continue
    const old = previous.blocks.find((candidate, oldIndex) => candidate.kind === 'process' && !used.has(candidate) && oldBoundaries[oldIndex] === boundaries[index])
    if (old) { matches.set(block, old); used.add(old) }
  }
  const remaining = group.blocks.filter((block): block is Extract<TranscriptBlock, { kind: 'process' }> => block.kind === 'process' && !matches.has(block))
  const available = oldProcesses.filter(block => !used.has(block))
  if (remaining.length === 1 && available.length === 1 && (!remaining[0]!.items.length || !available[0]!.items.length)) matches.set(remaining[0]!, available[0]!)
  const textByKey = new Map(previous.blocks.filter(block => block.kind === 'text').map(block => [block.key, block]))
  return group.blocks.map(block => {
    if (block.kind === 'text') {
      const old = textByKey.get(block.key)
      return old && old.text === block.text && old.streaming === block.streaming ? old : block
    }
    const old = matches.get(block)
    if (old?.kind === 'process') {
      if (old.active === block.active && old.items.length === block.items.length && block.items.every((item, index) => sameItem(item, old.items[index]))) return old
      return { ...block, key: old.key }
    }
    return { ...block, key: newKey(`${group.key}:process`) }
  })
}

// Pagination can reveal a preceding user or an earlier text boundary. Neither
// changes the identity of content already on screen. Keep just the last rendered
// groups, so this presentation bookkeeping has the same lifetime as the state.
export function reconcileTranscriptGroups(state: SessionState, groups: TranscriptGroup[]) {
  const raw = toRaw(state)
  const presentation = presentations.get(raw) ?? { groups: [], inputs: [], serial: 0 }
  const inputs = groups
  let prefix = 0
  while (prefix < groups.length && groups[prefix] === presentation.inputs[prefix]) prefix++
  const stablePrefix = presentation.groups.slice(0, prefix)
  const previous = presentation.groups.slice(prefix)
  groups = groups.slice(prefix)
  const byItem = new Map(previous.flatMap(group => itemIdentity(group).map(key => [key, group] as const)))
  const matches = new Map<TranscriptGroup, TranscriptGroup>()
  const used = new Set<TranscriptGroup>()
  for (const group of groups) {
    const old = itemIdentity(group).map(key => byItem.get(key)).find(candidate => candidate && !used.has(candidate) && candidate.role === group.role && candidate.runID === group.runID)
    if (old) { matches.set(group, old); used.add(old) }
  }
  for (const group of groups) {
    if (matches.has(group)) continue
    const old = previous.find(candidate => !used.has(candidate) && candidate.key === group.key && candidate.role === group.role && candidate.runID === group.runID)
    if (old) { matches.set(group, old); used.add(old) }
  }
  for (const group of groups) {
    if (matches.has(group) || group.role !== 'assistant') continue
    const available = previous.filter(candidate => !used.has(candidate) && candidate.role === 'assistant' && candidate.runID === group.runID)
    const pending = groups.filter(candidate => !matches.has(candidate) && candidate.role === 'assistant' && candidate.runID === group.runID)
    if (available.length === 1 && pending.length === 1) { matches.set(group, available[0]!); used.add(available[0]!) }
  }
  const keys = new Set([...matches.values()].map(group => group.key))
  const newKey = (base: string) => `${base}:${++presentation.serial}`
  const stable = groups.map(candidate => {
    const old = matches.get(candidate)
    let key = old?.key ?? candidate.key
    if (!old && keys.has(key)) key = newKey(key)
    keys.add(key)
    const group = key === candidate.key ? candidate : { ...candidate, key }
    const blocks = reconcileBlocks(group, old, newKey)
    if (old && sameArray(group.entries, old.entries) && sameArray(blocks, old.blocks) && group.active === old.active && group.status === old.status && sameParams(group.statusParams, old.statusParams)) return old
    return sameArray(blocks, group.blocks) ? group : { ...group, blocks }
  })
  presentation.inputs = inputs
  presentation.groups = [...stablePrefix, ...stable]
  presentations.set(raw, presentation)
  return presentation.groups
}
