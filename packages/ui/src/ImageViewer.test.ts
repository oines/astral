/* eslint-disable vue/one-component-per-file -- Dialog primitives are isolated renderer stubs. */
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { createRenderer, defineComponent, h, nextTick, reactive, ssrContextKey, type Component, type RenderFunction } from 'vue'
import { compileScript, compileTemplate, parse } from 'vue/compiler-sfc'
import Button from './Button.vue'
import ImageViewer from './ImageViewer.vue'
import type { ImageViewerItem, ImageViewerLabels } from './image-viewer-types'

vi.mock('reka-ui', () => {
  const primitive = (tag: string) => defineComponent({
    inheritAttrs: false,
    setup(_props, { attrs, slots }) { return () => h(tag, attrs, slots.default?.()) },
  })
  return {
    DialogRoot: defineComponent({
      props: { open: Boolean },
      setup(props, { slots }) { return () => props.open ? h('div', slots.default?.()) : null },
    }),
    DialogPortal: primitive('div'), DialogOverlay: primitive('div'), DialogContent: primitive('div'),
    DialogTitle: primitive('h2'), DialogClose: primitive('button'),
  }
})

// Vitest compiles SFCs for SSR. Compile their actual templates for this renderer
// so tests exercise real event bindings, rendered transforms and Button feedback.
function clientRender(filename: string): RenderFunction {
  const { descriptor } = parse(readFileSync(new URL(filename, import.meta.url), 'utf8'))
  const script = compileScript(descriptor, { id: filename })
  const result = compileTemplate({
    filename, id: filename, source: descriptor.template!.content,
    compilerOptions: { mode: 'function', bindingMetadata: script.bindings },
  })
  if (result.errors.length) throw new Error(result.errors.map(String).join('\n'))
  return new Function('Vue', result.code)(Vue) as RenderFunction
}
Object.assign(Button, { render: clientRender('./Button.vue') })
const clientViewer = { ...ImageViewer, render: clientRender('./ImageViewer.vue') } as Component

class TestNode {
  children: TestNode[] = []
  parent?: TestNode
  props: Record<string, unknown> = {}
  listeners = new Map<string, Set<EventListener>>()
  clientWidth = 800
  clientHeight = 600
  setPointerCapture = vi.fn()
  constructor(readonly type: string, public text = '') {}
  getBoundingClientRect() { return { left: 40, top: 60, width: this.clientWidth, height: this.clientHeight } }
  addEventListener(type: string, listener: EventListener) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set())
    this.listeners.get(type)!.add(listener)
  }
  removeEventListener(type: string, listener: EventListener) { this.listeners.get(type)?.delete(listener) }
}

const renderer = createRenderer<TestNode, TestNode>({
  createElement: type => new TestNode(type),
  createText: text => new TestNode('#text', text),
  createComment: text => new TestNode('#comment', text),
  insert(child, parent, anchor) {
    if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1)
    child.parent = parent
    const at = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(at < 0 ? parent.children.length : at, 0, child)
  },
  remove(child) { if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1) },
  setText: (node, text) => { node.text = text },
  setElementText: (node, text) => { node.text = text; node.children = [] },
  parentNode: node => node.parent ?? null,
  nextSibling(node) { const siblings = node.parent?.children; return siblings?.[siblings.indexOf(node) + 1] ?? null },
  patchProp: (node, key, _previous, value) => { node.props[key] = value },
})

