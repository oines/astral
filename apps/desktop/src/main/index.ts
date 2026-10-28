import { app, BrowserWindow, dialog, ipcMain, Menu, nativeImage, nativeTheme, session, shell, Tray, type IpcMainInvokeEvent, type MenuItemConstructorOptions } from 'electron'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { access } from 'node:fs/promises'
import electronUpdater from 'electron-updater'
import { LocalService } from './service'
import { desktopRequestHeaders, externalURL, trustedSender } from './policy'
import { setLinuxLoginStart } from './login'
import { networkArguments } from './network-arguments'
import { DesktopUpdates, updateChannel, updateFeed } from './updates'
import { DesktopShutdown } from './shutdown'
import { DesktopUpdateControl, startDesktopUpdateBridge } from './update-control'
import { nativeWindowAppearance, titleBarAppearance } from './window-appearance'
import type { DesktopUpdateState } from '../shared/updates'

app.setName('Astral')
if (process.platform === 'win32') app.setAppUserModelId('com.oines.astral')
const single = app.requestSingleInstanceLock()
if (!single) app.quit()
const service = new LocalService()
const shutdown = new DesktopShutdown(() => service.stop())
let updates: DesktopUpdates | undefined
let updateControl: DesktopUpdateControl | undefined
let updateBridge: Awaited<ReturnType<typeof startDesktopUpdateBridge>> | undefined
let updateTimer: ReturnType<typeof setTimeout> | undefined
let updateInterval: ReturnType<typeof setInterval> | undefined
let window: BrowserWindow | undefined
let tray: Tray | undefined
let quitting = false
let keepRunning = true
let serverURL = ''
const directory = fileURLToPath(new URL('.', import.meta.url))
const resourcePath = (name: string) => app.isPackaged ? join(process.resourcesPath, name) : join(directory, '../../resources', name)
const applicationIcon = () => resourcePath(nativeTheme.shouldUseDarkColors ? 'icon-dark.png' : 'icon.png')

