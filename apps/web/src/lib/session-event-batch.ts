import type { SessionEvent } from './types'

// Only incremental previews wait for the next paint. Durable commits and status
// transitions flush the preceding deltas first, in their original wire order.
export function isSessionDelta(event: SessionEvent) {
  return !event.entry && !event.view && ['message_update', 'tool_execution_update'].includes(event.core?.type ?? '')
}
export function createSessionEventBatch(consume: (events: SessionEvent[]) => void) {
  let pending: SessionEvent[] = []
  let frame: number | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  function unschedule() {
    if (frame !== undefined) globalThis.cancelAnimationFrame(frame)
    clearTimeout(timer)
    frame = undefined
    timer = undefined
  }
  function flush() {
    unschedule()
    const events = pending
    pending = []
    if (events.length) consume(events)
  }
  function clear() { unschedule(); pending = [] }
  function push(event: SessionEvent) {
    pending.push(event)
    if (!isSessionDelta(event) || pending.length >= 512) { flush(); return }
    if (timer !== undefined) return
    if (typeof globalThis.requestAnimationFrame === 'function') frame = globalThis.requestAnimationFrame(flush)
    // Animation frames stop in background tabs. Keep the queue bounded there,
    // and flush immediately on visibilitychange when the tab returns.
    timer = setTimeout(flush, frame === undefined ? 16 : 100)
  }
  return { push, flush, clear }
}
