import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { manualCopyText, useClipboard } from './useClipboard'
const browser = vi.hoisted(() => ({ copyToClipboard: vi.fn() }))
vi.mock('../lib/browser', () => browser)
afterEach(() => { manualCopyText.value = undefined; vi.clearAllMocks() })

describe('copy feedback', () => {
  it('offers the complete text for manual copying and never claims a blocked copy succeeded', async () => {
    browser.copyToClipboard.mockResolvedValue(false)
    const scope = effectScope()
    const clipboard = scope.run(useClipboard)!
    expect(await clipboard.copy('command with arguments\nand another line')).toBe(false)
    expect(clipboard.copied.value).toBe(false)
    expect(manualCopyText.value).toBe('command with arguments\nand another line')
    scope.stop()
  })

  it('shows copied feedback after a successful copy without opening the manual dialog', async () => {
    browser.copyToClipboard.mockResolvedValue(true)
    const scope = effectScope()
    const clipboard = scope.run(useClipboard)!
    expect(await clipboard.copy('copied text')).toBe(true)
    expect(clipboard.copied.value).toBe(true)
    expect(manualCopyText.value).toBeUndefined()
    scope.stop()
  })
})
