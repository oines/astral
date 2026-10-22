import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref, type EffectScope } from 'vue'
import { useSessionTitles } from './useSessionTitles'

interface Title { id: string; title: string; title_source?: string }
let scope: EffectScope | undefined
afterEach(() => {
  scope?.stop()
  vi.useRealTimers()
})
function setup(initial: Title[] | null = [{ id: 'one', title: 'First message', title_source: 'prompt' }]) {
  vi.useFakeTimers()
  scope = effectScope()
  const sessions = ref<Title[] | undefined>(initial ?? undefined)
  const reducedMotion = ref(false)
  const result = scope.run(() => useSessionTitles(sessions, reducedMotion))!
  return { sessions, reducedMotion, ...result }
}

describe('sidebar generated titles', () => {
  it('types whole graphemes once and survives repeated snapshots and row regrouping', async () => {
    const state = setup()
    const final = 'K8s更新👩🏽‍💻分析'
    state.sessions.value = [{ id: 'one', title: final, title_source: 'generated' }]
    await nextTick()
    expect(state.typing.value.one).toBe(true)
    expect(state.titles.value.one).toBe('')
    await vi.advanceTimersByTimeAsync(200)
    const partial = state.titles.value.one!
    expect(partial.length).toBeGreaterThan(0)
    expect(partial).not.toBe(final)
    state.sessions.value = [{ id: 'one', title: final, title_source: 'generated' }]
    await nextTick()
    expect(state.titles.value.one).toBe(partial)
    const graphemes = Array.from(new Intl.Segmenter(undefined, { granularity: 'grapheme' }).segment(final), part => part.segment)
    const validPrefixes = graphemes.map((_, index) => graphemes.slice(0, index + 1).join(''))
    while (state.typing.value.one) {
      await vi.advanceTimersByTimeAsync(65)
      expect(validPrefixes).toContain(state.titles.value.one)
    }
    expect(state.titles.value.one).toBe(final)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('shows historical generated titles immediately on initial load', async () => {
    const state = setup(null)
    state.sessions.value = [{ id: 'one', title: 'Already generated', title_source: 'generated' }]
    await nextTick()
    expect(state.titles.value.one).toBe('Already generated')
    expect(state.typing.value.one).toBeUndefined()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('animates a new session whose model finished before the first sidebar refresh', async () => {
    const state = setup([])
    state.sessions.value = [{ id: 'new', title: 'New title', title_source: 'generated' }]
    await nextTick()
    expect(state.typing.value.new).toBe(true)
    await vi.runAllTimersAsync()
    expect(state.titles.value.new).toBe('New title')
  })

  it('cancels the animation on a manual rename and never types manual titles', async () => {
    const state = setup()
    state.sessions.value = [{ id: 'one', title: 'Generated title', title_source: 'generated' }]
    await nextTick()
    await vi.advanceTimersByTimeAsync(100)
    state.sessions.value = [{ id: 'one', title: 'My title', title_source: 'manual' }]
    await nextTick()
    await vi.runAllTimersAsync()
    expect(state.titles.value.one).toBe('My title')
    expect(state.typing.value.one).toBeUndefined()
  })

  it('respects reduced motion before and during typing', async () => {
    const state = setup()
    state.reducedMotion.value = true
    state.sessions.value = [{ id: 'one', title: 'No motion', title_source: 'generated' }]
    await nextTick()
    expect(state.titles.value.one).toBe('No motion')
    expect(vi.getTimerCount()).toBe(0)
    state.reducedMotion.value = false
    state.sessions.value = [{ ...state.sessions.value[0]!, title_source: 'prompt' }]
    await nextTick()
    state.sessions.value = [{ id: 'one', title: 'Another title', title_source: 'generated' }]
    await nextTick()
    expect(state.typing.value.one).toBe(true)
    state.reducedMotion.value = true
    await nextTick()
    expect(state.titles.value.one).toBe('Another title')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('cleans up removed sessions and disposed sidebars', async () => {
    const state = setup()
    state.sessions.value = [{ id: 'one', title: 'Generated title', title_source: 'generated' }]
    await nextTick()
    state.sessions.value = []
    await nextTick()
    expect(state.titles.value.one).toBeUndefined()
    expect(vi.getTimerCount()).toBe(0)
    state.sessions.value = [{ id: 'two', title: 'Another title', title_source: 'generated' }]
    await nextTick()
    expect(vi.getTimerCount()).toBe(1)
    scope?.stop()
    expect(vi.getTimerCount()).toBe(0)
  })
})
