export interface ImageSize { width: number; height: number }
export interface ImagePoint { x: number; y: number }
export interface ImageView extends ImagePoint { zoom: number }

export const MIN_IMAGE_ZOOM = 1
export const MAX_IMAGE_ZOOM = 8

export function fitImage(image: ImageSize, viewport: ImageSize): ImageSize {
  if (image.width <= 0 || image.height <= 0 || viewport.width <= 0 || viewport.height <= 0)
    return { width: 0, height: 0 }
  const ratio = Math.min(1, viewport.width / image.width, viewport.height / image.height)
  return { width: image.width * ratio, height: image.height * ratio }
}

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value))
}

export function clampImageView(view: ImageView, fitted: ImageSize, viewport: ImageSize): ImageView {
  const zoom = clamp(view.zoom, MIN_IMAGE_ZOOM, MAX_IMAGE_ZOOM)
  const limitX = Math.max(0, (fitted.width * zoom - viewport.width) / 2)
  const limitY = Math.max(0, (fitted.height * zoom - viewport.height) / 2)
  return { zoom, x: clamp(view.x, -limitX, limitX), y: clamp(view.y, -limitY, limitY) }
}

// Anchor coordinates are relative to the viewport's center, as are pan offsets.
export function zoomImageAt(view: ImageView, zoom: number, anchor: ImagePoint, fitted: ImageSize, viewport: ImageSize): ImageView {
  const nextZoom = clamp(zoom, MIN_IMAGE_ZOOM, MAX_IMAGE_ZOOM)
  const ratio = nextZoom / view.zoom
  return clampImageView({ zoom: nextZoom, x: anchor.x - (anchor.x - view.x) * ratio, y: anchor.y - (anchor.y - view.y) * ratio }, fitted, viewport)
}

function midpoint(first: ImagePoint, second: ImagePoint): ImagePoint {
  return { x: (first.x + second.x) / 2, y: (first.y + second.y) / 2 }
}

export function pinchImage(view: ImageView, previous: readonly [ImagePoint, ImagePoint], next: readonly [ImagePoint, ImagePoint], fitted: ImageSize, viewport: ImageSize): ImageView {
  const before = midpoint(...previous)
  const after = midpoint(...next)
  const oldDistance = Math.hypot(previous[1].x - previous[0].x, previous[1].y - previous[0].y)
  const newDistance = Math.hypot(next[1].x - next[0].x, next[1].y - next[0].y)
  const zoom = clamp(view.zoom * (oldDistance > 0 ? newDistance / oldDistance : 1), MIN_IMAGE_ZOOM, MAX_IMAGE_ZOOM)
  const ratio = zoom / view.zoom
  // Preserve the point beneath the original centroid while following its motion.
  return clampImageView({ zoom, x: after.x - (before.x - view.x) * ratio, y: after.y - (before.y - view.y) * ratio }, fitted, viewport)
}

export function refitImageView(view: ImageView, previous: ImageSize, next: ImageSize, viewport: ImageSize): ImageView {
  if (view.zoom === MIN_IMAGE_ZOOM || !previous.width || !previous.height)
    return { zoom: MIN_IMAGE_ZOOM, x: 0, y: 0 }
  return clampImageView({ zoom: view.zoom, x: view.x * next.width / previous.width, y: view.y * next.height / previous.height }, next, viewport)
}
