import { describe, expect, it } from 'vitest'
import { StartupWait } from './service'
describe('managed startup maintenance budget', () => {
  it('waits for a matching maintenance process without a size-blind hard deadline', () => {
    const wait = new StartupWait(1000)
    const marker = { pid: 42, phase: 'preparing-data' }
    expect(wait.update(2000, 42, marker)).toEqual({ maintaining: true, expired: false })
    expect(wait.update(3_600_000, 42, marker)).toEqual({ maintaining: true, expired: false })
    expect(wait.update(3_600_000, 42, undefined).expired).toBe(false)
    expect(wait.update(3_629_999, 42, undefined).expired).toBe(false)
    expect(wait.update(3_630_000, 42, undefined).expired).toBe(true)
  })
  it('ignores stale markers and preserves the normal startup bound', () => {
    for (const marker of [undefined, { pid: 41, phase: 'preparing-data' }, { pid: 42, phase: 'ready' }]) {
      const wait = new StartupWait(1000)
      expect(wait.update(31_000, 42, marker)).toEqual({ maintaining: false, expired: true })
    }
    expect(new StartupWait(1000).update(31_000, undefined, { pid: 42, phase: 'preparing-data' }).expired).toBe(true)
  })
  it('rejects a process mismatch after entering maintenance', () => {
    const wait = new StartupWait(0)
    wait.update(1, 42, { pid: 42, phase: 'preparing-data' })
    expect(() => wait.update(2, 42, { pid: 43, phase: 'preparing-data' })).toThrow('process changed')
  })
})
