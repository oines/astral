import { describe, expect, it } from 'vitest'
import { createPreviewScrollState } from './preview-scroll'

describe('cached file preview reading positions', () => {
  it('restores reading position after another preview and ignores detached zero events', async () => {
    const element = { scrollTop: 0, isConnected: true }
    const scroll = createPreviewScrollState(() => element)
    await scroll.activate()
    element.scrollTop = 720
    scroll.record()
    scroll.deactivate()
    element.isConnected = false
    element.scrollTop = 0
    scroll.record()
    element.isConnected = true
    await scroll.activate()
    expect(element.scrollTop).toBe(720)
  })

  it('does not overwrite a saved position while active but still detached from the document', async () => {
    const element = { scrollTop: 0, isConnected: true }
    const scroll = createPreviewScrollState(() => element)
    await scroll.activate()
    element.scrollTop = 420
    scroll.record()
    element.isConnected = false
    element.scrollTop = 0
    scroll.record()
    element.isConnected = true
    await scroll.activate()
    expect(element.scrollTop).toBe(420)
  })

  it('does not apply an activation that finishes after the preview was hidden again', async () => {
    let finish!: () => void
    const nextRender = new Promise<void>(resolve => { finish = resolve })
    const element = { scrollTop: 90, isConnected: true }
    const scroll = createPreviewScrollState(() => element, () => nextRender)
    const activating = scroll.activate()
    scroll.deactivate()
    finish()
    await activating
    expect(element.scrollTop).toBe(90)
  })

  it('resets the position for a different file without sharing positions between previews', async () => {
    const element = { scrollTop: 0, isConnected: true }
    const scroll = createPreviewScrollState(() => element)
    await scroll.activate()
    element.scrollTop = 200
    scroll.record()
    scroll.reset()
    await scroll.activate()
    expect(element.scrollTop).toBe(0)
  })
})

it('keeps the saved reading position while a refresh temporarily clears content', async () => {
  const element = { scrollTop: 0, isConnected: true }
  const scroll = createPreviewScrollState(() => element)
  await scroll.activate()
  element.scrollTop = 720
  scroll.record()
  scroll.suspend()
  element.scrollTop = 0
  scroll.record()
  await scroll.resume()
  expect(element.scrollTop).toBe(720)
})

it('does not replace a reset for a different file with the previous file DOM position', async () => {
  const element = { scrollTop: 0, isConnected: true }
  const scroll = createPreviewScrollState(() => element)
  await scroll.activate()
  element.scrollTop = 720
  scroll.record()
  scroll.reset()
  scroll.suspend()
  element.scrollTop = 0
  await scroll.resume()
  expect(element.scrollTop).toBe(0)
})
