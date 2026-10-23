import { describe, expect, it } from 'vitest'
import { ScrollFollow, type ScrollMetrics } from './scroll-follow'

const bottom: ScrollMetrics = { top: 1000, height: 1600, viewport: 600, width: 800 }

describe('reading while the assistant is streaming', () => {
  it('releases the bottom on upward intent before a scroll event, even inside the old bottom tolerance', () => {
    const follow = new ScrollFollow()
    follow.wrote(bottom)
    follow.input('up', bottom, 1000)
    expect(follow.following).toBe(false)
    follow.observe({ ...bottom, top: 980 })
    expect(follow.following).toBe(false)
    expect(follow.showJump).toBe(true)
    follow.observe({ ...bottom, top: 700 })
    expect(follow.showJump).toBe(true)
  })

  it('resumes only when the reader moves down to the bottom', () => {
    const follow = new ScrollFollow()
    follow.input('up', bottom, 1000)
    follow.observe({ ...bottom, top: 700 })
    follow.input('down', { ...bottom, top: 700 }, 1200)
    follow.observe({ ...bottom, top: 970 })
    expect(follow.following).toBe(true)
    expect(follow.cooldown(1300)).toBe(50)
    expect(follow.cooldown(1350)).toBe(0)
  })

  it('keeps the follow mode through text growth, sidebar reflow and viewport resizing', () => {
    const follow = new ScrollFollow()
    follow.wrote(bottom)
    follow.observe({ ...bottom, height: 1800 })
    expect(follow.following).toBe(true)
    follow.observe({ ...bottom, top: 800, height: 1400, width: 1000 })
    expect(follow.following).toBe(true)
    follow.observe({ ...bottom, top: 800, height: 1400, viewport: 500 })
    expect(follow.following).toBe(true)
  })

  it('does not resume following after history insertion or a programmatic reading-anchor correction', () => {
    const follow = new ScrollFollow()
    follow.input('up', bottom, 0)
    follow.observe({ ...bottom, top: 100 })
    follow.wrote({ ...bottom, top: 2100, height: 3600 })
    follow.observe({ ...bottom, top: 2100, height: 3600 })
    expect(follow.following).toBe(false)
    follow.observe({ ...bottom, top: 1000, height: 1600, width: 1000 })
    expect(follow.following).toBe(false)
  })
})

describe('jump button visibility', () => {
  it('requires a measured overflowing viewport and an actual distance from the bottom', () => {
    const follow = new ScrollFollow()
    follow.input('up', { ...bottom, viewport: 0 }, 0)
    expect(follow.showJump).toBe(false)
    follow.input('up', { ...bottom, height: 600, top: 0 }, 0)
    expect(follow.showJump).toBe(false)
    follow.input('up', bottom, 0)
    expect(follow.showJump).toBe(false)
    follow.input('up', { ...bottom, top: 999.5 }, 0)
    expect(follow.showJump).toBe(false)
    follow.input('up', { ...bottom, top: 995 }, 0)
    expect(follow.showJump).toBe(true)
  })
})
