import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref, shallowRef, type EffectScope } from 'vue'
import { useTranscriptScroll } from './useTranscriptScroll'

const resize = vi.hoisted(() => ({ notify: () => {} }))
vi.mock('@vueuse/core', () => ({ useResizeObserver: (_targets: unknown, callback: () => void) => { resize.notify = callback } }))
vi.mock('vue', async importOriginal => ({
  ...await importOriginal<typeof import('vue')>(),
  onActivated: vi.fn(),
  onBeforeUnmount: vi.fn(),
  onDeactivated: vi.fn(),
}))

let scope: EffectScope | undefined
afterEach(() => { scope?.stop(); vi.useRealTimers(); vi.unstubAllGlobals() })

// Model real layout coordinates separately from native scrolling. This lets the
// tests deliver wheel/scroll events between a DOM update and its next paint.
async function transcript(options: { height?: number; hasMore?: boolean; needsHistory?: boolean; reducedMotion?: boolean; nativeAnchor?: boolean; savedTop?: number; rows?: Array<{ key: string; top: number; height: number }> } = {}) {
  vi.useFakeTimers()
  let top = 0
  let height = options.height ?? 1600
  let width = 800
  let frameID = 0
  const frames = new Map<number, FrameRequestCallback>()
  const writes: number[] = []
  const nodes: Array<{ key: string; top: number; height: number; element: HTMLElement }> = []
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => { frames.set(++frameID, callback); return frameID })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => frames.delete(id))
  vi.stubGlobal('CSS', { escape: (value: string) => value, supports: () => !!options.nativeAnchor })
  vi.stubGlobal('window', { matchMedia: () => ({ matches: !!options.reducedMotion }) })
  const scrollTo = vi.fn((options: ScrollToOptions) => {
    if (options.behavior !== 'smooth') el.scrollTop = options.top ?? top
  })
  const el = {
    get scrollTop() { return top },
    set scrollTop(value: number) { top = Math.max(0, Math.min(height - 600, value)); writes.push(top) },
    get scrollHeight() { return height },
    scrollTo,
    clientHeight: 600,
    get clientWidth() { return width },
    style: { overflowAnchor: '' },
    getBoundingClientRect: () => ({ top: 40, bottom: 640, left: 0, right: 800, width: 800 }),
    contains: (element: HTMLElement) => nodes.some(node => node.element === element),
    querySelector: (selector: string) => nodes.find(node => node.key === selector.split('"')[1])?.element,
  } as unknown as HTMLElement
  function add(key: string, y: number, size = 100) {
    const node = { key, top: y, height: size, element: undefined as unknown as HTMLElement }
    node.element = {
      isConnected: true,
      dataset: { scrollAnchor: key },
      getBoundingClientRect: () => ({ top: 40 + node.top - top, bottom: 40 + node.top + node.height - top }),
      closest: () => node.element,
    } as unknown as HTMLElement
    nodes.push(node)
    return node
  }
  if (options.rows) for (const row of options.rows) add(row.key, row.top, row.height)
  else for (let y = 0; y < height; y += 100) add(`message-${y}`, y)
  vi.stubGlobal('document', { elementFromPoint: (_x: number, y: number) => nodes.find(node => y >= 40 + node.top - top && y < 40 + node.top + node.height - top)?.element })
  let finishLoad: (loaded: boolean) => void = () => {}
  const pendingLoads: Array<(loaded: boolean) => void> = []
  const loadOlder = vi.fn(() => new Promise<boolean>(resolve => { finishLoad = resolve; pendingLoads.push(resolve) }))
  scope = effectScope()
  const sessionID = ref('chat')
  const saved = options.savedTop === undefined ? undefined : { following: false, top: options.savedTop }
  const savePosition = vi.fn()
  const hasMore = ref(options.hasMore ?? true)
  const needsHistory = ref(options.needsHistory ?? false)
  const scroll = scope.run(() => useTranscriptScroll({ scroller: shallowRef(el), content: shallowRef(el), sessionID, hasMore, needsHistory, loadOlder, getPosition: () => saved, savePosition }))!
  function frame() {
    const pending = [...frames.values()]
    frames.clear()
    pending.forEach(callback => callback(0))
  }
  function wheel(delta: number) { scroll.onWheel({ deltaY: delta } as WheelEvent) }
  function move(value: number) { top = value; scroll.onScroll() }
  function prepend(size: number) {
    nodes.forEach(node => { node.top += size })
    height += size
    if (options.nativeAnchor && el.style.overflowAnchor !== 'none') top += size
    add(`older-${size}`, 0, size)
    resize.notify()
  }
  function grow(size: number) { height += size; resize.notify() }
  function reflow(nextWidth: number, nativeCorrected = false) {
    const ratio = width / nextWidth
    nodes.forEach(node => { node.top *= ratio; node.height *= ratio })
    height *= ratio
    if (nativeCorrected) top *= ratio
    width = nextWidth
    resize.notify()
  }
  await nextTick()
  frame()
  writes.length = 0
  return { el, scroll, scrollTo, sessionID, hasMore, needsHistory, frame, wheel, move, prepend, grow, reflow, writes, nodes, add, loadOlder, pendingLoads, savePosition, finishLoad: (loaded = true) => finishLoad(loaded) }
}