const images: TestImage[] = []
class TestImage {
  src = ''
  naturalWidth = 0
  naturalHeight = 0
  onload: ((event: Event) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  constructor() { images.push(this) }
  removeAttribute(name: string) { if (name === 'src') this.src = '' }
  succeed(width = 1600, height = 1200) {
    this.naturalWidth = width
    this.naturalHeight = height
    this.onload?.(new Event('load'))
  }
  fail() { this.onerror?.(new Event('error')) }
}

const labels: ImageViewerLabels = {
  title: 'Image preview', close: 'Close', previous: 'Previous', next: 'Next', zoomIn: 'Zoom in', zoomOut: 'Zoom out',
  fit: 'Fit', download: 'Download original', downloadPreview: 'Download preview', loading: 'Loading', failed: 'Image unavailable',
  originalUnavailable: 'Original unavailable', retry: 'Retry',
}
const disposals: (() => void)[] = []
beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-11-01T12:00:00Z'))
  images.length = 0
  vi.stubGlobal('Image', TestImage)
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
})
afterEach(() => {
  for (const dispose of disposals.splice(0)) dispose()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

function find(node: TestNode, predicate: (node: TestNode) => boolean): TestNode | undefined {
  if (predicate(node)) return node
  for (const child of node.children) {
    const found = find(child, predicate)
    if (found) return found
  }
}
function hasClass(node: TestNode, name: string) { return String(node.props.class ?? '').split(' ').includes(name) }
function text(node: TestNode): string { return node.text + node.children.map(text).join('') }
async function fire(node: TestNode, type: string, fields: Record<string, unknown> = {}) {
  const event = Object.assign(new Event(type, { cancelable: true }), fields)
  const handler = node.props[`on${type[0]!.toUpperCase()}${type.slice(1)}`]
  if (typeof handler === 'function') handler(event)
  for (const listener of node.listeners.get(type) ?? []) listener(event)
  await nextTick()
  return event
}

async function mount(items: ImageViewerItem[] = [{ src: '/preview' }]) {
  const props = reactive({ open: true, items, index: 0, labels })
  const root = new TestNode('root')
  const app = renderer.createApp(defineComponent({ setup: () => () => h(clientViewer, props) }))
  app.provide(ssrContextKey, { modules: new Set() })
  app.mount(root)
  disposals.push(() => app.unmount())
  await nextTick()
  const byClass = (name: string) => find(root, node => hasClass(node, name))
  const stage = () => byClass('image-viewer-viewport')!
  const image = () => byClass('image-viewer-image')!
  const pose = () => {
    const style = image().props.style as { transform: string }
    const match = style.transform.match(/translate\(([-\d.e]+)px, ([-\d.e]+)px\) scale\(([-\d.e]+)\)$/)!
    return { x: Number(match[1]), y: Number(match[2]), zoom: Number(match[3]) }
  }
  const load = async (src = props.items[props.index]!.src) => {
    const image = images.findLast(image => image.src === src)!
    image.succeed()
    await nextTick()
    return image
  }
  const pointer = (type: string, id: number, x: number, y: number) => fire(stage(), type, { pointerType: 'touch', pointerId: id, button: 0, clientX: x, clientY: y })
  const wheel = (fields: Record<string, unknown>) => fire(stage(), 'wheel', { clientX: 440, clientY: 360, deltaMode: 0, deltaX: 0, deltaY: 0, ctrlKey: false, metaKey: false, ...fields })
  return { props, root, byClass, stage, image, pose, load, pointer, wheel }
}

describe('ImageViewer input events', () => {
  it('anchors macOS pinch wheel at the cursor, then pans with ordinary two-finger wheel', async () => {
    const view = await mount()
    await view.load()
    const pinch = await view.wheel({ ctrlKey: true, deltaY: -Math.log(2) / 0.01, clientX: 540, clientY: 410 })
    expect(pinch.defaultPrevented).toBe(true)
    expect(view.pose()).toEqual({ zoom: 2, x: -100, y: -50 })
    await view.wheel({ deltaX: 30, deltaY: -20 })
    expect(view.pose()).toEqual({ zoom: 2, x: -130, y: -30 })
    await view.wheel({ metaKey: true, deltaY: -Math.log(2) / 0.01 })
    expect(view.pose()).toEqual({ zoom: 4, x: -260, y: -60 })
    await fire(find(view.root, node => node.props['aria-label'] === labels.fit)!, 'click')
    await view.wheel({ deltaX: 100, deltaY: 100 })
    expect(view.pose()).toEqual({ zoom: 1, x: 0, y: 0 })
  })

  it('uses Safari gesture scaling once and ignores duplicate pinch wheel until the gesture ends', async () => {
    const view = await mount()
    await view.load()
    await fire(view.stage(), 'gesturestart', { scale: 1, clientX: 540, clientY: 410 })
    await view.wheel({ ctrlKey: true, deltaY: -Math.log(2) / 0.01 })
    expect(view.pose().zoom).toBe(1)
    await fire(view.stage(), 'gesturechange', { scale: 2 })
    expect(view.pose()).toEqual({ zoom: 2, x: -100, y: -50 })
    await view.wheel({ ctrlKey: true, deltaY: -Math.log(2) / 0.01 })
    expect(view.pose().zoom).toBe(2)
    await fire(view.stage(), 'gestureend')
    await view.wheel({ ctrlKey: true, deltaY: -Math.log(2) / 0.01 })
    expect(view.pose().zoom).toBe(2)
    await vi.advanceTimersByTimeAsync(101)
    await view.wheel({ ctrlKey: true, deltaY: -Math.log(2) / 0.01 })
    expect(view.pose().zoom).toBe(4)
  })

  it('tracks phone pinch centroid motion and continues with a single finger without jumping', async () => {
    const view = await mount()
    await view.load()
    await view.pointer('pointerdown', 1, 390, 360)
    await view.pointer('pointerdown', 2, 490, 360)
    await view.pointer('pointermove', 1, 340, 360)
    await view.pointer('pointermove', 2, 540, 360)
    expect(view.pose().zoom).toBeCloseTo(2)
    await view.pointer('pointermove', 1, 370, 380)
    await view.pointer('pointermove', 2, 570, 380)
    expect(view.pose().zoom).toBeCloseTo(2)
    expect(view.pose().x).toBeCloseTo(30)
    expect(view.pose().y).toBeCloseTo(20)
    // Native Safari gesture events must not scale a pointer pinch a second time.
    await fire(view.stage(), 'gesturestart', { scale: 1 })
    await fire(view.stage(), 'gesturechange', { scale: 3 })
    expect(view.pose().zoom).toBeCloseTo(2)
    await view.pointer('pointerup', 2, 570, 380)
    await view.pointer('pointermove', 1, 400, 400)
    expect(view.pose().x).toBeCloseTo(60)
    expect(view.pose().y).toBeCloseTo(40)
    await view.pointer('pointerup', 1, 400, 400)
    const settled = view.pose()
    await view.pointer('pointermove', 1, 500, 500)
    expect(view.pose()).toEqual(settled)
    expect(view.stage().setPointerCapture).toHaveBeenCalledWith(1)
    expect(view.stage().setPointerCapture).toHaveBeenCalledWith(2)
  })

  it('toggles phone double taps and ignores their synthesized mouse double-click', async () => {
    const view = await mount()
    await view.load()
    const tap = async () => {
      await view.pointer('pointerdown', 1, 540, 410)
      await vi.advanceTimersByTimeAsync(30)
      await view.pointer('pointerup', 1, 540, 410)
    }
    await tap()
    expect(view.pose().zoom).toBe(1)
    await vi.advanceTimersByTimeAsync(70)
    await tap()
    expect(view.pose()).toEqual({ zoom: 2, x: -100, y: -50 })
    await fire(view.stage(), 'dblclick', { clientX: 540, clientY: 410 })
    expect(view.pose().zoom).toBe(2)
    await vi.advanceTimersByTimeAsync(400)
    await tap()
    await vi.advanceTimersByTimeAsync(70)
    await tap()
    expect(view.pose()).toEqual({ zoom: 1, x: 0, y: 0 })
  })
})

describe('ImageViewer image loading', () => {
  it('isolates late image callbacks after navigation and after closing', async () => {
    const view = await mount([{ src: '/first-preview', originalSrc: '/first-original' }, { src: '/second-preview', originalSrc: '/second-original' }])
    const firstPreview = images[0]!
    const firstOriginal = images[1]!
    const latePreview = firstPreview.onload!
    const lateOriginal = firstOriginal.onload!
    const lateFailure = firstOriginal.onerror!
    await view.load('/first-preview')
    await view.wheel({ ctrlKey: true, deltaY: -Math.log(2) / 0.01 })
    view.props.index = 1
    await nextTick()
    await view.load('/second-preview')
    latePreview(new Event('load'))
    lateOriginal(new Event('load'))
    lateFailure(new Event('error'))
    await nextTick()
    expect(view.image().props.src).toBe('/second-preview')
    expect(view.pose()).toEqual({ zoom: 1, x: 0, y: 0 })
    expect(view.byClass('image-viewer-feedback')).toBeUndefined()
    expect(firstPreview.onload).toBeNull()
    expect(firstOriginal.onerror).toBeNull()

    const secondOriginal = images.find(image => image.src === '/second-original')!
    const lateAfterClose = secondOriginal.onload!
    const stage = view.stage()
    view.props.open = false
    await nextTick()
    lateAfterClose(new Event('load'))
    await nextTick()
    expect(view.image()).toBeUndefined()
    expect([...stage.listeners.values()].every(listeners => listeners.size === 0)).toBe(true)
    view.props.open = true
    await nextTick()
    expect(view.image()).toBeUndefined()
    await view.load('/second-preview')
    expect(view.image().props.src).toBe('/second-preview')
  })

  it('keeps the historical preview, failure and preview download throughout an original retry', async () => {
    const view = await mount([{ src: '/history-preview', originalSrc: '/original', downloadUrl: '/download-original' }])
    await view.load()
    const original = images.find(image => image.src === '/original')!
    original.fail()
    await nextTick()
    expect(view.image().props.src).toBe('/history-preview')
    expect(text(view.byClass('image-viewer-feedback')!)).toContain(labels.originalUnavailable)
    const download = () => view.byClass('image-viewer-download')!
    expect(download().props.href).toBe('/history-preview')
    expect(download().props['aria-label']).toBe(labels.downloadPreview)
    const retry = find(view.byClass('image-viewer-feedback')!, node => node.type === 'button')!
    await fire(retry, 'click')
    expect(view.byClass('image-viewer-feedback')).toBeDefined()
    expect(view.image().props.src).toBe('/history-preview')
    expect(download().props.href).toBe('/history-preview')
    expect(retry.props.disabled).toBe(true)
    expect(find(retry, node => hasClass(node, 'spinner'))).toBeUndefined()
    await vi.advanceTimersByTimeAsync(299)
    expect(find(retry, node => hasClass(node, 'spinner'))).toBeUndefined()
    await vi.advanceTimersByTimeAsync(1)
    expect(find(retry, node => hasClass(node, 'spinner'))).toBeDefined()
    images.findLast(image => image.src === '/original')!.fail()
    await nextTick()
    expect(view.byClass('image-viewer-feedback')).toBeDefined()
    expect(retry.props.disabled).toBe(false)
    expect(find(retry, node => hasClass(node, 'spinner'))).toBeUndefined()
    expect(download().props.href).toBe('/history-preview')
    await fire(retry, 'click')
    await view.load('/original')
    expect(view.image().props.src).toBe('/original')
    expect(view.byClass('image-viewer-feedback')).toBeUndefined()
    expect(download().props.href).toBe('/download-original')
    expect(download().props['aria-label']).toBe(labels.download)
  })

  it('delays loading feedback and removes it immediately when the preview is ready', async () => {
    const view = await mount()
    await vi.advanceTimersByTimeAsync(299)
    expect(view.byClass('image-viewer-loading')).toBeUndefined()
    await vi.advanceTimersByTimeAsync(1)
    expect(view.byClass('image-viewer-loading')?.props['aria-label']).toBe(labels.loading)
    await view.load()
    expect(view.byClass('image-viewer-loading')).toBeUndefined()
  })
})
