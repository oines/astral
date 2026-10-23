import { computed, nextTick, onActivated, onDeactivated, onScopeDispose, reactive, watch, type Ref } from 'vue'
import { useResizeObserver } from '@vueuse/core'
import { ScrollFollow, type ScrollMetrics } from '../lib/scroll-follow'

type Anchor = { element: HTMLElement; key: string; node?: string; offset: number }
type Options = {
  scroller: Ref<HTMLElement | undefined>
  content: Ref<HTMLElement | undefined>
  sessionID: Ref<string>
  hasMore: Ref<boolean>
  loadOlder: () => Promise<boolean>
}

export function useTranscriptScroll(options: Options) {
  const follow = reactive(new ScrollFollow())
  let active = true
  let initialized = false
  let frame = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  let anchor: Anchor | undefined
  let touchY = 0
  let loading = false
  let historyFailed = false
  let generation = 0

  function metrics(el: HTMLElement): ScrollMetrics {
    return { top: el.scrollTop, height: el.scrollHeight, viewport: el.clientHeight, width: el.clientWidth }
  }
  function captureAnchor() {
    const el = options.scroller.value
    if (!active || !el || !el.clientHeight) return
    const bounds = el.getBoundingClientRect()
    let best: HTMLElement | undefined
    let distance = Infinity
    for (const candidate of el.querySelectorAll<HTMLElement>('[data-scroll-anchor], .markdown [data-node-index]')) {
      const rect = candidate.getBoundingClientRect()
      if (rect.bottom <= bounds.top || rect.top >= bounds.bottom) continue
      const next = Math.abs(rect.top - bounds.top)
      if (next < distance) { best = candidate; distance = next }
    }
    const owner = best?.closest<HTMLElement>('[data-scroll-anchor]')
    if (best && owner) anchor = { element: best, key: owner.dataset.scrollAnchor!, node: best.dataset.nodeIndex, offset: best.getBoundingClientRect().top - bounds.top }
  }
  function restoreAnchor(el: HTMLElement) {
    if (!anchor) return
    let target = anchor.element
    if (!target.isConnected) {
      const owner = el.querySelector<HTMLElement>(`[data-scroll-anchor="${CSS.escape(anchor.key)}"]`)
      const node = anchor.node === undefined ? undefined : owner?.querySelector<HTMLElement>(`[data-node-index="${CSS.escape(anchor.node)}"]`)
      if (!owner) return
      target = node || owner
    }
    const delta = target.getBoundingClientRect().top - el.getBoundingClientRect().top - anchor.offset
    if (Math.abs(delta) > .5) { el.scrollTop += delta; follow.wrote(metrics(el)) }
  }
  async function loadEarlier() {
    const el = options.scroller.value
    if (!active || !initialized || follow.following || !el || el.scrollTop > 320 || !options.hasMore.value || loading || historyFailed) return
    loading = true
    const current = generation
    captureAnchor()
    const loaded = await options.loadOlder()
    if (current !== generation) return
    historyFailed = !loaded
    loading = false
    await nextTick()
    schedule()
  }
  function layout() {
    frame = 0
    const el = options.scroller.value
    if (!active || !el || !el.clientHeight) return
    if (follow.following) {
      const cooldown = follow.cooldown(performance.now())
      if (cooldown > 0) { clearTimeout(timer); timer = setTimeout(schedule, cooldown); return }
      el.scrollTop = el.scrollHeight
      follow.wrote(metrics(el))
      anchor = undefined
    } else {
      restoreAnchor(el)
      follow.measure(metrics(el))
      captureAnchor()
    }
    initialized = true
    void loadEarlier()
  }
  function schedule() { if (active && !frame) frame = requestAnimationFrame(layout) }
  function stop() { cancelAnimationFrame(frame); frame = 0; clearTimeout(timer) }
  function onScroll() {
    const el = options.scroller.value
    if (!el) return
    const next = metrics(el)
    const previous = follow.metrics
    const sameLayout = previous && previous.height === next.height && previous.viewport === next.viewport && previous.width === next.width
    follow.observe(next)
    // A prepend or width change can itself emit scroll. Keep the old reading
    // anchor until layout restores it; only user movement replaces the anchor.
    if (!follow.following && sameLayout) captureAnchor()
    schedule()
  }
  function input(direction: 'up' | 'down' | 'pointer') {
    const el = options.scroller.value
    if (!el) return
    follow.input(direction, metrics(el), performance.now())
    if (direction === 'up') { historyFailed = false; captureAnchor() }
    schedule()
  }
  function onWheel(event: WheelEvent) { if (event.deltaY) input(event.deltaY < 0 ? 'up' : 'down') }
  function onTouchStart(event: TouchEvent) { touchY = event.touches[0]?.clientY ?? 0 }
  function onTouchMove(event: TouchEvent) {
    const next = event.touches[0]?.clientY ?? touchY
    if (next !== touchY) input(next > touchY ? 'up' : 'down')
    touchY = next
  }
  function onKeydown(event: KeyboardEvent) {
    if (event.defaultPrevented || (event.target as Element).closest('input,textarea,select,button,a,[contenteditable="true"]')) return
    if (['ArrowUp', 'PageUp', 'Home'].includes(event.key) || event.key === ' ' && event.shiftKey) input('up')
    else if (['ArrowDown', 'PageDown', 'End', ' '].includes(event.key)) input('down')
  }
  function pause() { follow.following = false; captureAnchor() }
  function bottom() { follow.reset(); initialized = true; anchor = undefined; stop(); schedule() }

  useResizeObserver([options.content, options.scroller], schedule)
  watch(options.hasMore, schedule, { flush: 'post' })
  watch(options.sessionID, async () => {
    generation++
    loading = false
    historyFailed = false
    initialized = false
    bottom()
    await nextTick()
    schedule()
  }, { immediate: true, flush: 'post' })
  onActivated(() => { active = true; schedule() })
  onDeactivated(() => { active = false; stop() })
  onScopeDispose(stop)
  return { showJump: computed(() => follow.showJump), onScroll, onWheel, onTouchStart, onTouchMove, onKeydown, onPointerdown: () => input('pointer'), pause, bottom }
}
