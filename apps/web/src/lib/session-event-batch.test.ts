import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createSessionEventBatch } from './session-event-batch'
import type { SessionEvent } from './types'

function delta(sequence: number): SessionEvent {
  return { epoch: 'server', core: { sequence, session_id: 'chat', type: 'message_update', model: { type: 'text_delta', content_index: 0, text: String(sequence) } } }
}
let frames: Map<number, FrameRequestCallback>
beforeEach(() => {
  vi.useFakeTimers()
  frames = new Map()
  let id = 0
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => { frames.set(++id, callback); return id })
  vi.stubGlobal('cancelAnimationFrame', (key: number) => frames.delete(key))
})
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

describe('session event frame batches', () => {
  it('publishes one ordered batch per frame and flushes earlier deltas before a durable boundary', () => {
    const consume = vi.fn()
    const batch = createSessionEventBatch(consume)
    batch.push(delta(1)); batch.push(delta(2))
    expect(consume).not.toHaveBeenCalled()
    expect(frames.size).toBe(1)
    frames.values().next().value!(0)
    expect(consume).toHaveBeenLastCalledWith([delta(1), delta(2)])
    const commit: SessionEvent = { epoch: 'server', core: { sequence: 4, session_id: 'chat', type: 'entry_committed' } }
    batch.push(delta(3)); batch.push(commit)
    expect(consume).toHaveBeenLastCalledWith([delta(3), commit])
    expect(frames.size).toBe(0)
    expect(consume).toHaveBeenCalledTimes(2)
  })
  it('keeps background queues bounded when animation frames are suspended', async () => {
    const consume = vi.fn()
    const batch = createSessionEventBatch(consume)
    batch.push(delta(1))
    await vi.advanceTimersByTimeAsync(100)
    expect(consume).toHaveBeenCalledWith([delta(1)])
    for (let i = 2; i <= 513; i++) batch.push(delta(i))
    expect(consume).toHaveBeenCalledTimes(2)
    expect(consume.mock.calls[1]![0]).toHaveLength(512)
    expect(frames.size).toBe(0)
  })
  it('cancels a closing transport queue without replaying it into the next session', async () => {
    const consume = vi.fn()
    const batch = createSessionEventBatch(consume)
    batch.push(delta(1)); batch.clear()
    await vi.advanceTimersByTimeAsync(100)
    expect(consume).not.toHaveBeenCalled()
    expect(frames.size).toBe(0)
    batch.push(delta(2)); batch.flush()
    expect(consume).toHaveBeenCalledWith([delta(2)])
  })
})
