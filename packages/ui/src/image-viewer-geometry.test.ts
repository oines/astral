import { describe, expect, it } from 'vitest'
import { clampImageView, fitImage, pinchImage, refitImageView, zoomImageAt } from './image-viewer-geometry'

const viewport = { width: 800, height: 600 }
const fitted = { width: 800, height: 600 }

describe('image viewer geometry', () => {
  it('fits the complete image without distorting or upscaling it', () => {
    expect(fitImage({ width: 1600, height: 800 }, viewport)).toEqual({ width: 800, height: 400 })
    expect(fitImage({ width: 400, height: 1200 }, viewport)).toEqual({ width: 200, height: 600 })
    expect(fitImage({ width: 200, height: 100 }, viewport)).toEqual({ width: 200, height: 100 })
    expect(fitImage({ width: 0, height: 0 }, viewport)).toEqual({ width: 0, height: 0 })
  })

  it('keeps the image coordinate under a zoom anchor stationary', () => {
    const view = { zoom: 2, x: 30, y: -20 }
    const anchor = { x: 100, y: 80 }
    const next = zoomImageAt(view, 3, anchor, fitted, viewport)
    expect((anchor.x - next.x) / next.zoom).toBeCloseTo((anchor.x - view.x) / view.zoom)
    expect((anchor.y - next.y) / next.zoom).toBeCloseTo((anchor.y - view.y) / view.zoom)
    expect(next.zoom).toBe(3)
  })

  it('clamps offsets to prevent empty space beyond a zoomed image', () => {
    expect(clampImageView({ zoom: 2, x: 900, y: -900 }, fitted, viewport)).toEqual({ zoom: 2, x: 400, y: -300 })
    expect(clampImageView({ zoom: 2, x: 90, y: 50 }, { width: 200, height: 100 }, viewport)).toEqual({ zoom: 2, x: 0, y: 0 })
    expect(zoomImageAt({ zoom: 2, x: 300, y: 200 }, 1, { x: 200, y: 200 }, fitted, viewport)).toEqual({ zoom: 1, x: 0, y: 0 })
  })

  it('follows a moving pinch centroid as well as the change in finger distance', () => {
    const next = pinchImage(
      { zoom: 2, x: 0, y: 0 },
      [{ x: -50, y: 0 }, { x: 50, y: 0 }],
      [{ x: -80, y: 30 }, { x: 120, y: 30 }],
      fitted, viewport,
    )
    expect(next).toEqual({ zoom: 4, x: 20, y: 30 })
  })

  it('lets two fingers pan at the zoom limit without exceeding it', () => {
    const next = pinchImage(
      { zoom: 8, x: 0, y: 0 },
      [{ x: -50, y: 0 }, { x: 50, y: 0 }],
      [{ x: -90, y: 10 }, { x: 110, y: 10 }],
      fitted, viewport,
    )
    expect(next).toEqual({ zoom: 8, x: 10, y: 10 })
    expect(pinchImage({ zoom: 1, x: 0, y: 0 }, [{ x: 0, y: 0 }, { x: 0, y: 0 }], [{ x: 0, y: 0 }, { x: 0, y: 0 }], fitted, viewport)).toEqual({ zoom: 1, x: 0, y: 0 })
  })

  it('refits after resize while retaining the centered image point when zoomed', () => {
    const smallerViewport = { width: 400, height: 300 }
    const smallerFit = fitImage({ width: 1600, height: 1200 }, smallerViewport)
    expect(refitImageView({ zoom: 2, x: 200, y: -100 }, fitted, smallerFit, smallerViewport)).toEqual({ zoom: 2, x: 100, y: -50 })
    expect(refitImageView({ zoom: 1, x: 0, y: 0 }, fitted, smallerFit, smallerViewport)).toEqual({ zoom: 1, x: 0, y: 0 })
    expect(refitImageView({ zoom: 2, x: 400, y: 300 }, fitted, smallerFit, viewport)).toEqual({ zoom: 2, x: 0, y: 0 })
  })
})
