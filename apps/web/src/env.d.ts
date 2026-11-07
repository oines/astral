/// <reference types="vite/client" />
interface Window {
  astralDesktop?: {
    onSettings?: (callback: () => void) => () => void
    platform: string
    preferences: (value: { keepRunning: boolean; loginStart: boolean }) => Promise<void>
    openExternal: (url: string) => Promise<void>
    appearance: (dark: boolean) => Promise<void>
    files: import('../../desktop/src/shared/files').DesktopFileBridge
    updates: import('../../desktop/src/shared/updates').DesktopUpdateBridge
  }
}
