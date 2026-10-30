import { computed, nextTick, onActivated, onDeactivated, onScopeDispose, reactive, watch, type Ref } from 'vue'
import { useResizeObserver } from '@vueuse/core'
import { ScrollFollow, type ScrollMetrics } from '../lib/scroll-follow'

type Anchor = { element: HTMLElement; key: string; item?: string; node?: string; offset: number; metrics: ScrollMetrics }
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
  let jumpTimer: ReturnType<typeof setTimeout> | undefined
  let jumping = false
  let anchor: Anchor | undefined
  let touchY = 0
  let loading = false
  let historyFailed = false
  let generation = 0

  function metrics(el: HTMLElement): ScrollMetrics {
    return { top: el.scrollTop, height: el.scrollHeight, viewport: el.clientHeight, width: el.clientWidth }
  }
  function captureAnchor(restored = false) {
    const el = options.scroller.value
    if (!active || !el || !el.clientHeight) return
    const current = metrics(el)
    // Input/scroll callbacks can run after a prepend but before layout(). Keep
    // the pre-update anchor until it is restored, independent of follow.metrics.
    if (!restored && anchor && (anchor.metrics.height !== current.height || anchor.metrics.viewport !== current.viewport || anchor.metrics.width !== current.width)) return
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
    if (best && owner) anchor = { element: best, key: owner.dataset.scrollAnchor!, item: owner.dataset.scrollAnchorItems?.split(' ')[0], node: best.dataset.nodeIndex, offset: best.getBoundingClientRect().top - bounds.top, metrics: current }
  }
  function restoreAnchor(el: HTMLElement) {
    if (!anchor) return
    let target = anchor.element
    // Prepending within a run renumbers process blocks. The DOM/key can be
    // reused for an earlier block, so resolve it by a durable process item.
    if (anchor.item || !target.isConnected) {
      const owner = el.querySelector<HTMLElement>(anchor.item ? `[data-scroll-anchor-items~="${CSS.escape(anchor.item)}"]` : `[data-scroll-anchor="${CSS.escape(anchor.key)}"]`)
      const node = anchor.node === undefined ? undefined : owner?.querySelector<HTMLElement>(`[data-node-index="${CSS.escape(anchor.node)}"]`)
      if (!owner) return
      target = node || owner
    }
    // Compare content positions, not viewport positions: native scrolling since
    // capture must survive the correction. Account for a shrinking scroll range.
    const previousTop = Math.min(anchor.metrics.top, Math.max(0, el.scrollHeight - el.clientHeight))
    const delta = target.getBoundingClientRect().top - el.getBoundingClientRect().top - anchor.offset + el.scrollTop - previousTop
    if (Math.abs(delta) > .5) { el.scrollTop += delta; follow.wrote(metrics(el)) }
  }
  async function loadEarlier() {
    const el = options.scroller.value
    if (!active || !initialized || !el || !options.hasMore.value || loading || historyFailed) return
    // A tail full of passive observations may render no bubbles. Fill the
    // viewport before requiring an upward scroll the user cannot yet perform.
    if (follow.following && el.scrollHeight > el.clientHeight + 1 || el.scrollTop > Math.max(800, el.clientHeight * 1.5)) return
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
    if (jumping) { follow.measure(metrics(el)); return }
    if (follow.following) {
      const cooldown = follow.cooldown(performance.now())
      if (cooldown > 0) { clearTimeout(timer); timer = setTimeout(schedule, cooldown); return }
      el.scrollTop = el.scrollHeight
      follow.wrote(metrics(el))
      anchor = undefined
    } else {
      restoreAnchor(el)
      follow.measure(metrics(el))
      captureAnchor(true)
    }
    initialized = true
    void loadEarlier()
  }
  function schedule() { if (active && !frame) frame = requestAnimationFrame(layout) }
  function cancelJump() {
    if (!jumping) return
    jumping = false
    clearTimeout(jumpTimer)
    const el = options.scroller.value
    if (el) {
      el.scrollTo({ top: el.scrollTop, behavior: 'instant' })
      follow.following = false
      follow.measure(metrics(el))
    }
  }
  function stop() { cancelAnimationFrame(frame); frame = 0; clearTimeout(timer); cancelJump() }
  function waitForJumpEnd() {
    clearTimeout(jumpTimer)
    // scrollend handles native completion; this also covers engines that do not
    // emit it, without writing scrollTop during the animation.
    jumpTimer = setTimeout(onScrollEnd, 150)
  }
  function onScrollEnd() {
    const el = options.scroller.value
    if (!jumping || !el) return
    clearTimeout(jumpTimer)
    follow.measure(metrics(el))
    if (follow.distance > 1) {
      // Streaming may have moved the bottom while the native animation ran.
      el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
      waitForJumpEnd()
    } else {
      jumping = false
      follow.wrote(metrics(el))
      schedule()
    }
  }
  function onScroll() {
    const el = options.scroller.value
    if (!el) return
    if (jumping) { follow.measure(metrics(el)); waitForJumpEnd(); return }
    follow.observe(metrics(el))
    if (!follow.following) captureAnchor()
    schedule()
  }
  function input(direction: 'up' | 'down' | 'pointer') {
    const el = options.scroller.value
    if (!el) return
    cancelJump()
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
  function pause() { cancelJump(); follow.following = false; captureAnchor() }
  function reset() { stop(); follow.reset(); initialized = true; anchor = undefined; schedule() }
  function bottom() {
    reset()
    const el = options.scroller.value
    if (!el || el.scrollHeight - el.clientHeight - el.scrollTop <= 1 || window.matchMedia('(prefers-reduced-motion: reduce)').matches) return
    jumping = true
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
    waitForJumpEnd()
  }

  useResizeObserver([options.content, options.scroller], schedule)
  watch(options.hasMore, schedule, { flush: 'post' })
  watch(options.sessionID, async () => {
    generation++
    loading = false
    historyFailed = false
    initialized = false
    reset()
    await nextTick()
    schedule()
  }, { immediate: true, flush: 'post' })
  onActivated(() => { active = true; schedule() })
  onDeactivated(() => { active = false; stop() })
  onScopeDispose(stop)
  return { showJump: computed(() => follow.showJump), onScroll, onScrollEnd, onWheel, onTouchStart, onTouchMove, onKeydown, onPointerdown: () => input('pointer'), pause, bottom }
}
