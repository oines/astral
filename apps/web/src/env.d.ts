/// <reference types="vite/client" />
interface Window {
  astralDesktop?: {
    platform: string
    preferences: (value: { keepRunning: boolean; loginStart: boolean }) => Promise<void>
    openExternal: (url: string) => Promise<void>
  }
}
