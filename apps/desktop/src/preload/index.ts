import { contextBridge, ipcRenderer } from 'electron'

contextBridge.exposeInMainWorld('astralDesktop', {
  platform: process.platform,
  preferences: (value: { keepRunning: boolean; loginStart: boolean }) => ipcRenderer.invoke('astral:preferences', value),
  openExternal: (url: string) => ipcRenderer.invoke('astral:open-external', url),
})
