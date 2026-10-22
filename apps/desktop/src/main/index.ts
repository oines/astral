import { app, BrowserWindow, dialog, ipcMain, Menu, nativeImage, nativeTheme, session, shell, Tray, type IpcMainInvokeEvent } from 'electron'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { LocalService } from './service'
import { externalURL, trustedSender } from './policy'

app.setName('Astral')
const single = app.requestSingleInstanceLock()
if (!single) app.quit()
const service = new LocalService()
let window: BrowserWindow | undefined
let tray: Tray | undefined
let quitting = false
let shutdownComplete = false
let keepRunning = true
let serverURL = ''
const directory = fileURLToPath(new URL('.', import.meta.url))
const resourcePath = (name: string) => app.isPackaged ? join(process.resourcesPath, name) : join(directory, '../../resources', name)
const applicationIcon = () => resourcePath(nativeTheme.shouldUseDarkColors ? 'icon-dark.png' : 'icon.png')

function updateApplicationIcon() {
  app.dock?.setIcon(applicationIcon())
  if (process.platform !== 'darwin' && window && !window.isDestroyed()) window.setIcon(applicationIcon())
}

function show() {
  if (window?.isDestroyed()) window = undefined
  if (!window) createWindow()
  window?.show()
  window?.focus()
  app.dock?.show()
}
function assertSender(event: IpcMainInvokeEvent) {
  if (event.sender !== window?.webContents || !trustedSender(event.senderFrame?.url, event.senderFrame?.parent, serverURL)) throw new Error('Native access is unavailable to this page.')
}
function createWindow() {
  window = new BrowserWindow({
    title: 'Astral', width: 1240, height: 860, minWidth: 700, minHeight: 540,
    icon: applicationIcon(),
    show: false, backgroundColor: '#f4f4f1',
    ...(process.platform === 'darwin' ? { titleBarStyle: 'hidden' as const, trafficLightPosition: { x: 17, y: 16 } } : {}),
    webPreferences: {
      preload: join(directory, '../preload/index.cjs'),
      nodeIntegration: false, contextIsolation: true, sandbox: true,
      webSecurity: true, spellcheck: true,
    },
  })
  window.webContents.setWindowOpenHandler(({ url }) => {
    if (new URL(url).origin === new URL(serverURL).origin) window?.webContents.downloadURL(url)
    else { try { void shell.openExternal(externalURL(url)) } catch { /* Unsupported link stays closed. */ } }
    return { action: 'deny' }
  })
  window.webContents.on('will-navigate', (event, url) => {
    if (new URL(url).origin !== new URL(serverURL).origin) {
      event.preventDefault()
      try { void shell.openExternal(externalURL(url)) } catch { /* Unsupported link stays closed. */ }
    }
  })
  window.webContents.on('will-attach-webview', event => event.preventDefault())
  window.on('close', event => {
    if (quitting) return
    if (keepRunning) {
      event.preventDefault()
      window?.hide()
    } else app.quit()
  })
  window.once('ready-to-show', () => window?.show())
  void window.loadURL(serverURL).catch(error => dialog.showErrorBox('Astral could not open', String(error)))
}
async function start() {
  updateApplicationIcon()
  nativeTheme.on('updated', updateApplicationIcon)
  // Keep incompatible legacy data intact when starting the current host format.
  const root = process.env.ASTRAL_DESKTOP_DATA_DIR || join(app.getPath('appData'), 'astral', 'schema-3')
  const bins = app.isPackaged ? join(process.resourcesPath, 'bin') : fileURLToPath(new URL('../../../../dist/bin/', import.meta.url))
  const architecture = process.arch === 'x64' ? 'amd64' : process.arch
  const binary = join(bins, `astral-${process.platform}-${architecture}`)
  const token = await service.start(binary, root, bins)
  serverURL = service.url
  // Renderer code never receives the Owner token or a general native file API.
  await session.defaultSession.cookies.set({ url: serverURL, name: 'astral_owner', value: token, path: '/', httpOnly: true, sameSite: 'strict', secure: serverURL.startsWith('https:') })
  const initial = await fetch(`${serverURL}/api/v1/bootstrap`, { headers: { Authorization: `Bearer ${token}` } })
  if (!initial.ok) throw new Error(`Local service initialization failed (HTTP ${initial.status}).`)
  const data = await initial.json() as { preferences: { keep_running: boolean; login_start: boolean } }
  keepRunning = data.preferences.keep_running
  if (app.isPackaged) app.setLoginItemSettings({ openAtLogin: data.preferences.login_start })
  session.defaultSession.setPermissionRequestHandler((_contents, permission, callback) => callback(permission === 'clipboard-sanitized-write'))
  session.defaultSession.setPermissionCheckHandler((_contents, permission, origin) => permission === 'clipboard-sanitized-write' && origin === serverURL)
  ipcMain.handle('astral:preferences', (event, value: unknown) => {
    assertSender(event)
    if (!value || typeof value !== 'object' || !('keepRunning' in value) || typeof value.keepRunning !== 'boolean' || !('loginStart' in value) || typeof value.loginStart !== 'boolean') throw new Error('Invalid desktop preferences.')
    keepRunning = value.keepRunning
    if (app.isPackaged) app.setLoginItemSettings({ openAtLogin: value.loginStart })
    else if (value.loginStart) throw new Error('Login startup is available in the installed Astral application.')
  })
  ipcMain.handle('astral:open-external', async (event, url: unknown) => {
    assertSender(event)
    await shell.openExternal(externalURL(url))
  })
  Menu.setApplicationMenu(Menu.buildFromTemplate([
    { label: 'Astral', submenu: [{ role: 'about' }, { label: 'Settings…', accelerator: 'CmdOrCtrl+,', click: () => { show(); window?.webContents.send('astral:open-settings') } }, { type: 'separator' }, { role: 'hide' }, { role: 'hideOthers' }, { role: 'unhide' }, { type: 'separator' }, { role: 'quit' }] },
    { role: 'editMenu' }, { role: 'viewMenu' }, { role: 'windowMenu' },
  ]))
  // Electron loads the adjacent @2x representation for Retina displays.
  const icon = nativeImage.createFromPath(resourcePath('trayTemplate.png'))
  icon.setTemplateImage(process.platform === 'darwin')
  tray = new Tray(icon)
  tray.setToolTip('Astral')
  tray.setContextMenu(Menu.buildFromTemplate([{ label: 'Open Astral', click: show }, { type: 'separator' }, { label: 'Quit Astral', click: () => app.quit() }]))
  tray.on('click', show)
  service.onUnexpectedExit = async detail => {
    const choice = await dialog.showMessageBox({ type: 'error', title: 'Astral service stopped', message: 'The local service stopped unexpectedly.', detail, buttons: ['Restart Astral', 'Quit'], defaultId: 0, cancelId: 1 })
    if (choice.response === 0) app.relaunch()
    app.quit()
  }
  createWindow()
}
app.on('second-instance', () => { if (serverURL) show() })
app.on('activate', () => { if (serverURL && !quitting) show() })
app.on('window-all-closed', () => { if (!keepRunning) app.quit() })
process.on('SIGTERM', () => app.quit())
process.on('SIGINT', () => app.quit())
app.on('before-quit', event => {
  if (shutdownComplete) return
  event.preventDefault()
  if (quitting) return
  quitting = true
  tray?.destroy()
  void service.stop().finally(() => { shutdownComplete = true; app.quit() })
})
if (single) void app.whenReady().then(start).catch(async error => {
  dialog.showErrorBox('Astral could not start', `${error instanceof Error ? error.message : String(error)}\n\nIf another Astral server already uses this data directory, stop it before opening the desktop application.`)
  await service.stop()
  app.quit()
})
