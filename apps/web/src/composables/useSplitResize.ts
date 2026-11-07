import { onScopeDispose, ref, watch } from 'vue'

interface SplitResizeOptions {
  width: () => number
  setWidth: (width: number) => void
  enabled?: () => boolean
}

interface Drag {
  target: HTMLElement
  pointerID: number
  x: number
  width: number
}

// Both splitters control a panel to their right. Bounds stay with the caller,
// which knows its current viewport and the space the adjacent content needs.
export function useSplitResize(options: SplitResizeOptions) {
  const resizing = ref(false)
  let drag: Drag | undefined

  function stop() {
    const previous = drag
    drag = undefined
    resizing.value = false
    if (!previous) return
    const { target, pointerID } = previous
    target.removeEventListener('pointermove', move)
    target.removeEventListener('pointerup', end)
    target.removeEventListener('pointercancel', end)
    target.removeEventListener('lostpointercapture', end)
    try {
      if (target.hasPointerCapture(pointerID)) target.releasePointerCapture(pointerID)
    } catch { /* The element or pointer may already have left the document. */ }
  }

  function move(event: PointerEvent) {
    if (!drag || event.pointerId !== drag.pointerID) return
    if (options.enabled?.() === false) { stop(); return }
    event.preventDefault()
    options.setWidth(drag.width + drag.x - event.clientX)
  }

  function end(event: PointerEvent) {
    if (event.pointerId === drag?.pointerID) stop()
  }

  function start(event: PointerEvent) {
    if (!event.isPrimary || event.button !== 0 || options.enabled?.() === false) return
    const target = event.currentTarget as HTMLElement | null
    if (!target?.setPointerCapture) return
    stop()
    event.preventDefault()
    drag = { target, pointerID: event.pointerId, x: event.clientX, width: options.width() }
    resizing.value = true
    target.addEventListener('pointermove', move)
    target.addEventListener('pointerup', end)
    target.addEventListener('pointercancel', end)
    target.addEventListener('lostpointercapture', end)
    try { target.setPointerCapture(event.pointerId) }
    catch { stop() }
  }

  watch(() => options.enabled?.() ?? true, enabled => { if (!enabled) stop() }, { flush: 'sync' })
  onScopeDispose(stop)
  return { resizing, start, stop }
}
