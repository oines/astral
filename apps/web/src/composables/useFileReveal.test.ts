import { effectScope, nextTick, shallowRef } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useFileReveal, type FileRevealTarget } from './useFileReveal'
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (cause: Error) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
afterEach(() => vi.unstubAllGlobals())

describe('optional native source-file actions', () => {
  it('never carries a successful local capability into a newer remote target', async () => {
    const old = deferred<boolean>()
    const canReveal = vi.fn().mockReturnValueOnce(old.promise).mockResolvedValueOnce(false)
    vi.stubGlobal('window', { astralDesktop: { platform: 'darwin', files: { canReveal, reveal: vi.fn() } } })
    const target = shallowRef<FileRevealTarget>({ computerID: 'local', sessionID: 'session', fileID: 'a' })
    const scope = effectScope()
    const native = scope.run(() => useFileReveal(target))!
    target.value = { computerID: 'remote', sessionID: 'session', fileID: 'b' }
    await nextTick(); old.resolve(true); await old.promise; await nextTick()
    expect(native.available.value).toBe(false)
    scope.stop()
  })
  it('retains a failed requested action when its context menu closes', async () => {
    const pending = deferred<void>()
    const reveal = vi.fn(() => pending.promise)
    vi.stubGlobal('window', { astralDesktop: { platform: 'win32', files: { canReveal: vi.fn(async () => true), reveal } } })
    const target = shallowRef<FileRevealTarget | undefined>({ computerID: 'local', sessionID: 'session', path: 'C:\\work', directory: true })
    const scope = effectScope()
    const native = scope.run(() => useFileReveal(target))!
    await nextTick()
    const action = native.reveal()
    target.value = undefined
    await nextTick()
    pending.reject(new Error('File was removed'))
    await action
    expect(native.error.value).toBe('File was removed')
    expect(native.busy.value).toBe(false)
    scope.stop()
  })
})
