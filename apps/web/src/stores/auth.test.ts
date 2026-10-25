import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAuthStore } from './auth'
const api = vi.hoisted(() => ({ status: vi.fn() }))
vi.mock('../lib/auth', () => ({ authAPI: api }))
vi.mock('../lib/api', () => ({ errorText: (error: unknown) => error instanceof Error ? error.message : String(error) }))
class Stream extends EventTarget {
  static instances: Stream[] = []
  onerror?: () => void
  closed = false
  constructor(public url: string) { super(); Stream.instances.push(this) }
  close() { this.closed = true }
}
const status = (desktop = false) => ({ configured: true, authenticated: true, desktop, username: 'alice', passwordless: false, methods: ['totp'], passkey_available: false })
beforeEach(() => {
  vi.stubGlobal('window', new EventTarget())
  vi.stubGlobal('EventSource', Stream)
  setActivePinia(createPinia())
  api.status.mockReset()
})
afterEach(() => { vi.unstubAllGlobals(); Stream.instances = []; vi.useRealTimers() })
describe('Web and Desktop authentication state', () => {
  it('does not open a revocable browser-session stream for Desktop', async () => {
    api.status.mockResolvedValue(status(true))
    const auth = useAuthStore()
    await auth.refresh()
    expect(auth.authenticated).toBe(true)
    expect(auth.status?.desktop).toBe(true)
    expect(Stream.instances).toHaveLength(0)
  })
  it('checks the session after the server closes its authentication stream', async () => {
    vi.useFakeTimers()
    api.status.mockResolvedValueOnce(status()).mockResolvedValueOnce({ ...status(), authenticated: false })
    const auth = useAuthStore()
    await auth.refresh()
    const stream = Stream.instances[0]!
    expect(stream.url).toBe('/api/v1/auth/events')
    stream.onerror?.()
    await vi.advanceTimersByTimeAsync(0)
    expect(stream.closed).toBe(true)
    expect(auth.authenticated).toBe(false)
    expect(api.status).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(5000)
    expect(Stream.instances).toHaveLength(1)
  })
  it('preserves an existing session on a network failure, and exposes the error', async () => {
    api.status.mockResolvedValueOnce(status()).mockRejectedValueOnce(new Error('Connection lost'))
    const auth = useAuthStore()
    await auth.refresh(); await auth.refresh()
    expect(auth.authenticated).toBe(true)
    expect(auth.error).toBe('Connection lost')
  })
  it('deduplicates simultaneous authentication checks', async () => {
    let resolve!: (value: ReturnType<typeof status>) => void
    api.status.mockReturnValue(new Promise(done => { resolve = done }))
    const auth = useAuthStore()
    const first = auth.refresh(); const second = auth.refresh()
    expect(api.status).toHaveBeenCalledOnce()
    resolve(status()); await Promise.all([first, second])
    expect(auth.authenticated).toBe(true)
  })
})
