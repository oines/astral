import { effectScope, shallowRef } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import type { FileRef } from '../lib/types'
import { useFilePreview } from './useFilePreview'
import type { FilePreviewResource } from './useFileService'
vi.mock('@astral/sdk', () => ({ fileContent: vi.fn(), getFileReference: vi.fn(), listComputerDirectory: vi.fn(), referenceComputerFile: vi.fn() }))

const file = (id: string): FileRef => ({ id, revision: 1, agent_id: 'bot', computer_id: 'source-computer', path: `/work/${id}`, display_name: id, media_type: 'text/plain', size: 5, created_at: '', updated_at: '' })
const resource = (name: string): FilePreviewResource => ({ name, mediaType: 'text/plain', content: name, partial: false, url: `/files/${name}`, downloadURL: `/files/${name}?download=true` })
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (cause: Error) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }

describe('file preview request ownership', () => {
  it('aborts a previous file and ignores its late response without ending the new loading state', async () => {
    const a = deferred<FilePreviewResource>(), b = deferred<FilePreviewResource>()
    const load = vi.fn().mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise)
    const current = shallowRef(file('a.md'))
    const scope = effectScope()
    const preview = scope.run(() => useFilePreview(current, 'session', { preview: load }))!
    const oldSignal = load.mock.calls[0]![2] as AbortSignal
    current.value = file('b.md')
    expect(oldSignal.aborted).toBe(true)
    a.resolve(resource('a.md'))
    await a.promise; await Promise.resolve()
    expect(preview.preview.value).toBeUndefined()
    expect(preview.busy.value).toBe(true)
    b.resolve(resource('b.md'))
    await vi.waitFor(() => expect(preview.preview.value?.name).toBe('b.md'))
    expect(preview.busy.value).toBe(false)
    scope.stop()
  })

  it('keeps errors scoped to the active file and retries it with a fresh request', async () => {
    const old = deferred<FilePreviewResource>()
    const load = vi.fn().mockReturnValueOnce(old.promise).mockRejectedValueOnce(new Error('Computer offline')).mockResolvedValueOnce(resource('next.txt'))
    const current = shallowRef(file('old.txt'))
    const scope = effectScope()
    const preview = scope.run(() => useFilePreview(current, 'session', { preview: load }))!
    current.value = file('next.txt')
    await vi.waitFor(() => expect(preview.error.value).toBe('Computer offline'))
    old.reject(new Error('Obsolete failure'))
    await Promise.resolve(); await Promise.resolve()
    expect(preview.error.value).toBe('Computer offline')
    await preview.reload()
    expect(preview.error.value).toBe('')
    expect(preview.preview.value?.name).toBe('next.txt')
    scope.stop()
  })

  it('cancels an unmounted preview before it can publish a response', async () => {
    const pending = deferred<FilePreviewResource>()
    const load = vi.fn<(file: FileRef, sessionID: string, signal?: AbortSignal) => Promise<FilePreviewResource>>(() => pending.promise)
    const scope = effectScope()
    const preview = scope.run(() => useFilePreview(file('a'), 'session', { preview: load }))!
    const signal = load.mock.calls[0]![2] as AbortSignal
    scope.stop()
    expect(signal.aborted).toBe(true)
    pending.resolve(resource('a'))
    await pending.promise; await Promise.resolve()
    expect(preview.preview.value).toBeUndefined()
  })
})

it('does not reload a displayed preview for metadata-only replacements of the same reference', async () => {
  const current = shallowRef(file('same.md'))
  const load = vi.fn(async () => resource('same.md'))
  const scope = effectScope()
  const preview = scope.run(() => useFilePreview(current, 'session', { preview: load }))!
  await vi.waitFor(() => expect(preview.preview.value?.name).toBe('same.md'))
  current.value = { ...current.value, display_name: 'A descriptive title' }
  await Promise.resolve()
  expect(load).toHaveBeenCalledOnce()
  expect(preview.preview.value?.name).toBe('same.md')
  scope.stop()
})

it('keeps the displayed resource during a manual refresh and after its failure', async () => {
  const pending = deferred<FilePreviewResource>()
  const initial = resource('readable.md')
  const load = vi.fn().mockResolvedValueOnce(initial).mockReturnValueOnce(pending.promise)
  const scope = effectScope()
  const preview = scope.run(() => useFilePreview(file('readable.md'), 'session', { preview: load }))!
  await vi.waitFor(() => expect(preview.preview.value).toBe(initial))
  const refresh = preview.reload()
  expect(preview.preview.value).toBe(initial)
  expect(preview.busy.value).toBe(true)
  pending.reject(new Error('Computer unavailable'))
  await refresh
  expect(preview.preview.value).toBe(initial)
  expect(preview.error.value).toBe('Computer unavailable')
  expect(preview.busy.value).toBe(false)
  scope.stop()
})
