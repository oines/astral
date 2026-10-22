import { onScopeDispose, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { useMediaQuery } from '@vueuse/core'

interface Title { id: string; title: string; title_source?: string }

// Own animations above the grouped rows so moving from Running to Recent,
// searching, or polling the same title cannot restart the typewriter.
export function useSessionTitles(
  sessions: MaybeRefOrGetter<readonly Title[] | undefined>,
  reducedMotion: MaybeRefOrGetter<boolean> = useMediaQuery('(prefers-reduced-motion: reduce)'),
) {
  const titles = ref<Record<string, string>>({})
  const typing = ref<Record<string, boolean>>({})
  const previous = new Map<string, Title>()
  const timers = new Map<string, ReturnType<typeof setTimeout>>()
  const segmenter = new Intl.Segmenter(undefined, { granularity: 'grapheme' })
  let initialized = false

  function cancel(id: string) {
    clearTimeout(timers.get(id))
    timers.delete(id)
    delete typing.value[id]
  }
  function reveal(item: Title) {
    const characters = Array.from(segmenter.segment(item.title), part => part.segment)
    const interval = Math.min(65, Math.max(20, 800 / Math.max(1, characters.length)))
    let count = 0
    titles.value[item.id] = ''
    typing.value[item.id] = true
    function tick() {
      titles.value[item.id] = characters.slice(0, ++count).join('')
      if (count < characters.length) timers.set(item.id, setTimeout(tick, interval))
      else cancel(item.id)
    }
    timers.set(item.id, setTimeout(tick, interval))
  }

  watch(() => toValue(sessions)?.map(({ id, title, title_source }) => ({ id, title, title_source })), items => {
    if (!items) return
    const ids = new Set(items.map(item => item.id))
    for (const id of previous.keys()) {
      if (ids.has(id)) continue
      cancel(id)
      previous.delete(id)
      delete titles.value[id]
    }
    for (const item of items) {
      const old = previous.get(item.id)
      if (old?.title === item.title && old.title_source === item.title_source) continue
      cancel(item.id)
      if (initialized && item.title && item.title_source === 'generated' && old?.title_source !== 'generated' && old?.title !== item.title && !toValue(reducedMotion)) reveal(item)
      else titles.value[item.id] = item.title
      previous.set(item.id, item)
    }
    initialized = true
  }, { immediate: true })

  watch(() => toValue(reducedMotion), reduced => {
    if (!reduced) return
    for (const [id, item] of previous) {
      cancel(id)
      titles.value[id] = item.title
    }
  })
  onScopeDispose(() => { for (const id of timers.keys()) cancel(id) })
  return { titles, typing }
}
