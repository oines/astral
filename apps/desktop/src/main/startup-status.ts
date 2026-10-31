import { BrowserWindow } from 'electron'

// Data preparation runs before the authenticated renderer is available. This
// local status window has no preload, IPC, credentials, links or network access.
export function startupStatus(icon: string, chinese: boolean, cancel: () => void) {
  let timer: ReturnType<typeof setTimeout> | undefined
  let window: BrowserWindow | undefined
  const clear = () => {
    clearTimeout(timer)
    timer = undefined
    const previous = window
    window = undefined
    previous?.destroy()
  }
  return (active: boolean) => {
    if (!active) { clear(); return }
    if (timer || window) return
    timer = setTimeout(() => {
      timer = undefined
      const current = new BrowserWindow({ title: 'Astral', width: 460, height: 220, resizable: false, minimizable: false, maximizable: false, show: false, icon, backgroundColor: '#f4f4f1', webPreferences: { nodeIntegration: false, contextIsolation: true, sandbox: true } })
      window = current
      current.setMenu(null)
      current.webContents.setWindowOpenHandler(() => ({ action: 'deny' }))
      current.webContents.on('will-navigate', event => event.preventDefault())
      current.on('closed', () => { if (window === current) { window = undefined; cancel() } })
      current.once('ready-to-show', () => { if (window === current) current.show() })
      const title = chinese ? '正在准备数据' : 'Preparing your data'
      const detail = chinese ? '正在检查、备份或升级数据。历史记录较多时可能需要一些时间。' : 'Checking, backing up or upgrading data. Larger histories may take longer.'
      const action = chinese ? '关闭此窗口可取消启动。' : 'Close this window to cancel startup.'
      const html = `<!doctype html><html lang="${chinese ? 'zh-CN' : 'en'}"><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'"><title>Astral</title><style>body{margin:28px;background:#f4f4f1;color:#242422;font:14px/1.6 system-ui,sans-serif}h1{font-size:20px;line-height:1.3;margin:0 0 12px;font-weight:600}p{margin:8px 0;color:#666660}@media(prefers-color-scheme:dark){body{background:#232321;color:#e9e9e4}p{color:#aaa9a0}}</style><h1>${title}</h1><p>${detail}</p><p>${action}</p></html>`
      void current.loadURL(`data:text/html;charset=utf-8,${encodeURIComponent(html)}`).catch(() => { if (window === current) { clear(); cancel() } })
    }, 300)
  }
}
