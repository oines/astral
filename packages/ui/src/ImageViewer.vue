<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowLeft, ArrowRight, Download, Maximize2, X, ZoomIn, ZoomOut } from '@astral/icons'
import { DialogClose, DialogContent, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'
import Button from './Button.vue'
import { useDelayedPending } from './useDelayedPending'
import { clampImageView, fitImage, MAX_IMAGE_ZOOM, pinchImage, refitImageView, zoomImageAt, type ImagePoint, type ImageView } from './image-viewer-geometry'
import type { ImageViewerItem, ImageViewerLabels } from './image-viewer-types'

const props = defineProps<{ open: boolean; items: ImageViewerItem[]; index: number; labels: ImageViewerLabels }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; 'update:index': [value: number] }>()

const activeIndex = computed(() => Math.max(0, Math.min(props.items.length - 1, props.index)))
const item = computed(() => props.items[activeIndex.value])
const viewerOpen = computed(() => props.open && !!item.value)
const viewportElement = ref<HTMLElement>()
const viewport = ref({ width: 0, height: 0 })
const natural = ref({ width: 0, height: 0 })
const fitted = computed(() => fitImage(natural.value, viewport.value))
const view = ref<ImageView>({ zoom: 1, x: 0, y: 0 })
const displayedSrc = ref('')
type LoadState = 'idle' | 'loading' | 'ready' | 'failed'
const previewState = ref<LoadState>('idle')
const originalState = ref<LoadState>('idle')
const originalFailed = ref(false)
const reload = ref(0)
const pending = computed(() => viewerOpen.value && !displayedSrc.value && (previewState.value === 'loading' || originalState.value === 'loading'))
const waiting = useDelayedPending(pending)
const failed = computed(() => !displayedSrc.value && !pending.value && previewState.value === 'failed')
const originalUnavailable = computed(() => !!displayedSrc.value && originalFailed.value)
const previewDownload = computed(() => originalFailed.value)
const downloadHref = computed(() => previewDownload.value ? item.value?.src : item.value?.downloadUrl || item.value?.originalSrc || item.value?.src)
const downloadLabel = computed(() => previewDownload.value ? props.labels.downloadPreview : props.labels.download)
const imageStyle = computed(() => ({
  width: `${fitted.value.width}px`,
  height: `${fitted.value.height}px`,
  transform: `translate(-50%, -50%) translate(${view.value.x}px, ${view.value.y}px) scale(${view.value.zoom})`,
}))
let retryOriginal: (() => void) | undefined

const pointers = new Map<number, ImagePoint>()
let tap: { point: ImagePoint; time: number; moved: boolean } | undefined
let lastTap: { point: ImagePoint; time: number } | undefined
let lastTouchAt = 0
type SafariGesture = Event & { scale?: number; clientX?: number; clientY?: number }
let safariGesture: { view: ImageView; anchor: ImagePoint; scale: number } | undefined
let safariEndedAt = 0
const surfacePointers = new Set<number>()
let blankPress: { id: number; point: ImagePoint; startedOnImage: boolean; moved: boolean } | undefined
let ignorePointerClick = false

function resetInteraction() {
  pointers.clear()
  tap = undefined
  lastTap = undefined
  safariGesture = undefined
  safariEndedAt = 0
}

