import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import type { FileRef } from '../lib/types'
import type { SubagentReference } from '../lib/subagents'

export interface SurfaceSession { id: string; title: string }
export type Surface =
  | { kind: 'file'; id: string; sessionID: string; ref?: FileRef }
  | { kind: 'subagent'; id: string; title: string; path: SurfaceSession[] }
  | { kind: 'subagents'; id: string }
export type SurfaceTab = Surface & { key: string }
interface Workspace { tabs: SurfaceTab[]; activeKey: string; visible: boolean; directoryOpen: boolean; accessed: number }

export const useSurfaceStore = defineStore('surface', () => {
  const workspaces = ref<Record<string, Workspace>>({})
  const contextID = ref('')
  const inferredID = ref('')
  const rootID = computed(() => contextID.value || inferredID.value)
  const width = ref(620)
  const directoryWidth = ref(200)
  const epoch = ref(0)
  let clock = 0
  const workspace = computed(() => workspaces.value[rootID.value])
  const tabs = computed(() => workspace.value?.tabs || [])
  const selected = computed(() => tabs.value.find(tab => tab.key === workspace.value?.activeKey))
  const current = computed(() => workspace.value?.visible ? selected.value : undefined)
  const directoryOpen = computed({ get: () => workspace.value?.directoryOpen ?? true, set: value => { if (workspace.value) workspace.value.directoryOpen = value } })
  function ensure(id: string) {
    if (!workspaces.value[id]) workspaces.value[id] = { tabs: [], activeKey: '', visible: false, directoryOpen: true, accessed: 0 }
    // Read back through the reactive container. The RHS of an assignment is
    // still raw, so returning it would bypass synchronous workspace observers.
    const value = workspaces.value[id]!
    value.accessed = ++clock
    const stale = Object.entries(workspaces.value).filter(([key]) => key !== id).sort((a, b) => a[1].accessed - b[1].accessed)
    while (Object.keys(workspaces.value).length > 8 && stale.length) delete workspaces.value[stale.shift()![0]]
    return value
  }
  function setSession(id: string) {
    contextID.value = id
    inferredID.value = ''
    if (id && workspaces.value[id]) ensure(id)
  }
  function tabKey(value: Surface) { return `${value.kind}:${value.kind === 'file' ? `${value.sessionID}:` : ''}${value.id}` }
  // Async file operations attach to their originating workspace without
  // changing the current conversation or reopening a dismissed panel.
  function open(value: Surface, ownerID?: string) {
    const owner = ownerID || contextID.value || (value.kind === 'file' ? value.sessionID : value.kind === 'subagent' ? value.path[0]!.id : value.id)
    if (!ownerID) inferredID.value = owner
    const target = ownerID ? workspaces.value[owner] : ensure(owner)
    if (!target) return
    const key = tabKey(value)
    const index = target.tabs.findIndex(tab => tab.key === key || (tab.kind === 'file' && value.kind === 'file' && (tab.id === value.id || (tab.ref && value.ref && tab.ref.computer_id === value.ref.computer_id && tab.ref.path === value.ref.path && (tab.ref.version || '') === (value.ref.version || '')))))
    const activeKey = index >= 0 ? target.tabs[index]!.key : key
    if (index >= 0) {
      const previous = target.tabs[index]!
      if (!(previous.kind === 'file' && previous.ref && value.kind === 'file' && value.ref)) target.tabs[index] = { ...previous, ...value, key: activeKey } as SurfaceTab
    }
    else target.tabs.push({ ...value, key })
    target.activeKey = activeKey
    if (!ownerID) target.visible = true
  }
  function select(key: string) {
    if (!workspace.value || !tabs.value.some(tab => tab.key === key)) return
    workspace.value.activeKey = key
    workspace.value.visible = true
  }
  function updateFile(key: string, file: FileRef, owner = rootID.value) {
    const value = workspaces.value[owner]
    const tab = value?.tabs.find(tab => tab.key === key)
    if (!value || tab?.kind !== 'file' || tab.id !== file.id) return
    tab.ref = file
    const duplicate = value.tabs.find(other => other.key !== key && other.kind === 'file' && other.ref?.computer_id === file.computer_id && other.ref.path === file.path && (other.ref.version || '') === (file.version || ''))
    if (!duplicate) return
    value.tabs = value.tabs.filter(other => other.key !== key)
    if (value.activeKey === key) value.activeKey = duplicate.key
  }
  function openSubagent(child: SubagentReference, parent: SurfaceSession) {
    const previous = selected.value
    const known = tabs.value.find(tab => tab.kind === 'subagent' && tab.id === parent.id)
    const source = previous?.kind === 'subagent' && previous.path.some(item => item.id === parent.id) ? previous : known?.kind === 'subagent' ? known : undefined
    const parentIndex = source?.path.findIndex(item => item.id === parent.id) ?? -1
    const path = source && parentIndex >= 0 ? source.path.slice(0, parentIndex + 1) : [parent]
    const existing = path.findIndex(item => item.id === child.sessionID)
    const next = existing >= 0 ? path.slice(0, existing + 1) : [...path, { id: child.sessionID, title: child.title }]
    open({ kind: 'subagent', id: child.sessionID, title: child.title, path: next })
  }
  function backToSession(index: number) {
    if (selected.value?.kind !== 'subagent') return
    if (index === 0) { close(); return }
    const path = selected.value.path.slice(0, index + 1)
    const session = path.at(-1)
    if (session) open({ kind: 'subagent', id: session.id, title: session.title, path })
  }
  function closeTab(key: string) {
    const value = workspace.value
    if (!value) return
    const index = value.tabs.findIndex(tab => tab.key === key)
    if (index < 0) return
    value.tabs.splice(index, 1)
    if (value.activeKey === key) value.activeKey = value.tabs[Math.min(index, value.tabs.length - 1)]?.key || ''
    if (!value.tabs.length) value.visible = false
  }
  function close() { if (workspace.value) workspace.value.visible = false }
  function reopen() { if (workspace.value?.tabs.length) workspace.value.visible = true; else if (rootID.value) open({ kind: 'subagents', id: rootID.value }) }
  function forgetSessions(ids: Set<string>) {
    for (const [id, value] of Object.entries(workspaces.value)) {
      if (ids.has(id)) { delete workspaces.value[id]; continue }
      value.tabs = value.tabs.filter(tab => tab.kind === 'file' ? !ids.has(tab.sessionID) : tab.kind === 'subagents' ? !ids.has(tab.id) : !tab.path.some(item => ids.has(item.id)))
      if (!value.tabs.some(tab => tab.key === value.activeKey)) value.activeKey = value.tabs.at(-1)?.key || ''
      if (!value.activeKey) value.visible = false
    }
  }
  function clear() { workspaces.value = {}; contextID.value = ''; inferredID.value = ''; epoch.value++ }
  function workspaceFor(id: string) { return workspaces.value[id] }
  return { rootID, tabs, selected, current, width, directoryWidth, directoryOpen, epoch, workspaceFor, setSession, open, select, updateFile, openSubagent, backToSession, closeTab, close, reopen, forgetSessions, clear }
})