function traceLifecycle(phase: string) {
  if (process.env.CI) console.log(`[Astral lifecycle ${new Date().toISOString()}] ${phase}`)
}

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
async function setLoginStart(enabled: boolean) {
  if (!app.isPackaged) {
    if (enabled) throw new Error('Login startup is available in the installed Astral application.')
    return
  }
  if (process.platform === 'linux') {
    await setLinuxLoginStart(enabled, process.env.APPIMAGE || process.execPath)
  } else {
    app.setLoginItemSettings({ openAtLogin: enabled, ...(process.platform === 'win32' ? { path: process.execPath, args: [], enabled, name: 'Astral' } : {}) })
  }
}
function assertSender(event: IpcMainInvokeEvent) {
  if (event.sender !== window?.webContents || !trustedSender(event.senderFrame?.url, event.senderFrame?.parent, serverURL)) throw new Error('Native access is unavailable to this page.')
}
async function prepareShutdown() {
  quitting = true
  traceLifecycle('Server stop requested')
  try {
    await shutdown.prepare()
    await updateBridge?.close()
    tray?.destroy()
    clearTimeout(updateTimer)
    clearInterval(updateInterval)
    traceLifecycle('Server and its pipes exited')
  } catch (error) {
    quitting = false
    throw error
  }
}
async function configureUpdates() {
  let unavailable: DesktopUpdateState['unavailable']
  if (!app.isPackaged) unavailable = 'development'
  else if (process.platform === 'linux' && !process.env.APPIMAGE) unavailable = 'package'
  else if (process.platform === 'win32') {
    // An extracted ZIP has no NSIS installation to replace. Offer the installer instead.
    try { await access(join(dirname(process.execPath), 'Uninstall Astral.exe')) }
    catch { unavailable = 'windows-zip' }
  }
  else if (process.platform === 'darwin') {
    try {
      const bundle = join(app.getAppPath(), '../../..')
      const run = promisify(execFile)
      await run('/usr/bin/codesign', ['--verify', '--strict', bundle])
      const { stderr } = await run('/usr/bin/codesign', ['--display', '--verbose=4', bundle])
      if (!stderr.includes('TeamIdentifier=6N6W6Z6739') || !stderr.includes('Authority=Developer ID Application: ')) unavailable = 'signature'
    } catch { unavailable = 'signature' }
  }
  const { autoUpdater } = electronUpdater
  // Fixed public feed. No Renderer URL, credentials, or environment token is consulted.
  autoUpdater.setFeedURL({ provider: 'generic', url: updateFeed, channel: updateChannel(process.arch), useMultipleRangeRequest: false })
  updates = new DesktopUpdates(autoUpdater, app.getVersion(), async () => {
    await updateControl!.beforeInstall()
    await prepareShutdown()
    // AppImageUpdater can launch the replacement before quitting this process.
    // Release only after confirmed Server exit so the new app can take the lock.
    app.releaseSingleInstanceLock()
  }, unavailable)
  updateControl = new DesktopUpdateControl(updates, join(app.getPath('userData'), 'update-operation.json'))
  await updateControl.initialize()
  updateBridge = await startDesktopUpdateBridge(updateControl)
  updateControl.onState = state => {
    if (window && !window.isDestroyed()) window.webContents.send('astral:update-state', state)
  }
  updateControl.onInstallError = error => {
    // The service is already stopped; restart the current installation on installer failure.
    dialog.showErrorBox('Astral update failed', `${error.message}\n\nAstral will reopen the current version.`)
    app.relaunch()
    app.quit()
  }
  ipcMain.handle('astral:update-state', event => { assertSender(event); return updateControl!.state() })
  ipcMain.handle('astral:update-check', event => { assertSender(event); return updateControl!.check() })
  ipcMain.handle('astral:update', event => { assertSender(event); return updateControl!.update() })
  if (!unavailable) {
    // Metadata checks never download. Keep a failed user update visible until they act.
    updateTimer = setTimeout(() => { void updateControl?.check(true) }, 30_000)
    updateInterval = setInterval(() => { void updateControl?.check(true) }, 6 * 60 * 60 * 1000)
    updateTimer.unref()
    updateInterval.unref()
  }
}
function createWindow() {
  window = new BrowserWindow({
    title: 'Astral', width: 1240, height: 860, minWidth: 700, minHeight: 540,
    icon: applicationIcon(),
    show: false, backgroundColor: '#f4f4f1',
    ...(process.platform === 'darwin' ? { titleBarStyle: 'hidden' as const, trafficLightPosition: { x: 17, y: 14 } } : {}),
    ...nativeWindowAppearance(process.platform, nativeTheme.shouldUseDarkColors),
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
    traceLifecycle(`window close: quitting=${quitting}, keepRunning=${keepRunning}`)
    if (quitting) return
    if (keepRunning) {
      event.preventDefault()
      window?.hide()
    } else app.quit()
  })
  window.once('closed', () => traceLifecycle('window closed'))
  // Windows does not emit app.before-quit for logoff or system shutdown.
  // Request cleanup promptly without blocking the user's session ending.
  if (process.platform === 'win32') window.on('query-session-end', () => { void service.stop() })
  window.once('ready-to-show', () => window?.show())
  void window.loadURL(serverURL).catch(error => {
    traceLifecycle(`window loadURL rejected: quitting=${quitting}, error=${String(error)}`)
    if (!quitting) dialog.showErrorBox('Astral could not open', String(error))
  })
}
async function start() {
  updateApplicationIcon()
  nativeTheme.on('updated', updateApplicationIcon)
  // Keep incompatible legacy data intact for the current Host/Core format pair.
  const root = process.env.ASTRAL_DESKTOP_DATA_DIR || join(app.getPath('appData'), 'astral', 'schema-3-core-11')
  const bins = app.isPackaged ? join(process.resourcesPath, 'bin') : fileURLToPath(new URL('../../../../dist/bin/', import.meta.url))
  const architecture = process.arch === 'x64' ? 'amd64' : process.arch
  const system = process.platform === 'win32' ? 'windows' : process.platform
  const binary = join(bins, `astral-${system}-${architecture}${system === 'windows' ? '.exe' : ''}`)
  await configureUpdates()
  const token = await service.start(binary, root, bins, networkArguments(process.argv.slice(1)), updateBridge)
  serverURL = service.url
  // Only this Electron session may attach the process-local Desktop identity,
  // and only to the exact Server origin. Renderer JS never receives the secret.
  session.defaultSession.webRequest.onBeforeSendHeaders((details, callback) => {
    callback({ requestHeaders: desktopRequestHeaders(details, window?.webContents.id, serverURL, token) })
  })
  const initial = await fetch(`${serverURL}/api/v1/bootstrap`, { headers: { Authorization: `Desktop ${token}` } })
  if (!initial.ok) throw new Error(`Local service initialization failed (HTTP ${initial.status}).`)
  const data = await initial.json() as { preferences: { keep_running: boolean; login_start: boolean } }
  keepRunning = data.preferences.keep_running
  await setLoginStart(data.preferences.login_start)
  session.defaultSession.setPermissionRequestHandler((_contents, permission, callback) => callback(permission === 'clipboard-sanitized-write'))
  session.defaultSession.setPermissionCheckHandler((_contents, permission, origin) => permission === 'clipboard-sanitized-write' && origin === serverURL)
  ipcMain.handle('astral:preferences', async (event, value: unknown) => {
    assertSender(event)
    if (!value || typeof value !== 'object' || !('keepRunning' in value) || typeof value.keepRunning !== 'boolean' || !('loginStart' in value) || typeof value.loginStart !== 'boolean') throw new Error('Invalid desktop preferences.')
    await setLoginStart(value.loginStart)
    keepRunning = value.keepRunning
  })
  ipcMain.handle('astral:open-external', async (event, url: unknown) => {
    assertSender(event)
    await shell.openExternal(externalURL(url))
  })
  ipcMain.handle('astral:appearance', (event, dark: unknown) => {
    assertSender(event)
    const appearance = titleBarAppearance(dark)
    if (process.platform === 'win32' || process.platform === 'linux') window?.setTitleBarOverlay(appearance)
  })
  const settings: MenuItemConstructorOptions = { label: 'Settings…', accelerator: 'CmdOrCtrl+,', click: () => { show(); window?.webContents.send('astral:open-settings') } }
  const applicationMenu: MenuItemConstructorOptions = process.platform === 'darwin'
    ? { label: 'Astral', submenu: [{ role: 'about' }, settings, { type: 'separator' }, { role: 'hide' }, { role: 'hideOthers' }, { role: 'unhide' }, { type: 'separator' }, { role: 'quit' }] }
    : { label: 'File', submenu: [settings, { type: 'separator' }, { role: 'quit' }] }
  Menu.setApplicationMenu(Menu.buildFromTemplate([
    applicationMenu,
    { role: 'editMenu' }, { role: 'viewMenu' }, { role: 'windowMenu' },
    ...(process.platform === 'darwin' ? [] : [{ role: 'help' as const, submenu: [{ role: 'about' as const }] }]),
  ]))
  // Electron loads the adjacent @2x representation for Retina displays.
  const icon = nativeImage.createFromPath(resourcePath(process.platform === 'darwin' ? 'trayTemplate.png' : process.platform === 'win32' ? 'icon.ico' : 'icon.png'))
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
app.on('window-all-closed', () => { traceLifecycle(`window-all-closed: quitting=${quitting}, keepRunning=${keepRunning}`); if (!keepRunning) app.quit() })
for (const signal of ['SIGTERM', 'SIGINT'] as const) process.on(signal, () => {
  traceLifecycle(`${signal} received; requesting Quit`)
  app.quit()
  traceLifecycle(`${signal} Quit request returned`)
})
app.on('before-quit', event => {
  traceLifecycle(`before-quit: quitting=${quitting}, shutdownComplete=${shutdown.complete}`)
  if (shutdown.complete) return
  event.preventDefault()
  if (quitting) return
  void prepareShutdown().then(() => {
    // Leave Electron's native before-quit dispatch before requesting Quit again.
    // A Promise callback can reenter it and have its quitting state overwritten.
    setTimeout(() => {
      traceLifecycle('requesting Quit after Server stop')
      app.quit()
      traceLifecycle('second Quit request returned')
    }, 0)
  }).catch(error => {
    show()
    dialog.showErrorBox('Astral could not quit safely', error instanceof Error ? error.message : String(error))
  })
})
app.on('will-quit', () => traceLifecycle('will-quit'))
app.on('quit', (_event, exitCode) => traceLifecycle(`quit: exitCode=${exitCode}`))
if (single) void app.whenReady().then(start).catch(async error => {
  dialog.showErrorBox('Astral could not start', `${error instanceof Error ? error.message : String(error)}\n\nIf another Astral server already uses this data directory, stop it before opening the desktop application.`)
  await service.stop()
  await updateBridge?.close()
  app.quit()
})
