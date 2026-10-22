import { afterEach, describe, expect, it, vi } from 'vitest'
import { createSettingsMutations, type SettingsSaveState } from './settings-mutations'

function deferred() {
  let resolve!: () => void
  let reject!: (error: unknown) => void
  const promise = new Promise<void>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}
function setup() {
  const states = new Map<string, SettingsSaveState>()
  const report = vi.fn((key: string, value: SettingsSaveState) => states.set(key, value))
  return { states, report, saves: createSettingsMutations(report, 100) }
}
afterEach(() => { vi.useRealTimers() })

describe('settings mutations', () => {
  it('serializes one resource, coalesces queued choices and preserves other fields', async () => {
    const { saves, states } = setup()
    const gate = deferred()
    const writes: string[] = []
    const first = saves.run('bot:1', 'name', { write: async () => { writes.push('name'); await gate.promise } })
    await Promise.resolve()
    const superseded = saves.run('bot:1', 'model', { write: async () => { writes.push('old model') } })
    const permission = saves.run('bot:1', 'access', { write: async () => { writes.push('access') } })
    const latest = saves.run('bot:1', 'model', { write: async () => { writes.push('new model') } })
    expect(writes).toEqual(['name'])
    gate.resolve()
    await Promise.all([first, superseded, permission, latest])
    expect(writes).toEqual(['name', 'access', 'new model'])
    expect(states.get('bot:1/model')?.status).toBe('saved')
  })

  it('does not let a failed earlier request replace the feedback for a newer choice', async () => {
    const { saves, report, states } = setup()
    const gate = deferred()
    const first = saves.run('bot:1', 'model', { write: () => gate.promise })
    await Promise.resolve()
    const latest = saves.run('bot:1', 'model', { write: async () => undefined })
    gate.reject({ code: 'conflict', message: 'Earlier request failed' })
    await Promise.all([first, latest])
    expect(report.mock.calls.some(([, state]) => state.status === 'error')).toBe(false)
    expect(states.get('bot:1/model')?.status).toBe('saved')
  })

  it('reconciles a lost response without repeating a committed write', async () => {
    const { saves, states } = setup()
    let persisted = false
    const write = vi.fn(async () => { persisted = true; throw new TypeError('Network disconnected') })
    await saves.run('account:1', 'enabled', { write, reconcile: async () => persisted ? 'applied' : 'not-applied' })
    expect(write).toHaveBeenCalledTimes(1)
    expect(states.get('account:1/enabled')?.status).toBe('saved')
  })

  it('checks an uncertain result before allowing a retry to write again', async () => {
    const { saves, states } = setup()
    let resolution: 'unknown' | 'not-applied' = 'unknown'
    const write = vi.fn(async (): Promise<void> => { throw new TypeError('Network disconnected') })
    await saves.run('account:1', 'config', { write, reconcile: async () => resolution })
    expect(states.get('account:1/config')?.status).toBe('uncertain')
    await saves.retry('account:1/config')
    expect(write).toHaveBeenCalledTimes(1)
    resolution = 'not-applied'
    await saves.retry('account:1/config')
    expect(states.get('account:1/config')?.status).toBe('error')
    expect(write).toHaveBeenCalledTimes(1)
    write.mockImplementationOnce(async () => undefined)
    await saves.retry('account:1/config')
    expect(write).toHaveBeenCalledTimes(2)
    expect(states.get('account:1/config')?.status).toBe('saved')
  })

  it('does not treat a browser abort code as a definite server rejection', async () => {
    const { saves, states } = setup()
    const reconcile = vi.fn(async () => 'applied' as const)
    await saves.run('bot:1', 'name', { write: async () => { throw new DOMException('Aborted', 'AbortError') }, reconcile })
    expect(reconcile).toHaveBeenCalledOnce()
    expect(states.get('bot:1/name')?.status).toBe('saved')
  })

  it('keeps newer success visible when an earlier success timer expires', async () => {
    vi.useFakeTimers()
    const { saves, states } = setup()
    await saves.run('bot:1', 'name', { write: async () => undefined })
    await vi.advanceTimersByTimeAsync(60)
    await saves.run('bot:1', 'name', { write: async () => undefined })
    await vi.advanceTimersByTimeAsync(40)
    expect(states.get('bot:1/name')?.status).toBe('saved')
    await vi.advanceTimersByTimeAsync(60)
    expect(states.get('bot:1/name')?.status).toBe('idle')
  })
})