describe('filling the readable history window', () => {
  const activities = {
    height: 2050,
    rows: Array.from({ length: 50 }, (_, index) => ({ key: `activity-${index}`, top: index * 41, height: 41 })),
  }

  it('loads main history on the first frame even when fifty activity rows fill the screen', async () => {
    const t = await transcript({ ...activities, needsHistory: true })
    expect(t.el.scrollTop).toBe(1450)
    expect(t.loadOlder).toHaveBeenCalledTimes(1)
  })

  it('continues through empty scanned pages and stops forcing prefetch once the window is readable', async () => {
    const t = await transcript({ ...activities, needsHistory: true })
    for (let page = 0; page < 3; page++) {
      expect(t.loadOlder).toHaveBeenCalledTimes(page + 1)
      t.finishLoad()
      await nextTick()
      await nextTick()
      t.frame()
    }
    expect(t.loadOlder).toHaveBeenCalledTimes(4)
    expect(t.el.scrollTop).toBe(1450)

    t.needsHistory.value = false
    t.finishLoad()
    await nextTick()
    await nextTick()
    t.frame()
    expect(t.hasMore.value).toBe(true)
    expect(t.loadOlder).toHaveBeenCalledTimes(4)
  })

  it('starts filling when the history requirement changes without a DOM resize', async () => {
    const t = await transcript(activities)
    expect(t.loadOlder).not.toHaveBeenCalled()
    t.needsHistory.value = true
    await nextTick()
    t.frame()
    expect(t.loadOlder).toHaveBeenCalledTimes(1)
  })

  it('retains cursor exhaustion and failure guards while forcing the initial scan', async () => {
    const t = await transcript({ ...activities, hasMore: false, needsHistory: true })
    expect(t.loadOlder).not.toHaveBeenCalled()
    t.hasMore.value = true
    await nextTick()
    t.frame()
    expect(t.loadOlder).toHaveBeenCalledTimes(1)

    t.finishLoad(false)
    await nextTick()
    await nextTick()
    t.frame()
    for (let index = 0; index < 3; index++) { t.grow(1); t.frame() }
    expect(t.loadOlder).toHaveBeenCalledTimes(1)
    t.wheel(-20)
    t.frame()
    expect(t.loadOlder).toHaveBeenCalledTimes(2)
  })

  it('does not let a previous session failure block or duplicate the current session scan', async () => {
    const t = await transcript({ ...activities, needsHistory: true })
    t.sessionID.value = 'next-chat'
    await nextTick()
    t.frame()
    expect(t.loadOlder).toHaveBeenCalledTimes(2)

    t.pendingLoads[0]!(false)
    await nextTick()
    await nextTick()
    t.frame()
    expect(t.loadOlder).toHaveBeenCalledTimes(2)
    t.pendingLoads[1]!(true)
    await nextTick()
    await nextTick()
    t.frame()
    expect(t.loadOlder).toHaveBeenCalledTimes(3)
  })

  it('does not continue a pending scan after the transcript scope is disposed', async () => {
    const t = await transcript({ ...activities, needsHistory: true })
    scope!.stop()
    t.finishLoad()
    await nextTick()
    await nextTick()
    t.frame()
    expect(t.loadOlder).toHaveBeenCalledTimes(1)
  })

  it.each([false, true])('preserves upward reading movement when main history arrives with native anchoring %s', async nativeAnchor => {
    const t = await transcript({ ...activities, needsHistory: true, savedTop: 400, nativeAnchor })
    t.wheel(-120)
    t.move(280)
    t.frame()
    t.prepend(3000)
    t.needsHistory.value = false
    t.finishLoad()
    await nextTick()
    await nextTick()
    t.frame()

    expect(t.el.scrollTop).toBe(3280)
    expect(t.loadOlder).toHaveBeenCalledTimes(1)
    expect(t.savePosition).toHaveBeenLastCalledWith(expect.objectContaining({ following: false, anchor: expect.objectContaining({ key: 'activity-7' }) }))
  })
})

