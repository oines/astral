import { nextTick, onActivated, onBeforeUnmount, onDeactivated, onScopeDispose, ref, watch, type Ref } from 'vue'
import { useResizeObserver } from '@vueuse/core'
import { ScrollFollow, type ScrollMetrics } from '../lib/scroll-follow'
import type { TranscriptPosition } from '../lib/transcript-position'

type Anchor = NonNullable<TranscriptPosition['anchor']> & { element: HTMLElement; metrics: ScrollMetrics }
type Options = {
  scroller: Ref<HTMLElement | undefined>
  content: Ref<HTMLElement | undefined>
  sessionID: Ref<string>
  hasMore: Ref<boolean>
  loadOlder: () => Promise<boolean>
  getPosition?: () => TranscriptPosition | undefined
  savePosition?: (position: TranscriptPosition) => void
}

export function useTranscriptScroll(options: Options) {
  // Scrolling updates one boolean in Vue, not the whole metrics tree.
  const follow = new ScrollFollow()
  const showJump = ref(false)
  const nativeAnchor = typeof CSS !== 'undefined' && CSS.supports?.('overflow-anchor', 'auto') === true
  let active = true
  let initialized = false
  let frame = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  let jumpTimer: ReturnType<typeof setTimeout> | undefined
  let jumping = false
  let anchor: Anchor | undefined
  let manualAnchor = !nativeAnchor
  let touchY = 0
  let loading = false
  let historyFailed = false
  let generation = 0

  function metrics(el: HTMLElement): ScrollMetrics {
    return { top: el.scrollTop, height: el.scrollHeight, viewport: el.clientHeight, width: el.clientWidth }
  }
  function measure(el: HTMLElement, written = false) {
    if (written) follow.wrote(metrics(el))
    else follow.measure(metrics(el))
    showJump.value = follow.showJump
  }
  function resolveAnchor(el: HTMLElement, saved: NonNullable<TranscriptPosition['anchor']>) {
    const owner = el.querySelector<HTMLElement>(`[data-scroll-anchor="${CSS.escape(saved.key)}"]`)
    return saved.node === undefined ? owner : owner?.querySelector<HTMLElement>(`[data-node-index="${CSS.escape(saved.node)}"]`) ?? owner
  }
  function captureAnchor(settled = false) {
    const el = options.scroller.value
    if (!active || !el?.clientHeight) return
    const current = metrics(el)
    // Keep the pre-layout position until a fallback correction has run. Native
    // anchoring, when available, is the only code allowed to correct reading.
    if (!settled && anchor && (anchor.metrics.height !== current.height || anchor.metrics.width !== current.width || anchor.metrics.viewport !== current.viewport)) return
    // Browsers suppress native anchoring at scrollTop=0. Choose one owner
    // before the prepend, then return control after the correction leaves zero.
    manualAnchor = !nativeAnchor || current.top <= 1
    el.style.overflowAnchor = manualAnchor ? 'none' : 'auto'
    const bounds = el.getBoundingClientRect()
    const body = options.content.value?.getBoundingClientRect() ?? bounds
    const left = Math.max(bounds.left, body.left)
    const right = Math.min(bounds.right, body.right)
    // User bubbles are right-aligned; the viewport center can hit only blank
    // space above the next assistant. Probe both content edges as well.
    const points = [(left + right) / 2, right - 32, left + 32]
    for (const inset of [12, 48, 96]) {
      for (const x of points) {
        const hit = document.elementFromPoint(x, Math.min(bounds.bottom - 1, bounds.top + inset))
        if (!hit || !el.contains(hit)) continue
        const target = hit.closest<HTMLElement>('[data-node-index], [data-scroll-anchor]')
        const owner = target?.closest<HTMLElement>('[data-scroll-anchor]')
        if (!target || !owner) continue
        anchor = { element: target, key: owner.dataset.scrollAnchor!, node: target.dataset.nodeIndex, offset: target.getBoundingClientRect().top - bounds.top, metrics: current }
        return
      }
    }
  }
  function restoreFallback(el: HTMLElement, reflowed = false) {
    if (!anchor) return
    const target = anchor.element.isConnected ? anchor.element : resolveAnchor(el, anchor)
    if (!target) return
    const previousTop = Math.min(anchor.metrics.top, Math.max(0, el.scrollHeight - el.clientHeight))
    const delta = target.getBoundingClientRect().top - el.getBoundingClientRect().top - anchor.offset + (reflowed ? 0 : el.scrollTop - previousTop)
    if (Math.abs(delta) > .5) { el.scrollTop += delta; measure(el, true) }
  }
  function savePosition() {
    const el = options.scroller.value
    if (!initialized || !el) return
    options.savePosition?.({ following: follow.following, top: el.scrollTop, anchor: anchor && { key: anchor.key, node: anchor.node, offset: anchor.offset } })
  }
  function restorePosition(el: HTMLElement) {
    const saved = options.getPosition?.()
    follow.following = saved?.following ?? true
    if (!follow.following && saved) {
      el.scrollTop = saved.top
      const target = saved.anchor && resolveAnchor(el, saved.anchor)
      if (target && saved.anchor) el.scrollTop += target.getBoundingClientRect().top - el.getBoundingClientRect().top - saved.anchor.offset
      measure(el, true)
      captureAnchor(true)
    }
    initialized = true
  }
  async function loadEarlier() {
    const el = options.scroller.value
    if (!active || !initialized || !el || !options.hasMore.value || loading || historyFailed) return
    // A page may hit its scan/byte budget before filling the screen. Continue
    // until there is something to scroll, without requiring an impossible gesture.
    if (follow.following && el.scrollHeight > el.clientHeight + 1 || el.scrollTop > Math.max(320, el.clientHeight * .75)) return
    loading = true
    const current = generation
    captureAnchor()
    try {
      const loaded = await options.loadOlder()
      if (current === generation) historyFailed = !loaded
    } catch { if (current === generation) historyFailed = true }
    finally {
      if (current === generation) {
        loading = false
        await nextTick()
        schedule()
      }
    }
  }
  function layout() {
    frame = 0
    const el = options.scroller.value
    if (!active || !el?.clientHeight) return
    if (!initialized) restorePosition(el)
    if (jumping) { measure(el); return }
    if (follow.following) {
      const cooldown = follow.cooldown(performance.now())
      if (cooldown > 0) { clearTimeout(timer); timer = setTimeout(schedule, cooldown); return }
      if (el.scrollHeight - el.clientHeight - el.scrollTop > .5) el.scrollTop = el.scrollHeight
      measure(el, true)
      anchor = undefined
      el.style.overflowAnchor = 'none'
    } else {
      const reflowed = anchor && (anchor.metrics.width !== el.clientWidth || anchor.metrics.viewport !== el.clientHeight)
      if (reflowed) {
        // Width/viewport changes can suppress native anchoring too. Restore
        // the measured paragraph once, without adding any native movement.
        el.style.overflowAnchor = 'none'
        restoreFallback(el, true)
      } else if (manualAnchor) restoreFallback(el)
      measure(el)
      captureAnchor(true)
    }
    savePosition()
    void loadEarlier()
  }
  function schedule() { if (active && !frame) frame = requestAnimationFrame(layout) }
  function cancelJump() {
    if (!jumping) return
    jumping = false
    clearTimeout(jumpTimer)
    const el = options.scroller.value
    if (el) { el.scrollTo({ top: el.scrollTop, behavior: 'instant' }); follow.following = false; measure(el) }
  }
  function stop() { cancelAnimationFrame(frame); frame = 0; clearTimeout(timer); cancelJump() }
  function waitForJumpEnd() {
    clearTimeout(jumpTimer)
    jumpTimer = setTimeout(onScrollEnd, 150)
  }
  function onScrollEnd() {
    const el = options.scroller.value
    if (!jumping || !el) return
    clearTimeout(jumpTimer)
    measure(el)
    if (follow.distance > 1) {
      el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
      waitForJumpEnd()
    } else { jumping = false; measure(el, true); schedule() }
  }
  function onScroll() {
    const el = options.scroller.value
    if (!el) return
    if (jumping) { measure(el); waitForJumpEnd(); return }
    follow.observe(metrics(el))
    showJump.value = follow.showJump
    if (!follow.following) captureAnchor()
    schedule()
  }
  function input(direction: 'up' | 'down' | 'pointer') {
    const el = options.scroller.value
    if (!el) return
    cancelJump()
    follow.input(direction, metrics(el), performance.now())
    showJump.value = follow.showJump
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
  function pause() { cancelJump(); follow.following = false; captureAnchor(); schedule() }
  function reset() { stop(); follow.reset(); initialized = false; anchor = undefined; showJump.value = false; schedule() }
  function bottom() {
    stop()
    follow.reset()
    initialized = true
    anchor = undefined
    const el = options.scroller.value
    if (!el || el.scrollHeight - el.clientHeight - el.scrollTop <= 1 || window.matchMedia('(prefers-reduced-motion: reduce)').matches) { schedule(); return }
    jumping = true
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
    waitForJumpEnd()
  }

  useResizeObserver([options.content, options.scroller], schedule)
  watch([options.scroller, options.content, options.hasMore], schedule, { immediate: true, flush: 'post' })
  watch(options.sessionID, () => { generation++; loading = false; historyFailed = false; reset() }, { immediate: true, flush: 'post' })
  onActivated(() => { active = true; initialized = false; schedule() })
  onDeactivated(() => { savePosition(); active = false; stop() })
  onBeforeUnmount(savePosition)
  onScopeDispose(() => { savePosition(); active = false; stop() })
  return { showJump, onScroll, onScrollEnd, onWheel, onTouchStart, onTouchMove, onKeydown, onPointerdown: () => input('pointer'), pause, bottom }
}
