import { effectScope, shallowRef } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import type { FileRef } from '../lib/types'
import { useFileExplorer, type FileExplorerSource } from './useFileExplorer'
import type { DirectoryEntry, DirectoryPage, FileService } from './useFileService'
vi.mock('@astral/sdk', () => ({ fileContent: vi.fn(), getFileReference: vi.fn(), listComputerDirectory: vi.fn(), referenceComputerFile: vi.fn() }))
const file = (computer: string, path: string): FileRef => ({ id: `${computer}:${path}`, revision: 1, agent_id: 'bot', computer_id: computer, path, display_name: path.split(/[\\/]/).at(-1)!, media_type: 'text/plain', size: 5, created_at: '', updated_at: '' })
const entry = (path: string, directory = false): DirectoryEntry => ({ name: path.split(/[\\/]/).at(-1)!, path, is_dir: directory, size: 5, modified_at: '' })
const page = (computer_id: string, path: string, parent: string, entries: DirectoryEntry[], next_cursor?: string): DirectoryPage => ({ computer_id, path, parent, entries, next_cursor })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(yes => { resolve = yes }); return { promise, resolve } }

describe('source-bound directory browsing', () => {
  it.each([
    { name: 'the session working directory', path: '/work', canonicalPath: '/work' },
    { name: 'the server default for an empty path', path: '', canonicalPath: '/bot-home' },
  ])('browses $name without a FileRef and opens a listed file through its authorized reference', async ({ path, canonicalPath }) => {
    const origin: FileExplorerSource = { computer_id: 'session-computer', path }
    const listed = entry(`${canonicalPath}/result.md`)
    const authorized = file(origin.computer_id, listed.path)
    const listing = vi.fn<FileService['listDirectory']>(async () => page(origin.computer_id, canonicalPath, '/', [listed]))
    const reference = vi.fn<FileService['referencePath']>(async () => authorized)
    const scope = effectScope()
    const tree = scope.run(() => useFileExplorer(origin, 'authorized-session', { listDirectory: listing, referencePath: reference }))!
    await vi.waitFor(() => expect(tree.rootPath.value).toBe(canonicalPath))
    expect(listing.mock.calls[0]!.slice(0, 3)).toEqual([origin, 'authorized-session', path])
    expect(tree.rows.value.filter(row => row.kind === 'entry').map(row => row.entry.path)).toEqual([listed.path])
    expect(reference).not.toHaveBeenCalled()
    expect(await tree.open(listed)).toEqual(authorized)
    expect(reference.mock.calls[0]!.slice(0, 3)).toEqual([origin, 'authorized-session', listed.path])
    expect(origin).toEqual({ computer_id: 'session-computer', path })
    scope.stop()
  })

  it('loads a directory source instead of mistaking its cached parent entry for a located file', async () => {
    const source = shallowRef<FileExplorerSource>({ computer_id: 'computer', path: '/work/a.md' })
    const listing = vi.fn<FileService['listDirectory']>().mockImplementation(async (_source, _session, path) => path === '/work/docs'
      ? page('computer', '/work/docs', '/work', [entry('/work/docs/note.md')])
      : page('computer', '/work', '/', [entry('/work/docs', true), entry('/work/a.md')]))
    const scope = effectScope()
    const tree = scope.run(() => useFileExplorer(source, 'session', { listDirectory: listing, referencePath: vi.fn() }))!
    await vi.waitFor(() => expect(tree.rootPath.value).toBe('/work'))
    source.value = { computer_id: 'computer', path: '/work/docs' }
    await vi.waitFor(() => expect(tree.rows.value.some(row => row.kind === 'entry' && row.entry.path === '/work/docs/note.md')).toBe(true))
    expect(listing.mock.calls.map(call => call[2])).toEqual(['/work/a.md', '/work/docs'])
    expect(tree.rootPath.value).toBe('/work')
    scope.stop()
  })

  it('lazily expands the source Computer paths and keeps loaded children when collapsed', async () => {
    const origin = file('windows-source', 'C:\\work\\report.md')
    const listing = vi.fn<FileService['listDirectory']>().mockImplementation(async (source, _session, path) => {
      if (path === origin.path) return page(source.computer_id, 'C:\\work', 'C:\\', [entry('C:\\work\\docs', true), entry(origin.path)])
      return page(source.computer_id, 'C:\\work\\docs', 'C:\\work', [entry('C:\\work\\docs\\notes.md')])
    })
    const reference = vi.fn<FileService['referencePath']>(async (source, _session, path) => file(source.computer_id, path))
    const scope = effectScope()
    const tree = scope.run(() => useFileExplorer(origin, 'authorized-session', { listDirectory: listing, referencePath: reference }))!
    await vi.waitFor(() => expect(tree.rootPath.value).toBe('C:\\work'))
    expect(listing).toHaveBeenCalledTimes(1)
    expect(listing.mock.calls[0]!.slice(0, 3)).toEqual([origin, 'authorized-session', origin.path])
    await tree.toggle('C:\\work\\docs')
    expect(tree.rows.value.some(row => row.kind === 'entry' && row.entry.path.endsWith('notes.md'))).toBe(true)
    await tree.toggle('C:\\work\\docs')
    expect(tree.rows.value.some(row => row.kind === 'entry' && row.entry.path.endsWith('notes.md'))).toBe(false)
    await tree.toggle('C:\\work\\docs')
    expect(listing).toHaveBeenCalledTimes(2)
    const selected = await tree.open(entry('C:\\work\\docs\\notes.md'))
    expect(selected?.computer_id).toBe('windows-source')
    expect(reference.mock.calls[0]!.slice(0, 3)).toEqual([origin, 'authorized-session', 'C:\\work\\docs\\notes.md'])
    expect(origin.path).toBe('C:\\work\\report.md')
    scope.stop()
  })

  it('does not let a late directory from another Computer replace the active source', async () => {
    const old = deferred<DirectoryPage>()
    const source = shallowRef(file('remote-a', '/a/original.md'))
    const listing = vi.fn<FileService['listDirectory']>().mockImplementation(async value => value.computer_id === 'remote-a' ? old.promise : page('remote-b', '/b', '/', [entry('/b/new.md')]))
    const scope = effectScope()
    const tree = scope.run(() => useFileExplorer(source, 'session', { listDirectory: listing, referencePath: vi.fn() }))!
    source.value = file('remote-b', '/b/new.md')
    await vi.waitFor(() => expect(tree.rootPath.value).toBe('/b'))
    old.resolve(page('remote-a', '/a', '/', [entry('/a/original.md')]))
    await old.promise; await Promise.resolve()
    expect(tree.rootPath.value).toBe('/b')
    expect(tree.rows.value.filter(row => row.kind === 'entry').map(row => row.entry.path)).toEqual(['/b/new.md'])
    scope.stop()
  })

  it('preserves an expanded tree when another file in that tree becomes active', async () => {
    const source = shallowRef(file('computer', '/work/a.md'))
    const listing = vi.fn<FileService['listDirectory']>().mockImplementation(async (_source, _session, path) => path === '/work/docs' ? page('computer', '/work/docs', '/work', [entry('/work/docs/b.md')]) : page('computer', '/work', '/', [entry('/work/docs', true), entry('/work/a.md')]))
    const scope = effectScope()
    const tree = scope.run(() => useFileExplorer(source, 'session', { listDirectory: listing, referencePath: vi.fn() }))!
    await vi.waitFor(() => expect(tree.rootPath.value).toBe('/work'))
    await tree.toggle('/work/docs')
    source.value = file('computer', '/work/docs/b.md')
    await Promise.resolve()
    expect(tree.rootPath.value).toBe('/work')
    expect(tree.rows.value.some(row => row.kind === 'entry' && row.entry.path === '/work/docs/b.md')).toBe(true)
    expect(listing).toHaveBeenCalledTimes(2)
    scope.stop()
  })

  it('paginates without duplicating entries and refreshes expanded folders without collapsing them', async () => {
    const listing = vi.fn<FileService['listDirectory']>().mockImplementation(async (_source, _session, path, options) => {
      if (path === '/work/docs') return page('computer', '/work/docs', '/work', [entry('/work/docs/note')])
      if (options?.cursor) return page('computer', '/work', '/', [entry('/work/docs', true), entry('/work/last')])
      return page('computer', '/work', '/', [entry('/work/docs', true)], 'next')
    })
    const scope = effectScope()
    const tree = scope.run(() => useFileExplorer(file('computer', '/work/a'), 'session', { listDirectory: listing, referencePath: vi.fn() }))!
    await vi.waitFor(() => expect(tree.rootPath.value).toBe('/work'))
    await tree.more('/work')
    expect(tree.root.value?.page?.entries.map(value => value.path)).toEqual(['/work/docs', '/work/last'])
    await tree.toggle('/work/docs')
    await tree.refresh()
    expect(tree.rows.value.some(row => row.kind === 'entry' && row.entry.path === '/work/docs/note')).toBe(true)
    scope.stop()
  })

  it('ignores an old path-reference result after the browsing source changes', async () => {
    const old = deferred<FileRef>()
    const source = shallowRef(file('a', '/a/file'))
    const listing = vi.fn<FileService['listDirectory']>().mockImplementation(async value => page(value.computer_id, `/${value.computer_id}`, '/', []))
    const reference = vi.fn<FileService['referencePath']>(() => old.promise)
    const scope = effectScope()
    const tree = scope.run(() => useFileExplorer(source, 'session', { listDirectory: listing, referencePath: reference }))!
    await vi.waitFor(() => expect(tree.rootPath.value).toBe('/a'))
    const opening = tree.open(entry('/a/file'))
    const signal = reference.mock.calls[0]![3] as AbortSignal
    source.value = file('b', '/b/file')
    expect(signal.aborted).toBe(true)
    old.resolve(file('a', '/a/file'))
    expect(await opening).toBeUndefined()
    scope.stop()
  })
})

