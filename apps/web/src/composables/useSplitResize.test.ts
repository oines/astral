import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref, type EffectScope } from 'vue'
import { useSplitResize } from './useSplitResize'

interface PointerInit { pointerId?: number; clientX?: number; button?: number; isPrimary?: boolean }
class Pointer extends Event {
  readonly pointerId: number
  readonly clientX: number
  readonly button: number
  readonly isPrimary: boolean
  constructor(type: string, init: PointerInit = {}) {
    super(type, { cancelable: true })
    this.pointerId = init.pointerId ?? 1
    this.clientX = init.clientX ?? 500
    this.button = init.button ?? 0
    this.isPrimary = init.isPrimary ?? true
  }
}
class Handle extends EventTarget {
  captures = new Set<number>()
  setPointerCapture = vi.fn((id: number) => { this.captures.add(id) })
  hasPointerCapture = vi.fn((id: number) => this.captures.has(id))
  releasePointerCapture = vi.fn((id: number) => {
    this.captures.delete(id)
    this.dispatchEvent(new Pointer('lostpointercapture', { pointerId: id }))
  })
  fire(type: string, init: PointerInit = {}) {
    const event = new Pointer(type, init)
    if (type === 'lostpointercapture') this.captures.delete(event.pointerId)
    this.dispatchEvent(event)
    return event
  }
}

const scopes: EffectScope[] = []
function setup() {
  const scope = effectScope()
  scopes.push(scope)
  const width = ref(620)
  const enabled = ref(true)
  const setWidth = vi.fn((value: number) => { width.value = value })
  const resize = scope.run(() => useSplitResize({ width: () => width.value, setWidth, enabled: () => enabled.value }))!
  function handle() {
    const target = new Handle()
    target.addEventListener('pointerdown', resize.start as EventListener)
    return target
  }
  return { scope, width, enabled, setWidth, resize, handle }
}
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop() })

describe('right panel split resizing', () => {
  it('captures the primary pointer, grows leftwards and ignores a second pointer', () => {
    const state = setup()
    const handle = state.handle()
    expect(handle.fire('pointerdown', { pointerId: 5 }).defaultPrevented).toBe(true)
    expect(handle.setPointerCapture).toHaveBeenCalledWith(5)
    expect(state.resize.resizing.value).toBe(true)
    expect(handle.fire('pointermove', { pointerId: 5, clientX: 460 }).defaultPrevented).toBe(true)
    expect(state.width.value).toBe(660)

    handle.fire('pointerdown', { pointerId: 9, isPrimary: false })
    expect(handle.fire('pointermove', { pointerId: 9, clientX: 300 }).defaultPrevented).toBe(false)
    handle.fire('pointerup', { pointerId: 9 })
    expect(state.width.value).toBe(660)
    expect(state.resize.resizing.value).toBe(true)
    expect(handle.hasPointerCapture(5)).toBe(true)

    handle.fire('pointermove', { pointerId: 5, clientX: 540 })
    expect(state.width.value).toBe(580)
    handle.fire('pointerup', { pointerId: 5 })
    expect(state.resize.resizing.value).toBe(false)
    expect(handle.hasPointerCapture(5)).toBe(false)
    expect(handle.releasePointerCapture).toHaveBeenCalledOnce()
    handle.fire('pointermove', { pointerId: 5, clientX: 200 })
    expect(state.width.value).toBe(580)
  })

  it('leaves secondary buttons, non-primary pointers and disabled handles untouched', () => {
    const state = setup()
    const handle = state.handle()
    expect(handle.fire('pointerdown', { button: 2 }).defaultPrevented).toBe(false)
    expect(handle.fire('pointerdown', { isPrimary: false }).defaultPrevented).toBe(false)
    state.enabled.value = false
    expect(handle.fire('pointerdown').defaultPrevented).toBe(false)
    expect(handle.setPointerCapture).not.toHaveBeenCalled()
    expect(state.setWidth).not.toHaveBeenCalled()
    expect(state.resize.resizing.value).toBe(false)
  })

  it.each(['pointerup', 'pointercancel', 'lostpointercapture'])('cleans every drag listener after %s', type => {
    const state = setup()
    const handle = state.handle()
    const remove = vi.spyOn(handle, 'removeEventListener')
    handle.fire('pointerdown')
    handle.fire('pointermove', { clientX: 480 })
    handle.fire(type)
    expect(state.resize.resizing.value).toBe(false)
    expect(handle.hasPointerCapture(1)).toBe(false)
    expect(remove.mock.calls.map(([name]) => name).sort()).toEqual(['lostpointercapture', 'pointercancel', 'pointermove', 'pointerup'])
    handle.fire('pointermove', { clientX: 400 })
    expect(state.width.value).toBe(640)
    expect(state.setWidth).toHaveBeenCalledOnce()
    state.resize.stop()
    expect(remove).toHaveBeenCalledTimes(4)
  })

  it('releases an old capture before starting again from the latest width and target', () => {
    const state = setup()
    const first = state.handle()
    const second = state.handle()
    first.fire('pointerdown')
    first.fire('pointermove', { clientX: 470 })
    expect(state.width.value).toBe(650)
    second.fire('pointerdown', { pointerId: 2, clientX: 700 })
    expect(first.releasePointerCapture).toHaveBeenCalledWith(1)
    expect(first.hasPointerCapture(1)).toBe(false)
    first.fire('pointermove', { clientX: 100 })
    expect(state.width.value).toBe(650)
    second.fire('pointermove', { pointerId: 2, clientX: 650 })
    expect(state.width.value).toBe(700)
    state.scope.stop()
    expect(state.resize.resizing.value).toBe(false)
    expect(second.releasePointerCapture).toHaveBeenCalledWith(2)
    second.fire('pointermove', { pointerId: 2, clientX: 300 })
    expect(state.width.value).toBe(700)
  })

  it('ends an in-progress drag immediately when its owning panel disables resizing', () => {
    const state = setup()
    const handle = state.handle()
    handle.fire('pointerdown')
    state.enabled.value = false
    expect(state.resize.resizing.value).toBe(false)
    expect(handle.hasPointerCapture(1)).toBe(false)
    handle.fire('pointermove', { clientX: 200 })
    expect(state.setWidth).not.toHaveBeenCalled()
    state.enabled.value = true
    handle.fire('pointerdown', { pointerId: 2 })
    expect(state.resize.resizing.value).toBe(true)
  })

  it('cleans up if the browser refuses pointer capture', () => {
    const state = setup()
    const handle = state.handle()
    const remove = vi.spyOn(handle, 'removeEventListener')
    handle.setPointerCapture.mockImplementationOnce(() => { throw new Error('The pointer is no longer active') })
    handle.fire('pointerdown')
    expect(state.resize.resizing.value).toBe(false)
    expect(remove).toHaveBeenCalledTimes(4)
    handle.fire('pointermove', { clientX: 100 })
    expect(state.setWidth).not.toHaveBeenCalled()
  })
})
