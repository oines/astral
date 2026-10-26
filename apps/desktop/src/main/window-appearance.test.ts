import { describe, expect, it } from 'vitest'
import { nativeWindowAppearance, titleBarAppearance } from './window-appearance'

describe('native window appearance', () => {
  it('uses native overlaid controls and an Alt-accessible hidden menu on Windows and Linux', () => {
    for (const platform of ['win32', 'linux'] as const) {
      const options = nativeWindowAppearance(platform, false)
      expect(options).toMatchObject({ titleBarStyle: 'hidden', autoHideMenuBar: true, titleBarOverlay: { height: 44, color: '#f4f4f1', symbolColor: '#252524' } })
      expect(options.frame).toBeUndefined()
      expect(nativeWindowAppearance(platform, true)).toMatchObject({ backgroundColor: '#191918', titleBarOverlay: { color: '#191918', symbolColor: '#e9e9e5' } })
    }
  })
  it('leaves macOS window options to its existing native configuration', () => {
    expect(nativeWindowAppearance('darwin', false)).toEqual({})
    expect(nativeWindowAppearance('darwin', true)).toEqual({})
  })
  it('accepts only the theme choice at the native boundary', () => {
    for (const value of [undefined, null, 0, 1, 'dark', '#ffffff', {}, { color: '#ffffff' }]) expect(() => titleBarAppearance(value)).toThrow('Invalid window appearance')
    expect(titleBarAppearance(false).height).toBe(44)
    expect(titleBarAppearance(true).height).toBe(44)
  })
})
