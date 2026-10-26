import { contextBridge, ipcRenderer } from 'electron'
import type { DesktopUpdateState } from '../shared/updates'

contextBridge.exposeInMainWorld('astralDesktop', {
  platform: process.platform,
  preferences: (value: { keepRunning: boolean; loginStart: boolean }) => ipcRenderer.invoke('astral:preferences', value),
  openExternal: (url: string) => ipcRenderer.invoke('astral:open-external', url),
  appearance: (dark: boolean) => ipcRenderer.invoke('astral:appearance', dark),
  updates: {
    state: () => ipcRenderer.invoke('astral:update-state'),
    check: () => ipcRenderer.invoke('astral:update-check'),
    install: () => ipcRenderer.invoke('astral:update-install'),
    onState: (callback: (state: DesktopUpdateState) => void) => {
      const listener = (_event: Electron.IpcRendererEvent, state: DesktopUpdateState) => callback(state)
      ipcRenderer.on('astral:update-state', listener)
      return () => ipcRenderer.removeListener('astral:update-state', listener)
    },
  },
  onSettings: (callback: () => void) => {
    const listener = () => callback()
    ipcRenderer.on('astral:open-settings', listener)
    return () => ipcRenderer.removeListener('astral:open-settings', listener)
  },
})
