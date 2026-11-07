import { computed, onScopeDispose, reactive, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { errorText } from '../lib/api'
import { fileService, type DirectoryEntry, type DirectoryPage, type FileService } from './useFileService'

export interface FileExplorerSource {
  computer_id: string
  path: string
}

export interface DirectoryState {
  page?: DirectoryPage
  expanded: boolean
  busy: boolean
  waiting: boolean
  error: string
}
export type FileTreeRow =
  | { kind: 'entry'; key: string; entry: DirectoryEntry; depth: number; parent: string; directory: DirectoryState }
  | { kind: 'empty' | 'error' | 'more'; key: string; path: string; depth: number; directory: DirectoryState }

// Paths are opaque paths belonging to the source Computer. Parent relationships
// and canonical paths come from that Computer, never from the browser's OS.
export function useFileExplorer(source: MaybeRefOrGetter<FileExplorerSource>, sessionID: MaybeRefOrGetter<string>, service: Pick<FileService, 'listDirectory' | 'referencePath'> = fileService) {
  const directories = reactive(new Map<string, DirectoryState>())
  const roots = new Map<string, string>()
  const rootPath = ref('')
  const locating = ref(false)
  const refreshing = ref(false)
  const error = ref('')
  const openingPath = ref('')
  const sourceKey = computed(() => `${toValue(sessionID)}\u0000${toValue(source).computer_id}`)
  const keyFor = (scope: string, path: string) => `${scope}\u0000${path}`
  const pending = new Map<string, { controller: AbortController; promise: Promise<DirectoryPage | undefined>; sequence: number }>()
  const timers = new Map<string, ReturnType<typeof setTimeout>>()
  let sequence = 0
  let destination = 0
  let refreshSequence = 0
  let openSequence = 0
  let openController: AbortController | undefined

  function directory(path: string, scope = sourceKey.value): DirectoryState {
    const key = keyFor(scope, path)
    let value = directories.get(key)
    if (!value) {
      value = { expanded: false, busy: false, waiting: false, error: '' }
      directories.set(key, value)
    }
    return directories.get(key)!
  }
  const root = computed(() => rootPath.value ? directories.get(keyFor(sourceKey.value, rootPath.value)) : undefined)

  function load(path: string, options: { append?: boolean; force?: boolean } = {}, origin = toValue(source), session = toValue(sessionID)) {
    const scope = `${session}\u0000${origin.computer_id}`
    const key = keyFor(scope, path)
    const previous = pending.get(key)
    if (previous && !options.force) return previous.promise
    previous?.controller.abort()
    const controller = new AbortController()
    const current = ++sequence
    const state = directory(path, scope)
    state.busy = true; state.waiting = false; state.error = ''
    clearTimeout(timers.get(key))
    timers.set(key, setTimeout(() => { if (pending.get(key)?.sequence === current) state.waiting = true }, 300))
    const promise = service.listDirectory(origin, session, path, { cursor: options.append ? state.page?.next_cursor : undefined, signal: controller.signal }).then(page => {
      if (pending.get(key)?.sequence !== current) return undefined
      const target = directory(page.path, scope)
      if (options.append && target.page) {
        const entries = new Map(target.page.entries.map(entry => [entry.path, entry]))
        for (const entry of page.entries) entries.set(entry.path, entry)
        target.page = { ...page, entries: [...entries.values()] }
      } else target.page = page
      target.error = ''
      return target.page
    }).catch(cause => {
      if (pending.get(key)?.sequence === current && !controller.signal.aborted) state.error = errorText(cause)
      return undefined
    }).finally(() => {
      if (pending.get(key)?.sequence === current) {
        pending.delete(key)
        clearTimeout(timers.get(key)); timers.delete(key)
        state.busy = false; state.waiting = false
      }
    })
    pending.set(key, { controller, promise, sequence: current })
    return promise
  }

  function ancestors(path: string) {
    const result: string[] = []
    const seen = new Set<string>()
    while (path && !seen.has(path)) {
      seen.add(path); result.push(path)
      path = directories.get(keyFor(sourceKey.value, path))?.page?.parent || ''
    }
    return result
  }
  async function locate() {
    const current = ++destination
    refreshSequence++; refreshing.value = false
    openSequence++; openController?.abort(); openingPath.value = ''
    const origin = toValue(source), session = toValue(sessionID), scope = sourceKey.value
    const savedRoot = roots.get(scope)
    rootPath.value = savedRoot || ''
    error.value = ''; locating.value = true
    const cached = [...directories.entries()].find(([key, value]) => key.startsWith(`${scope}\u0000`) && value.page?.entries.some(entry => !entry.is_dir && entry.path === origin.path))?.[1].page
    const page = cached || await load(origin.path, {}, origin, session)
    if (current !== destination) return
    locating.value = false
    if (!page) {
      error.value = directory(origin.path, scope).error
      return
    }
    const parents = ancestors(page.path)
    if (!savedRoot || !parents.includes(savedRoot)) rootPath.value = page.path
    roots.set(scope, rootPath.value)
    for (const path of parents) {
      directory(path).expanded = true
      if (path === rootPath.value) break
    }
  }
  watch([() => sourceKey.value, () => toValue(source).path], locate, { immediate: true, flush: 'sync' })

  async function toggle(path: string) {
    const state = directory(path)
    state.expanded = !state.expanded
    if (state.expanded && !state.page) await load(path)
  }
  async function parent() {
    const path = root.value?.page?.parent
    if (!path) return
    const scope = sourceKey.value
    const current = ++destination
    locating.value = true; error.value = ''
    const page = directory(path).page || await load(path)
    if (current !== destination) return
    locating.value = false
    if (!page) { error.value = directory(path).error; return }
    rootPath.value = page.path
    roots.set(scope, page.path)
    directory(page.path).expanded = true
  }
  async function refresh() {
    if (refreshing.value) return
    if (!rootPath.value) { await locate(); return }
    const current = ++refreshSequence
    refreshing.value = true
    error.value = ''
    const paths = new Set([rootPath.value, ...rows.value.flatMap(row => row.kind === 'entry' && row.entry.is_dir && row.directory.expanded && row.directory.page ? [row.directory.page.path] : [])])
    try { await Promise.all([...paths].map(path => load(path, { force: true }))) }
    finally { if (current === refreshSequence) refreshing.value = false }
  }
  async function open(entry: DirectoryEntry) {
    if (entry.is_dir) { await toggle(entry.path); return undefined }
    const current = ++openSequence
    const scope = sourceKey.value
    openController?.abort()
    const controller = new AbortController()
    openController = controller
    openingPath.value = entry.path; error.value = ''
    try {
      const file = await service.referencePath(toValue(source), toValue(sessionID), entry.path, controller.signal)
      if (current === openSequence && scope === sourceKey.value) return file
    } catch (cause) {
      if (current === openSequence && !controller.signal.aborted) error.value = errorText(cause)
    } finally {
      if (current === openSequence) openingPath.value = ''
    }
  }
  const rows = computed(() => {
    const result: FileTreeRow[] = []
    const seen = new Set<string>()
    function visit(path: string, depth: number) {
      if (seen.has(path)) return
      seen.add(path)
      const state = directories.get(keyFor(sourceKey.value, path))
      if (!state) return
      for (const entry of state.page?.entries || []) {
        const child = directories.get(keyFor(sourceKey.value, entry.path)) || { expanded: false, busy: false, waiting: false, error: '' }
        result.push({ kind: 'entry', key: keyFor(sourceKey.value, entry.path), entry, depth, parent: path, directory: child })
        if (entry.is_dir && child.expanded) visit(entry.path, depth + 1)
      }
      if (state.error) result.push({ kind: 'error', key: `${path}:error`, path, depth, directory: state })
      else if (state.page && state.page.entries.length === 0) result.push({ kind: 'empty', key: `${path}:empty`, path, depth, directory: state })
      if (state.page?.next_cursor) result.push({ kind: 'more', key: `${path}:more`, path, depth, directory: state })
    }
    if (rootPath.value) visit(rootPath.value, 0)
    return result
  })
  onScopeDispose(() => {
    destination++; openSequence++; openController?.abort()
    for (const request of pending.values()) request.controller.abort()
    for (const timer of timers.values()) clearTimeout(timer)
  })
  return { root, rootPath, rows, locating, refreshing, error, openingPath, toggle, parent, refresh, open, retry: (path: string) => load(path, { force: true }), more: (path: string) => load(path, { append: true }) }
}
