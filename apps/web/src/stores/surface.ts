import { ref } from 'vue'
import { defineStore } from 'pinia'
import type { FileRef } from '../lib/types'
import type { SubagentReference } from '../lib/subagents'
export interface SurfaceSession { id: string; title: string }
export type Surface =
  | { kind: 'file'; id: string; sessionID: string; ref?: FileRef }
  | { kind: 'subagent'; id: string; title: string; path: SurfaceSession[] }
export const useSurfaceStore = defineStore('surface', () => {
  const current = ref<Surface>()
  const width = ref(520)
  function open(value: Surface) { current.value = value }
  function openSubagent(child: SubagentReference, parent: SurfaceSession) {
    const previous = current.value
    const parentIndex = previous?.kind === 'subagent' ? previous.path.findIndex(item => item.id === parent.id) : -1
    const path = previous?.kind === 'subagent' && parentIndex >= 0 ? previous.path.slice(0, parentIndex + 1) : [parent]
    const existing = path.findIndex(item => item.id === child.sessionID)
    const next = existing >= 0 ? path.slice(0, existing + 1) : [...path, { id: child.sessionID, title: child.title }]
    current.value = { kind: 'subagent', id: child.sessionID, title: child.title, path: next }
  }
  function backToSession(index: number) {
    if (current.value?.kind !== 'subagent') return
    if (index === 0) { close(); return }
    const path = current.value.path.slice(0, index + 1)
    const session = path.at(-1)
    if (session) current.value = { kind: 'subagent', id: session.id, title: session.title, path }
  }
  function close() { current.value = undefined }
  return { current, width, open, openSubagent, backToSession, close }
})
