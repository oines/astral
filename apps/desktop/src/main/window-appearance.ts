import type { BrowserWindowConstructorOptions, TitleBarOverlay } from 'electron'

/** Renderer may choose only the app theme, never native colors or geometry. */
export function titleBarAppearance(dark: unknown): TitleBarOverlay {
  if (typeof dark !== 'boolean') throw new Error('Invalid window appearance.')
  return { height: 44, color: dark ? '#191918' : '#f4f4f1', symbolColor: dark ? '#e9e9e5' : '#252524' }
}

export function nativeWindowAppearance(platform: NodeJS.Platform, dark: boolean): BrowserWindowConstructorOptions {
  if (platform !== 'win32' && platform !== 'linux') return {}
  const overlay = titleBarAppearance(dark)
  return { titleBarStyle: 'hidden', titleBarOverlay: overlay, autoHideMenuBar: true, backgroundColor: overlay.color }
}