describe('transcript history scroll anchoring', () => {
  it('fills a short initial tail without requiring an impossible upward scroll', async () => {
    const t = await transcript({ height: 600 })
    expect(t.loadOlder).toHaveBeenCalledTimes(1)
    t.prepend(1000)
    t.finishLoad()
    await nextTick()
    t.frame()
    expect(t.el.scrollTop).toBe(1000)
    expect(t.loadOlder).toHaveBeenCalledTimes(1)
  })

  it('lets native scroll anchoring handle prepends without a second correction', async () => {
    const t = await transcript({ nativeAnchor: true })
    t.wheel(-710)
    t.move(290)
    t.frame()
    t.writes.length = 0
    t.prepend(3000)
    t.finishLoad()
    await nextTick()
    t.frame()
    expect(t.el.scrollTop).toBe(3290)
    expect(t.writes).toEqual([])
  })

  it('restores a cached reading position on the first frame and saves it with the session', async () => {
    const t = await transcript({ savedTop: 400, hasMore: false })
    expect(t.el.scrollTop).toBe(400)
    expect(t.scroll.showJump.value).toBe(true)
    expect(t.savePosition).toHaveBeenLastCalledWith(expect.objectContaining({ following: false, top: 400, anchor: expect.objectContaining({ key: 'message-400' }) }))
  })

  it('temporarily owns the anchor at zero where browsers suppress native anchoring', async () => {
    const t = await transcript({ nativeAnchor: true })
    t.wheel(-1000)
    t.move(0)
    t.frame()
    expect(t.el.style.overflowAnchor).toBe('none')
    t.prepend(3000)
    t.finishLoad()
    await nextTick()
    t.frame()
    expect(t.el.scrollTop).toBe(3000)
    expect(t.el.style.overflowAnchor).toBe('auto')
  })

  it.each([false, true])('keeps the visible paragraph on width changes even if native correction is %s', async nativeCorrected => {
    const t = await transcript({ nativeAnchor: true, hasMore: false })
    t.wheel(-600)
    t.move(400)
    t.frame()
    t.reflow(400, nativeCorrected)
    t.frame()
    expect(t.el.scrollTop).toBe(800)
    expect(t.el.style.overflowAnchor).toBe('auto')
  })

  it('keeps the original message in place when another upward wheel arrives after a prepend but before paint', async () => {
    const t = await transcript()
    t.wheel(-710)
    t.move(290)
    t.frame()
    expect(t.loadOlder).toHaveBeenCalledTimes(1)

    t.prepend(3000)
    t.finishLoad()
    await nextTick()
    // The next trackpad event sees the new DOM before the anchor is restored.
    t.wheel(-20)
    t.move(270)
    t.frame()

    expect(t.el.scrollTop).toBe(3270)
    expect(t.el.scrollHeight - t.el.scrollTop).toBe(1330)
    expect(t.loadOlder).toHaveBeenCalledTimes(1)
  })

  it('does not lose the prepend anchor to multiple scroll events in the same frame', async () => {
    const t = await transcript()
    t.wheel(-710)
    t.move(290)
    t.frame()
    t.prepend(3000)
    t.finishLoad()
    await nextTick()
    t.move(280)
    t.move(270)
    t.frame()

    expect(t.el.scrollTop).toBe(3270)
    expect(t.loadOlder).toHaveBeenCalledTimes(1)
  })

  it('leaves native gesture movement alone when animation runs before the scroll event', async () => {
    const t = await transcript()
    t.wheel(-20)
    // Native/compositor movement can be visible before its scroll callback.
    t.el.scrollTop = 980
    t.writes.length = 0
    t.frame()
    t.scroll.onScroll()
    t.frame()

    expect(t.el.scrollTop).toBe(980)
    expect(t.writes).toEqual([])
  })

  it('tracks reading movement while history is in flight and later layout growth', async () => {
    const t = await transcript()
    t.wheel(-710)
    t.move(290)
    t.frame()
    t.wheel(50)
    t.move(340)
    t.frame()
    t.prepend(3000)
    t.finishLoad()
    await nextTick()
    t.frame()
    expect(t.el.scrollTop).toBe(3340)

    // Images and Markdown in the newly loaded history can settle later.
    t.prepend(200)
    t.frame()
    expect(t.el.scrollTop).toBe(3540)
    expect(t.loadOlder).toHaveBeenCalledTimes(1)
    expect(t.scroll.showJump.value).toBe(true)

    t.scroll.bottom()
    t.move(t.el.scrollHeight - t.el.clientHeight)
    t.scroll.onScrollEnd()
    t.frame()
    expect(t.el.scrollTop).toBe(t.el.scrollHeight - t.el.clientHeight)
    expect(t.scroll.showJump.value).toBe(false)
  })
})