watch(() => [viewerOpen.value, activeIndex.value, item.value?.src, item.value?.originalSrc, reload.value] as const, ([open, , src, original], _, onCleanup) => {
  displayedSrc.value = ''
  natural.value = { width: 0, height: 0 }
  view.value = { zoom: 1, x: 0, y: 0 }
  previewState.value = 'idle'
  originalState.value = 'idle'
  originalFailed.value = false
  resetInteraction()
  surfacePointers.clear()
  blankPress = undefined
  ignorePointerClick = false
  retryOriginal = undefined
  if (!open || !src) return

  let current = true
  let preview: HTMLImageElement | undefined
  let full: HTMLImageElement | undefined
  function dispose(image: HTMLImageElement | undefined) {
    if (!image) return
    image.onload = null
    image.onerror = null
    image.removeAttribute('src')
  }
  function display(image: HTMLImageElement, url: string) {
    displayedSrc.value = url
    natural.value = { width: image.naturalWidth, height: image.naturalHeight }
  }
  previewState.value = 'loading'
  preview = new Image()
  preview.onload = () => {
    if (!current || !preview) return
    previewState.value = 'ready'
    if (originalState.value !== 'ready') display(preview, src)
  }
  preview.onerror = () => { if (current) previewState.value = 'failed' }
  preview.src = src

  function startOriginal() {
    if (!original || original === src) return
    dispose(full)
    const image = new Image()
    full = image
    originalState.value = 'loading'
    image.onload = () => {
      if (!current || full !== image) return
      originalState.value = 'ready'
      originalFailed.value = false
      display(image, original)
    }
    image.onerror = () => {
      if (!current || full !== image) return
      originalState.value = 'failed'
      originalFailed.value = true
    }
    image.src = original
  }
  retryOriginal = startOriginal
  startOriginal()
  onCleanup(() => {
    current = false
    retryOriginal = undefined
    dispose(preview)
    dispose(full)
  })
}, { immediate: true, flush: 'sync' })

watch(fitted, (next, previous) => {
  view.value = refitImageView(view.value, previous, next, viewport.value)
  resetInteraction()
})

function retry() {
  if (displayedSrc.value) {
    if (originalState.value === 'failed') retryOriginal?.()
  } else reload.value++
}

function fit() {
  view.value = { zoom: 1, x: 0, y: 0 }
  resetInteraction()
}

function zoomTo(zoom: number, anchor: ImagePoint = { x: 0, y: 0 }) {
  if (!displayedSrc.value) return
  view.value = zoomImageAt(view.value, zoom, anchor, fitted.value, viewport.value)
}

function pointAt(clientX: number, clientY: number): ImagePoint {
  const bounds = viewportElement.value?.getBoundingClientRect()
  return bounds ? { x: clientX - bounds.left - bounds.width / 2, y: clientY - bounds.top - bounds.height / 2 } : { x: 0, y: 0 }
}

function isOnImage(point: ImagePoint) {
  if (!displayedSrc.value || Math.abs(point.x) > viewport.value.width / 2 || Math.abs(point.y) > viewport.value.height / 2) return false
  return Math.abs(point.x - view.value.x) <= fitted.value.width * view.value.zoom / 2
    && Math.abs(point.y - view.value.y) <= fitted.value.height * view.value.zoom / 2
}

function isOnControls(event: Event) {
  return event.target instanceof Element && !!event.target.closest('[data-image-viewer-controls]')
}

function onSurfacePointerDown(event: PointerEvent) {
  if (event.pointerType !== 'touch' && event.button !== 0) return
  ignorePointerClick = true
  surfacePointers.add(event.pointerId)
  const point = pointAt(event.clientX, event.clientY)
  blankPress = surfacePointers.size === 1 && !isOnControls(event)
    ? { id: event.pointerId, point, startedOnImage: isOnImage(point), moved: false }
    : undefined
}

function onSurfacePointerMove(event: PointerEvent) {
  if (!blankPress || blankPress.id !== event.pointerId) return
  const point = pointAt(event.clientX, event.clientY)
  if (Math.hypot(point.x - blankPress.point.x, point.y - blankPress.point.y) > 8) blankPress.moved = true
}

function onSurfacePointerEnd(event: PointerEvent) {
  if (!surfacePointers.has(event.pointerId)) return
  const point = pointAt(event.clientX, event.clientY)
  const close = event.type === 'pointerup' && surfacePointers.size === 1 && blankPress?.id === event.pointerId
    && !blankPress.moved && !blankPress.startedOnImage && !isOnImage(point) && !isOnControls(event)
  surfacePointers.delete(event.pointerId)
  blankPress = undefined
  // Closing on pointerup also works when touch preventDefault suppresses click.
  // Keep its subsequent captured/synthesized click from becoming another action.
  if (close) emit('update:open', false)
}

