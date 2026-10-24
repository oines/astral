import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref } from 'vue'
import { useDelayedPending } from './useDelayedPending'

afterEach(() => { vi.useRealTimers() })

describe('delayed pending feedback', () => {
  it('keeps fast requests invisible and never flashes after completion', async () => {
    vi.useFakeTimers()
    const scope = effectScope()
    const pending = ref(false)
    const visible = scope.run(() => useDelayedPending(pending))!
    pending.value = true
    await vi.advanceTimersByTimeAsync(100)
    pending.value = false
    await vi.advanceTimersByTimeAsync(1000)
    expect(visible.value).toBe(false)
    scope.stop()
  })

  it('shows only a slow request and disappears as soon as it finishes', async () => {
    vi.useFakeTimers()
    const scope = effectScope()
    const pending = ref(true)
    const visible = scope.run(() => useDelayedPending(pending))!
    await vi.advanceTimersByTimeAsync(299)
    expect(visible.value).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(visible.value).toBe(true)
    pending.value = false
    expect(visible.value).toBe(false)
    scope.stop()
  })

  it('restarts the delay when switching to another conversation or field', async () => {
    vi.useFakeTimers()
    const scope = effectScope()
    const pending = ref('session:old')
    const visible = scope.run(() => useDelayedPending(pending))!
    await vi.advanceTimersByTimeAsync(250)
    pending.value = 'session:new'
    await vi.advanceTimersByTimeAsync(100)
    expect(visible.value).toBe(false)
    pending.value = ''
    await vi.advanceTimersByTimeAsync(1000)
    expect(visible.value).toBe(false)
    scope.stop()
  })

  it('cancels delayed feedback when its view is disposed', async () => {
    vi.useFakeTimers()
    const scope = effectScope()
    const visible = scope.run(() => useDelayedPending(true))!
    scope.stop()
    await vi.advanceTimersByTimeAsync(1000)
    expect(visible.value).toBe(false)
  })
})
