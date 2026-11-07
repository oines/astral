import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { watch } from 'vue'
import type { FileRef } from '../lib/types'
import { useSurfaceStore } from './surface'

function file(id: string, extra: Partial<FileRef> = {}): FileRef {
  return { id, revision: 1, created_at: '', updated_at: '', agent_id: 'bot', computer_id: 'local', path: `/project/${id}.md`, display_name: 'report.md', media_type: 'text/markdown', size: 12, ...extra }
}
function child(id: string, parent = 'root') { return { sessionID: id, parentSessionID: parent, title: id, status: 'running' } }

beforeEach(() => { setActivePinia(createPinia()) })

describe('the contextual workspace', () => {
  it('publishes the first file immediately when layout observes the current tab synchronously', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    const changes: Array<string | undefined> = []
    const stop = watch(() => surface.current?.key, value => changes.push(value), { flush: 'sync', immediate: true })
    try {
      surface.open({ kind: 'file', id: 'first-file', sessionID: 'root', ref: file('first-file') })
      expect(surface.current).toMatchObject({ kind: 'file', id: 'first-file' })
      const key = surface.current!.key
      expect(changes.at(-1)).toBe(key)
      surface.close()
      expect(surface.current).toBeUndefined()
      expect(changes.at(-1)).toBeUndefined()
      surface.reopen()
      expect(surface.current?.key).toBe(key)
      expect(changes.at(-1)).toBe(key)
    } finally { stop() }
  })

  it('publishes the initial subagent list when an empty workspace is reopened under synchronous observation', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    const changes: Array<string | undefined> = []
    const stop = watch(() => surface.current?.key, value => changes.push(value), { flush: 'sync', immediate: true })
    try {
      surface.reopen()
      expect(surface.current).toMatchObject({ kind: 'subagents', id: 'root' })
      const key = surface.current!.key
      expect(changes.at(-1)).toBe(key)
      surface.close()
      expect(surface.current).toBeUndefined()
      surface.reopen()
      expect(surface.current?.key).toBe(key)
      expect(changes.at(-1)).toBe(key)
    } finally { stop() }
  })

  it('reuses the same reference when it is opened again through another conversation entry', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    surface.open({ kind: 'file', id: 'reference', sessionID: 'root' })
    const key = surface.current!.key
    surface.open({ kind: 'file', id: 'reference', sessionID: 'child', ref: file('reference') })
    expect(surface.tabs).toHaveLength(1)
    expect(surface.current).toMatchObject({ key, id: 'reference', sessionID: 'child', ref: { computer_id: 'local' } })
  })

  it('deduplicates matching Computer paths and versions while keeping different snapshots separate', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    const original = file('first', { path: '/project/report.md' })
    surface.open({ kind: 'file', id: original.id, sessionID: 'root', ref: original })
    const key = surface.current!.key
    const equivalent = file('second', { path: original.path, version: '' })
    surface.open({ kind: 'file', id: equivalent.id, sessionID: 'root', ref: equivalent })
    expect(surface.tabs).toHaveLength(1)
    // Keep the loaded reference and renderer; an equivalent link should
    // activate that preview without changing its resource or reading position.
    expect(surface.current).toMatchObject({ key, id: 'first' })
    for (const version of ['snapshot-one', 'snapshot-two']) {
      const snapshot = file(version, { path: original.path, version })
      surface.open({ kind: 'file', id: snapshot.id, sessionID: 'root', ref: snapshot })
    }
    expect(surface.tabs.map(tab => tab.id)).toEqual(['first', 'snapshot-one', 'snapshot-two'])
  })

  it('keeps same-name and same-path files on different Computers in different tabs', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    for (const computer of ['local', 'office']) {
      const ref = file(`reference-${computer}`, { computer_id: computer, path: '/project/report.md' })
      surface.open({ kind: 'file', id: ref.id, sessionID: 'root', ref })
    }
    expect(surface.tabs).toHaveLength(2)
    expect(new Set(surface.tabs.map(tab => tab.key)).size).toBe(2)
    expect(surface.tabs.map(tab => tab.kind === 'file' && tab.ref?.computer_id)).toEqual(['local', 'office'])
  })

  it('merges different links after their metadata resolves to the same Computer path and version', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    surface.open({ kind: 'file', id: 'first-link', sessionID: 'root' })
    const first = surface.current!.key
    surface.open({ kind: 'file', id: 'second-link', sessionID: 'root' })
    const second = surface.current!.key
    surface.updateFile(second, file('second-link', { path: '/project/report.md', version: 'same-version' }), 'root')
    surface.updateFile(first, file('first-link', { path: '/project/report.md', version: 'same-version' }), 'root')
    expect(surface.tabs).toHaveLength(1)
    expect(surface.current).toMatchObject({ kind: 'file', ref: { computer_id: 'local', path: '/project/report.md', version: 'same-version' } })
  })

  it('preserves the directory switch while opening and selecting file and subagent tabs', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    surface.open({ kind: 'file', id: 'first', sessionID: 'root', ref: file('first') })
    const first = surface.current!.key
    surface.directoryOpen = false
    surface.open({ kind: 'file', id: 'second', sessionID: 'root', ref: file('second', { computer_id: 'office' }) })
    expect(surface.directoryOpen).toBe(false)
    surface.openSubagent(child('child'), { id: 'root', title: 'Work' })
    expect(surface.directoryOpen).toBe(false)
    surface.select(first)
    expect(surface.current).toMatchObject({ kind: 'file', id: 'first' })
    expect(surface.directoryOpen).toBe(false)
  })

  it('closes the whole panel without losing tabs, selection or the directory preference', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    surface.open({ kind: 'file', id: 'first', sessionID: 'root' })
    surface.openSubagent(child('child'), { id: 'root', title: 'Work' })
    surface.directoryOpen = false
    const activeKey = surface.current!.key
    surface.close()
    expect(surface.current).toBeUndefined()
    expect(surface.tabs).toHaveLength(2)
    expect(surface.selected?.key).toBe(activeKey)
    surface.reopen()
    expect(surface.current?.key).toBe(activeKey)
    expect(surface.directoryOpen).toBe(false)
  })

  it('removes only the requested tab and keeps another selected tab when possible', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    for (const id of ['first', 'second', 'third']) surface.open({ kind: 'file', id, sessionID: 'root' })
    const [first, second, third] = surface.tabs.map(tab => tab.key) as [string, string, string]
    surface.closeTab(first)
    expect(surface.tabs.map(tab => tab.id)).toEqual(['second', 'third'])
    expect(surface.current?.key).toBe(third)
    surface.closeTab('missing-tab')
    expect(surface.current?.key).toBe(third)
    surface.closeTab(third)
    expect(surface.current?.key).toBe(second)
    expect(surface.tabs.map(tab => tab.id)).toEqual(['second'])
    surface.closeTab(second)
    expect(surface.tabs).toEqual([])
    expect(surface.current).toBeUndefined()
  })

  it('restores each main Session workspace with its own visibility, selection and directory switch', () => {
    const surface = useSurfaceStore()
    surface.setSession('first-root')
    surface.open({ kind: 'file', id: 'first-file', sessionID: 'first-root' })
    surface.directoryOpen = false
    surface.close()
    surface.setSession('second-root')
    expect(surface.tabs).toEqual([])
    expect(surface.current).toBeUndefined()
    expect(surface.directoryOpen).toBe(true)
    surface.openSubagent(child('second-child', 'second-root'), { id: 'second-root', title: 'Second work' })
    const secondKey = surface.current!.key
    surface.setSession('first-root')
    expect(surface.tabs.map(tab => tab.id)).toEqual(['first-file'])
    expect(surface.current).toBeUndefined()
    expect(surface.directoryOpen).toBe(false)
    surface.reopen()
    expect(surface.current).toMatchObject({ id: 'first-file' })
    surface.setSession('second-root')
    expect(surface.current?.key).toBe(secondKey)
    expect(surface.directoryOpen).toBe(true)
    surface.setSession('')
    expect(surface.rootID).toBe('')
    expect(surface.current).toBeUndefined()
  })

  it('applies late metadata only to its captured owner instead of the later workspace', async () => {
    const surface = useSurfaceStore()
    surface.setSession('first-root')
    surface.open({ kind: 'file', id: 'shared-file', sessionID: 'source-session' })
    const owner = surface.rootID
    const key = surface.current!.key
    let finish!: (value: FileRef) => void
    const pending = new Promise<FileRef>(resolve => { finish = resolve }).then(ref => surface.updateFile(key, ref, owner))
    surface.setSession('second-root')
    surface.open({ kind: 'file', id: 'shared-file', sessionID: 'source-session' })
    expect(surface.current?.key).toBe(key)
    finish(file('shared-file'))
    await pending
    expect(surface.current).not.toHaveProperty('ref')
    expect(surface.workspaceFor(owner)?.tabs[0]).toHaveProperty('ref', file('shared-file'))
  })

  it('attaches an asynchronous directory file to its originating workspace after changing conversations', async () => {
    const surface = useSurfaceStore()
    surface.setSession('first-root')
    surface.open({ kind: 'file', id: 'first-source', sessionID: 'first-child', ref: file('first-source', { computer_id: 'office' }) })
    const owner = surface.rootID
    let finish!: (value: FileRef) => void
    const pending = new Promise<FileRef>(resolve => { finish = resolve }).then(ref => {
      surface.open({ kind: 'file', id: ref.id, sessionID: 'first-child', ref }, owner)
    })

    surface.setSession('second-root')
    surface.open({ kind: 'file', id: 'second-source', sessionID: 'second-root', ref: file('second-source', { computer_id: 'local' }) })
    surface.directoryOpen = false
    const secondKey = surface.current!.key
    const resolved = file('late-office-file', { computer_id: 'office' })
    finish(resolved)
    await pending

    expect(surface.rootID).toBe('second-root')
    expect(surface.current).toMatchObject({ key: secondKey, id: 'second-source', sessionID: 'second-root', ref: { computer_id: 'local' } })
    expect(surface.tabs.map(tab => tab.id)).toEqual(['second-source'])
    expect(surface.directoryOpen).toBe(false)
    expect(surface.workspaceFor(owner)?.tabs.map(tab => tab.id)).toEqual(['first-source', resolved.id])
    surface.setSession(owner)
    expect(surface.current).toMatchObject({ id: resolved.id, sessionID: 'first-child', ref: resolved })
  })

  it('retains a late directory file without reopening a panel the user closed', async () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    surface.open({ kind: 'file', id: 'source', sessionID: 'root', ref: file('source') })
    const owner = surface.rootID
    let finish!: (value: FileRef) => void
    const pending = new Promise<FileRef>(resolve => { finish = resolve }).then(ref => {
      surface.open({ kind: 'file', id: ref.id, sessionID: 'root', ref }, owner)
    })

    surface.close()
    const resolved = file('late-file')
    finish(resolved)
    await pending

    expect(surface.rootID).toBe(owner)
    expect(surface.current).toBeUndefined()
    expect(surface.workspaceFor(owner)?.visible).toBe(false)
    expect(surface.tabs.map(tab => tab.id)).toEqual(['source', resolved.id])
    surface.reopen()
    expect(surface.current).toMatchObject({ id: resolved.id, ref: resolved })
  })

  it('discards a late directory file when its originating workspace has been forgotten', async () => {
    const surface = useSurfaceStore()
    surface.setSession('removed-root')
    surface.open({ kind: 'file', id: 'removed-source', sessionID: 'removed-root', ref: file('removed-source') })
    const owner = surface.rootID
    let finish!: (value: FileRef) => void
    const pending = new Promise<FileRef>(resolve => { finish = resolve }).then(ref => {
      surface.open({ kind: 'file', id: ref.id, sessionID: 'removed-root', ref }, owner)
    })

    surface.forgetSessions(new Set([owner]))
    surface.setSession('remaining-root')
    surface.open({ kind: 'subagents', id: 'remaining-root' })
    const remainingKey = surface.current!.key
    finish(file('late-removed-file'))
    await pending

    expect(surface.workspaceFor(owner)).toBeUndefined()
    expect(surface.rootID).toBe('remaining-root')
    expect(surface.current).toMatchObject({ key: remainingKey, kind: 'subagents', id: 'remaining-root' })
    expect(surface.tabs.map(tab => tab.id)).toEqual(['remaining-root'])
  })

  it('ignores metadata for an equivalent reference and does not recreate a forgotten workspace', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    const original = file('original', { path: '/project/report.md' })
    surface.open({ kind: 'file', id: original.id, sessionID: 'root', ref: original })
    const key = surface.current!.key
    const replacement = file('replacement', { path: original.path })
    surface.open({ kind: 'file', id: replacement.id, sessionID: 'root', ref: replacement })
    surface.updateFile(key, replacement, 'root')
    expect(surface.current).toMatchObject({ id: original.id, ref: original })
    surface.forgetSessions(new Set(['root']))
    surface.updateFile(key, replacement, 'root')
    expect(surface.workspaceFor('root')).toBeUndefined()
    expect(surface.current).toBeUndefined()
  })

  it('removes deleted descendants and their file tabs while keeping unrelated tabs and roots', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    surface.open({ kind: 'file', id: 'root-file', sessionID: 'root' })
    surface.openSubagent(child('child'), { id: 'root', title: 'Work' })
    surface.openSubagent(child('grandchild', 'child'), { id: 'child', title: 'Child work' })
    surface.open({ kind: 'file', id: 'child-file', sessionID: 'child' })
    surface.openSubagent(child('sibling'), { id: 'root', title: 'Work' })
    const siblingKey = surface.current!.key
    surface.setSession('other-root')
    surface.open({ kind: 'file', id: 'other-file', sessionID: 'other-root' })
    surface.forgetSessions(new Set(['child']))
    expect(surface.current).toMatchObject({ id: 'other-file' })
    surface.setSession('root')
    expect(surface.tabs.map(tab => tab.id)).toEqual(['root-file', 'sibling'])
    expect(surface.current?.key).toBe(siblingKey)
  })

  it('clears all workspace data and invalidates pending work after an identity reset', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    surface.open({ kind: 'file', id: 'root-file', sessionID: 'root' })
    surface.setSession('other-root')
    surface.reopen()
    expect(surface.current).toMatchObject({ kind: 'subagents', id: 'other-root' })
    const epoch = surface.epoch
    surface.clear()
    expect(surface.epoch).toBe(epoch + 1)
    expect(surface.rootID).toBe('')
    expect(surface.tabs).toEqual([])
    expect(surface.current).toBeUndefined()
    expect(surface.workspaceFor('root')).toBeUndefined()
    expect(surface.workspaceFor('other-root')).toBeUndefined()
    surface.updateFile('file:root:root-file', file('root-file'), 'root')
    expect(surface.workspaceFor('root')).toBeUndefined()
  })

  it('retains descendant ancestry and other tabs when selecting an ancestor', () => {
    const surface = useSurfaceStore()
    surface.setSession('root')
    surface.openSubagent(child('child'), { id: 'root', title: 'Work' })
    surface.open({ kind: 'file', id: 'report', sessionID: 'root' })
    surface.openSubagent(child('grandchild', 'child'), { id: 'child', title: 'Child work' })
    expect(surface.current).toMatchObject({ id: 'grandchild', path: [{ id: 'root' }, { id: 'child' }, { id: 'grandchild' }] })
    surface.backToSession(1)
    expect(surface.current).toMatchObject({ id: 'child', path: [{ id: 'root' }, { id: 'child' }] })
    expect(surface.tabs.map(tab => tab.id)).toEqual(['child', 'report', 'grandchild'])
    surface.backToSession(0)
    expect(surface.current).toBeUndefined()
    expect(surface.tabs.map(tab => tab.id)).toEqual(['child', 'report', 'grandchild'])
  })
})