function onBlankClick(event: MouseEvent) {
  if (ignorePointerClick) {
    ignorePointerClick = false
    return
  }
  if (event.button !== 0 || event.defaultPrevented || isOnControls(event) || isOnImage(pointAt(event.clientX, event.clientY))) return
  emit('update:open', false)
}

function toggleZoom(point: ImagePoint) {
  if (view.value.zoom > 1) fit()
  else zoomTo(2, point)
}

function navigate(delta: number) {
  const next = activeIndex.value + delta
  if (next >= 0 && next < props.items.length) emit('update:index', next)
}

function onKeydown(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  if (event.key === 'ArrowLeft') navigate(-1)
  else if (event.key === 'ArrowRight') navigate(1)
  else if (event.key === '+' || event.key === '=') zoomTo(view.value.zoom * 1.5)
  else if (event.key === '-') zoomTo(view.value.zoom / 1.5)
  else if (event.key === '0') fit()
  else return
  event.preventDefault()
}

function onWheel(event: WheelEvent) {
  event.preventDefault()
  if (!displayedSrc.value || pointers.size >= 2) return
  if (event.ctrlKey || event.metaKey) {
    // Safari can emit WebKit gestures alongside wheel events for one pinch.
    if (safariGesture || Date.now() - safariEndedAt < 100) return
    const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? viewport.value.height : 1
    zoomTo(view.value.zoom * Math.exp(-event.deltaY * unit * 0.01), pointAt(event.clientX, event.clientY))
  } else if (view.value.zoom > 1) {
    const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? viewport.value.height : 1
    view.value = clampImageView({ ...view.value, x: view.value.x - event.deltaX * unit, y: view.value.y - event.deltaY * unit }, fitted.value, viewport.value)
  }
}

function onGestureStart(event: SafariGesture) {
  event.preventDefault()
  if (pointers.size >= 2 || !displayedSrc.value) return
  const anchor = typeof event.clientX === 'number' && typeof event.clientY === 'number' ? pointAt(event.clientX, event.clientY) : { x: 0, y: 0 }
  safariGesture = { view: { ...view.value }, anchor, scale: event.scale || 1 }
}

function onGestureChange(event: SafariGesture) {
  event.preventDefault()
  if (!safariGesture || pointers.size >= 2 || !event.scale) return
  view.value = zoomImageAt(safariGesture.view, safariGesture.view.zoom * event.scale / safariGesture.scale, safariGesture.anchor, fitted.value, viewport.value)
}

function onGestureEnd(event: Event) {
  event.preventDefault()
  if (safariGesture) safariEndedAt = Date.now()
  safariGesture = undefined
}

watch(viewportElement, (element, _, onCleanup) => {
  if (!element) return
  const measure = () => { viewport.value = { width: element.clientWidth, height: element.clientHeight } }
  measure()
  const observer = new ResizeObserver(measure)
  observer.observe(element)
  element.addEventListener('wheel', onWheel, { passive: false })
  element.addEventListener('gesturestart', onGestureStart, { passive: false })
  element.addEventListener('gesturechange', onGestureChange, { passive: false })
  element.addEventListener('gestureend', onGestureEnd, { passive: false })
  onCleanup(() => {
    observer.disconnect()
    element.removeEventListener('wheel', onWheel)
    element.removeEventListener('gesturestart', onGestureStart)
    element.removeEventListener('gesturechange', onGestureChange)
    element.removeEventListener('gestureend', onGestureEnd)
  })
}, { flush: 'post' })

