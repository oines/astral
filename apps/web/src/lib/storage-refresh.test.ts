import { describe, expect, it, vi } from 'vitest'
import { createStorageLoader } from './storage-refresh'

describe('storage refresh coordination', () => {
  it('refreshes sizes after a cleanup that finishes while an older snapshot is loading', async () => {
    let finishRead!: () => void
    const first = new Promise<void>(resolve => { finishRead = resolve })
    const read = vi.fn(async (_refresh: boolean) => { if (read.mock.calls.length === 1) await first })
    const load = createStorageLoader(read)
    const initial = load()
    const cleanup = load(true)
    const nextCleanup = load(true)
    expect(read.mock.calls).toEqual([[false]])
    finishRead()
    await Promise.all([initial, cleanup, nextCleanup])
    expect(read.mock.calls).toEqual([[false], [true]])
  })

  it('coalesces polling and allows another read after an error', async () => {
    let finishRead!: () => void
    const first = new Promise<void>(resolve => { finishRead = resolve })
    const read = vi.fn(async (_refresh: boolean) => { if (read.mock.calls.length === 1) { await first; throw new Error('offline') } })
    const load = createStorageLoader(read)
    const initial = load()
    expect(load()).toBe(initial)
    finishRead()
    await expect(initial).rejects.toThrow('offline')
    await load(true)
    expect(read.mock.calls).toEqual([[false], [true]])
  })
})