it('does not carry an old Computer refresh spinner into a newly selected source', async () => {
  const oldRefresh = deferred<DirectoryPage>()
  const source = shallowRef(file('a', '/a/file'))
  let initial = true
  const listing = vi.fn<FileService['listDirectory']>().mockImplementation(async value => {
    if (value.computer_id === 'a' && !initial) return oldRefresh.promise
    initial = false
    return page(value.computer_id, `/${value.computer_id}`, '/', [entry(`/${value.computer_id}/file`)])
  })
  const scope = effectScope()
  const tree = scope.run(() => useFileExplorer(source, 'session', { listDirectory: listing, referencePath: vi.fn() }))!
  await vi.waitFor(() => expect(tree.rootPath.value).toBe('/a'))
  const refreshing = tree.refresh()
  expect(tree.refreshing.value).toBe(true)
  source.value = file('b', '/b/file')
  await vi.waitFor(() => expect(tree.rootPath.value).toBe('/b'))
  expect(tree.refreshing.value).toBe(false)
  oldRefresh.resolve(page('a', '/a', '/', []))
  await refreshing
  expect(tree.rootPath.value).toBe('/b')
  scope.stop()
})

it('refreshes only the visible tree after navigating to another folder on the same Computer', async () => {
  const source = shallowRef(file('computer', '/work/a'))
  const listing = vi.fn<FileService['listDirectory']>().mockImplementation(async (_source, _session, path) => {
    if (path === '/other/b' || path === '/other') return page('computer', '/other', '/', [entry('/other/b')])
    if (path === '/work/docs') return page('computer', '/work/docs', '/work', [entry('/work/docs/note')])
    return page('computer', '/work', '/', [entry('/work/docs', true)])
  })
  const scope = effectScope()
  const tree = scope.run(() => useFileExplorer(source, 'session', { listDirectory: listing, referencePath: vi.fn() }))!
  await vi.waitFor(() => expect(tree.rootPath.value).toBe('/work'))
  await tree.toggle('/work/docs')
  source.value = file('computer', '/other/b')
  await vi.waitFor(() => expect(tree.rootPath.value).toBe('/other'))
  listing.mockClear()
  await tree.refresh()
  expect(listing.mock.calls.map(call => call[2])).toEqual(['/other'])
  scope.stop()
})