function onPointerDown(event: PointerEvent) {
  if (!displayedSrc.value || (event.pointerType !== 'touch' && event.button !== 0)) return
  // Keep native mouse click/double-click synthesis; touch taps are handled here.
  if (event.pointerType === 'touch') event.preventDefault()
  const point = pointAt(event.clientX, event.clientY)
  pointers.set(event.pointerId, point)
  if (pointers.size === 1) tap = { point, time: Date.now(), moved: false }
  else {
    tap = undefined
    lastTap = undefined
    safariGesture = undefined
  }
  try { viewportElement.value?.setPointerCapture(event.pointerId) }
  catch { /* Synthetic pointer events need not correspond to a native pointer. */ }
}

function pointerPair(): [ImagePoint, ImagePoint] | undefined {
  const values = [...pointers.values()]
  return values[0] && values[1] ? [values[0], values[1]] : undefined
}

function onPointerMove(event: PointerEvent) {
  const previous = pointers.get(event.pointerId)
  if (!previous) return
  event.preventDefault()
  const before = pointerPair()
  const point = pointAt(event.clientX, event.clientY)
  pointers.set(event.pointerId, point)
  if (tap && Math.hypot(point.x - tap.point.x, point.y - tap.point.y) > 8) tap.moved = true
  const after = pointerPair()
  if (before && after) view.value = pinchImage(view.value, before, after, fitted.value, viewport.value)
  else if (view.value.zoom > 1) view.value = clampImageView({ ...view.value, x: view.value.x + point.x - previous.x, y: view.value.y + point.y - previous.y }, fitted.value, viewport.value)
}

function onPointerEnd(event: PointerEvent) {
  if (!pointers.has(event.pointerId)) return
  const point = pointAt(event.clientX, event.clientY)
  const now = Date.now()
  if (event.pointerType === 'touch') {
    lastTouchAt = now
    if (event.type === 'pointerup' && pointers.size === 1 && tap && !tap.moved && now - tap.time < 250) {
      if (lastTap && now - lastTap.time < 300 && Math.hypot(point.x - lastTap.point.x, point.y - lastTap.point.y) < 32) {
        toggleZoom(point)
        lastTap = undefined
      } else lastTap = { point, time: now }
    }
  }
  pointers.delete(event.pointerId)
  tap = undefined
}

function onDoubleClick(event: MouseEvent) {
  event.preventDefault()
  if (Date.now() - lastTouchAt > 500) toggleZoom(pointAt(event.clientX, event.clientY))
}

function focusClose(event: Event) {
  if (!(event.target instanceof HTMLElement)) return
  event.preventDefault()
  event.target.querySelector<HTMLElement>('[data-viewer-close]')?.focus({ preventScroll: true })
}
</script>

