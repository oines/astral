import { afterEach, describe, expect, it, vi } from 'vitest'
import { webcrypto } from 'node:crypto'
import { copyToClipboard, randomID } from './browser'

afterEach(() => vi.unstubAllGlobals())

describe('browser capabilities on an HTTP origin', () => {
  it('creates UUID request identities with secure randomness when randomUUID is unavailable', () => {
    const getRandomValues = vi.fn((bytes: Uint8Array<ArrayBuffer>) => webcrypto.getRandomValues(bytes))
    vi.stubGlobal('isSecureContext', false)
    vi.stubGlobal('crypto', { getRandomValues })
    const values = Array.from({ length: 20 }, randomID)
    expect(getRandomValues).toHaveBeenCalledTimes(20)
    expect(new Set(values).size).toBe(20)
    for (const value of values) expect(value).toMatch(/^[a-f\d]{8}-[a-f\d]{4}-4[a-f\d]{3}-[89ab][a-f\d]{3}-[a-f\d]{12}$/)
  })

  it('reports success only after the modern clipboard write succeeds', async () => {
    const writeText = vi.fn(async () => undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    expect(await copyToClipboard('the command on screen')).toBe(true)
    expect(writeText).toHaveBeenCalledWith('the command on screen')
  })

  function fallback(result: boolean) {
    class Element { focus = vi.fn() }
    vi.stubGlobal('HTMLElement', Element)
    const previousFocus = new Element()
    const input = { value: '', readOnly: false, style: {}, focus: vi.fn(), select: vi.fn(), remove: vi.fn() }
    const execCommand = vi.fn(() => result)
    vi.stubGlobal('document', { activeElement: previousFocus, getSelection: () => null, createElement: () => input, body: { append: vi.fn() }, execCommand })
    return { input, execCommand, previousFocus }
  }

  it('copies through a selected textarea without the secure-context clipboard API', async () => {
    vi.stubGlobal('isSecureContext', false)
    vi.stubGlobal('navigator', {})
    const { input, execCommand, previousFocus } = fallback(true)
    expect(await copyToClipboard('http://computer:4545')).toBe(true)
    expect(input.value).toBe('http://computer:4545')
    expect(input.select).toHaveBeenCalledOnce()
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(input.remove).toHaveBeenCalledOnce()
    expect(previousFocus.focus).toHaveBeenCalledOnce()
  })

  it('tries DOM copying after clipboard permission is denied and preserves the real failure', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn(async () => { throw new Error('permission denied') }) } })
    const { input, execCommand } = fallback(false)
    expect(await copyToClipboard('copy me manually')).toBe(false)
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(input.remove).toHaveBeenCalledOnce()
  })
})
