import { describe, expect, it } from 'vitest'
import { surfaceLayout } from './surface-layout'

describe('the conversation and contextual workspace split', () => {
  it('keeps a landscape window split even below the old 1200px breakpoint', () => {
    // A 1024px window with 256px navigation and an 8px outer inset.
    expect(surfaceLayout(760, 620)).toEqual({ overlay: false, maximumWidth: 392, width: 392 })
  })

  it('uses actual remaining space, including changes to the navigation sidebar', () => {
    expect(surfaceLayout(636, 620).overlay).toBe(true)
    expect(surfaceLayout(884, 620)).toEqual({ overlay: false, maximumWidth: 516, width: 516 })
  })

  it('only overlays when the two minimum widths and their divider cannot fit', () => {
    expect(surfaceLayout(688, 620)).toEqual({ overlay: false, maximumWidth: 320, width: 320 })
    expect(surfaceLayout(687, 620)).toEqual({ overlay: true, maximumWidth: 687, width: 687 })
    expect(surfaceLayout(390, 620).width).toBe(390)
  })

  it('keeps the preferred divider position within usable limits', () => {
    expect(surfaceLayout(1400, 700).width).toBe(700)
    expect(surfaceLayout(1400, 2000).width).toBe(1032)
    expect(surfaceLayout(1400, 100).width).toBe(320)
  })
})