<template>
  <DialogRoot
    :open="viewerOpen"
    @update:open="emit('update:open', $event)"
  >
    <DialogPortal>
      <DialogOverlay class="image-viewer-overlay" />
      <DialogContent
        class="image-viewer"
        :aria-describedby="undefined"
        @open-auto-focus="focusClose"
        @keydown="onKeydown"
        @pointerdown.capture="onSurfacePointerDown"
        @pointermove.capture="onSurfacePointerMove"
        @pointerup.capture="onSurfacePointerEnd"
        @pointercancel.capture="onSurfacePointerEnd"
        @click="onBlankClick"
      >
        <header
          class="image-viewer-header"
        >
          <DialogTitle
            class="image-viewer-title"
            :title="item?.name || labels.title"
            data-image-viewer-controls
          >
            {{ item?.name || labels.title }}
          </DialogTitle>
          <DialogClose
            class="button button-ghost image-viewer-control"
            data-viewer-close
            data-image-viewer-controls
            :aria-label="labels.close"
            :title="labels.close"
          >
            <X aria-hidden="true" />
          </DialogClose>
        </header>
        <div
          ref="viewportElement"
          class="image-viewer-viewport"
          :class="{ 'image-viewer-zoomed': view.zoom > 1 }"
          @pointerdown="onPointerDown"
          @pointermove="onPointerMove"
          @pointerup="onPointerEnd"
          @pointercancel="onPointerEnd"
          @lostpointercapture="onPointerEnd"
          @dblclick="onDoubleClick"
        >
          <img
            v-if="displayedSrc"
            class="image-viewer-image"
            :src="displayedSrc"
            :alt="item?.name || labels.title"
            :style="imageStyle"
            draggable="false"
            decoding="async"
          >
          <span
            v-if="waiting"
            class="image-viewer-loading"
            role="status"
            :aria-label="labels.loading"
          ><span class="image-viewer-loading-indicator"><span
            class="spinner"
            aria-hidden="true"
          /></span></span>
          <div
            v-if="failed"
            class="image-viewer-failure"
            role="status"
          >
            <span>{{ labels.failed }}</span>
            <Button
              variant="secondary"
              data-image-viewer-controls
              @click.stop="retry"
            >
              {{ labels.retry }}
            </Button>
          </div>
        </div>
        <div
          v-if="originalUnavailable"
          class="image-viewer-feedback"
          role="status"
        >
          <span data-image-viewer-controls>{{ labels.originalUnavailable }}</span>
          <Button
            variant="ghost"
            :busy="originalState === 'loading'"
            data-image-viewer-controls
            @click="retry"
          >
            {{ labels.retry }}
          </Button>
        </div>
        <footer
          class="image-viewer-toolbar"
        >
          <div
            v-if="items.length > 1"
            class="image-viewer-navigation"
          >
            <div
              class="image-viewer-navigation-controls"
              data-image-viewer-controls
            >
              <Button
                variant="ghost"
                class="image-viewer-control"
                :disabled="activeIndex === 0"
                :aria-label="labels.previous"
                :title="labels.previous"
                @click="navigate(-1)"
              >
                <ArrowLeft aria-hidden="true" />
              </Button>
              <span class="image-viewer-count">{{ activeIndex + 1 }} / {{ items.length }}</span>
              <Button
                variant="ghost"
                class="image-viewer-control"
                :disabled="activeIndex === items.length - 1"
                :aria-label="labels.next"
                :title="labels.next"
                @click="navigate(1)"
              >
                <ArrowRight aria-hidden="true" />
              </Button>
            </div>
          </div>
          <div
            class="image-viewer-zoom-controls"
            data-image-viewer-controls
          >
            <Button
              variant="ghost"
              class="image-viewer-control"
              :disabled="!displayedSrc || view.zoom <= 1"
              :aria-label="labels.zoomOut"
              :title="labels.zoomOut"
              @click="zoomTo(view.zoom / 1.5)"
            >
              <ZoomOut aria-hidden="true" />
            </Button>
            <span class="image-viewer-zoom-value">{{ Math.round(view.zoom * 100) }}%</span>
            <Button
              variant="ghost"
              class="image-viewer-control"
              :disabled="!displayedSrc || view.zoom >= MAX_IMAGE_ZOOM"
              :aria-label="labels.zoomIn"
              :title="labels.zoomIn"
              @click="zoomTo(view.zoom * 1.5)"
            >
              <ZoomIn aria-hidden="true" />
            </Button>
            <Button
              variant="ghost"
              class="image-viewer-control"
              :disabled="!displayedSrc"
              :aria-label="labels.fit"
              :title="labels.fit"
              @click="fit"
            >
              <Maximize2 aria-hidden="true" />
            </Button>
          </div>
          <a
            class="button button-ghost image-viewer-control image-viewer-download"
            data-image-viewer-controls
            :href="downloadHref"
            :download="item?.name || ''"
            :aria-label="downloadLabel"
            :title="downloadLabel"
            target="_blank"
            rel="noopener noreferrer"
          >
            <Download aria-hidden="true" />
          </a>
        </footer>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>