describe('returning to the latest message', () => {
  it('starts and switches sessions at the bottom without showing the button or animating', async () => {
    const t = await transcript({ hasMore: false })
    expect(t.el.scrollTop).toBe(1000)
    expect(t.scroll.showJump.value).toBe(false)
    expect(t.scrollTo).not.toHaveBeenCalled()
    t.wheel(-500)
    t.move(500)
    t.frame()
    expect(t.scroll.showJump.value).toBe(true)

    t.sessionID.value = 'next-chat'
    await nextTick()
    t.frame()
    expect(t.el.scrollTop).toBe(1000)
    expect(t.scroll.showJump.value).toBe(false)
    expect(t.scrollTo).not.toHaveBeenCalled()
  })

  it('does not display the button when the entire conversation fits in one viewport', async () => {
    const t = await transcript({ height: 600, hasMore: false })
    t.wheel(-20)
    t.frame()
    expect(t.el.scrollTop).toBe(0)
    expect(t.scroll.showJump.value).toBe(false)
  })

  it('lets native smooth scrolling run without layout snapping it to the bottom', async () => {
    const t = await transcript({ hasMore: false })
    t.wheel(-500)
    t.move(500)
    t.frame()
    t.writes.length = 0
    t.scroll.bottom()
    t.frame()
    expect(t.scrollTo).toHaveBeenLastCalledWith({ top: 1600, behavior: 'smooth' })
    expect(t.el.scrollTop).toBe(500)
    expect(t.writes).toEqual([])
    t.move(750)
    t.frame()
    expect(t.el.scrollTop).toBe(750)
    expect(t.writes).toEqual([])

    t.move(1000)
    t.scroll.onScrollEnd()
    t.frame()
    expect(t.scroll.showJump.value).toBe(false)
    t.grow(200)
    t.frame()
    expect(t.el.scrollTop).toBe(1200)
  })

  it('cancels the animation immediately when the reader scrolls up', async () => {
    const t = await transcript({ hasMore: false })
    t.wheel(-500)
    t.move(500)
    t.frame()
    t.scroll.bottom()
    t.move(750)
    t.wheel(-20)
    expect(t.scrollTo).toHaveBeenLastCalledWith({ top: 750, behavior: 'instant' })
    t.move(730)
    t.frame()
    await vi.advanceTimersByTimeAsync(1000)
    t.frame()
    expect(t.el.scrollTop).toBe(730)
    expect(t.scroll.showJump.value).toBe(true)
  })

  it('reaches the new bottom if more text arrives during the animation', async () => {
    const t = await transcript({ hasMore: false })
    t.wheel(-500)
    t.move(500)
    t.frame()
    t.scroll.bottom()
    t.grow(200)
    t.frame()
    t.move(1000)
    t.scroll.onScrollEnd()
    expect(t.scrollTo).toHaveBeenLastCalledWith({ top: 1800, behavior: 'smooth' })
    t.move(1200)
    t.scroll.onScrollEnd()
    t.frame()
    expect(t.scroll.showJump.value).toBe(false)
  })

  it('finishes following even when the browser does not emit scrollend', async () => {
    const t = await transcript({ hasMore: false })
    t.wheel(-500)
    t.move(500)
    t.frame()
    t.scroll.bottom()
    t.move(1000)
    await vi.advanceTimersByTimeAsync(150)
    t.grow(200)
    t.frame()
    expect(t.el.scrollTop).toBe(1200)
  })

  it('uses direct positioning when reduced motion is preferred', async () => {
    const t = await transcript({ hasMore: false, reducedMotion: true })
    t.wheel(-500)
    t.move(500)
    t.frame()
    t.scroll.bottom()
    t.frame()
    expect(t.el.scrollTop).toBe(1000)
    expect(t.scrollTo).not.toHaveBeenCalled()
  })
})