<style scoped>
.image-viewer-overlay { position: fixed; inset: 0; z-index: 70; background: var(--overlay); }
.image-viewer { position: fixed; inset: 0; z-index: 71; display: flex; flex-direction: column; min-width: 0; height: 100%; height: 100dvh; overflow: hidden; background: transparent; color: var(--ink); overscroll-behavior: contain; -webkit-app-region: no-drag; }
.image-viewer-header { display: flex; align-items: center; gap: 16px; flex-shrink: 0; padding: max(12px, env(safe-area-inset-top)) max(16px, env(safe-area-inset-right)) 8px max(20px, env(safe-area-inset-left)); }
.image-viewer-title { flex: 0 1 auto; min-width: 0; margin-right: auto; padding: 8px 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; border-radius: var(--radius-control); background: var(--raised); font-size: 14px; font-weight: 550; line-height: 1.5; }
.image-viewer-control { width: 44px; height: 44px; min-height: 44px; flex-shrink: 0; padding: 11px; background: var(--raised); }
.image-viewer-control svg { width: 20px; height: 20px; }
.image-viewer-viewport { position: relative; flex: 1; min-height: 0; margin: 0 max(20px, env(safe-area-inset-right)) 0 max(20px, env(safe-area-inset-left)); overflow: hidden; touch-action: none; user-select: none; -webkit-user-select: none; cursor: zoom-in; }
.image-viewer-zoomed { cursor: grab; }
.image-viewer-zoomed:active { cursor: grabbing; }
.image-viewer-image { position: absolute; top: 50%; left: 50%; max-width: none; max-height: none; object-fit: contain; transform-origin: center; user-select: none; -webkit-user-select: none; -webkit-user-drag: none; pointer-events: none; }
.image-viewer-loading, .image-viewer-failure { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; }
.image-viewer-loading-indicator { display: inline-flex; padding: 14px; border-radius: var(--radius-control); background: var(--raised); }
.image-viewer-failure { flex-direction: column; gap: 12px; color: var(--muted); font-size: 13px; cursor: default; }
.image-viewer-failure > span, .image-viewer-feedback > span { padding: 8px 12px; border-radius: var(--radius-control); background: var(--raised); }
.image-viewer-failure .button { min-height: 44px; }
.image-viewer-feedback { display: flex; align-items: center; justify-content: center; flex-shrink: 0; gap: 8px; padding: 8px max(20px, env(safe-area-inset-right)) 0 max(20px, env(safe-area-inset-left)); color: var(--muted); font-size: 13px; line-height: 1.5; }
.image-viewer-feedback .button { min-height: 44px; background: var(--raised); }
.image-viewer-toolbar { display: flex; align-items: center; justify-content: center; flex-shrink: 0; flex-wrap: wrap; gap: 8px 20px; padding: 12px max(16px, env(safe-area-inset-right)) max(12px, env(safe-area-inset-bottom)) max(16px, env(safe-area-inset-left)); }
.image-viewer-navigation { display: flex; align-items: center; }
.image-viewer-navigation-controls, .image-viewer-zoom-controls { display: flex; align-items: center; gap: 2px; padding: 2px; border-radius: var(--radius-panel); background: var(--raised); }
.image-viewer-count, .image-viewer-zoom-value { color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; text-align: center; white-space: nowrap; }
.image-viewer-count { min-width: 44px; }
.image-viewer-zoom-value { width: 48px; }
:root[data-desktop] .image-viewer { padding-top: var(--desktop-titlebar-height); }
@media (max-width: 600px) {
  .image-viewer-header { padding-left: max(16px, env(safe-area-inset-left)); }
  .image-viewer-viewport { margin-left: max(12px, env(safe-area-inset-left)); margin-right: max(12px, env(safe-area-inset-right)); }
  .image-viewer-toolbar { gap: 8px; }
  .image-viewer-navigation { order: -1; width: 100%; justify-content: center; }
}
</style>
