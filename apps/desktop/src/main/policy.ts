/** Keep the native bridge restricted to the main frame of our own Web app. */
export function trustedSender(frameURL: string | undefined, parentFrame: unknown, serverURL: string): boolean {
  if (!frameURL || parentFrame) return false
  try { return new URL(frameURL).origin === new URL(serverURL).origin } catch { return false }
}

interface RequestFrame { url: string; parent: RequestFrame | null }
interface DesktopRequest {
  url: string
  method: string
  resourceType: string
  webContentsId?: number
  frame?: RequestFrame | null
  requestHeaders: Record<string, string>
}

/** The Desktop credential is restricted to our main frame and managed origin. */
export function desktopRequestHeaders(details: DesktopRequest, windowID: number | undefined, serverURL: string, credential: string): Record<string, string> {
  const headers = { ...details.requestHeaders }
  for (const key of Object.keys(headers)) {
    if (key.toLowerCase() === 'authorization' && headers[key] === `Desktop ${credential}`) delete headers[key]
  }
  if (windowID === undefined || details.webContentsId !== windowID) return headers
  try {
    const target = new URL(details.url)
    const server = new URL(serverURL)
    if (target.host !== server.host || !['http:', 'ws:'].includes(target.protocol)) return headers
    const frame = details.frame
    const trustedMain = frame && trustedSender(frame.url, frame.parent, serverURL)
    // PDF previews are the sole child-frame exception, restricted to a local
    // read-only file request initiated directly by our trusted main frame.
    const trustedPDF = frame?.parent && !frame.parent.parent && trustedSender(frame.parent.url, null, serverURL)
      && (frame.url === 'about:blank' || new URL(frame.url).origin === server.origin)
      && details.resourceType === 'subFrame' && details.method === 'GET'
      && /^\/api\/v1\/files\/[^/]+\/content$/.test(target.pathname)
    if (trustedMain || trustedPDF) headers.Authorization = `Desktop ${credential}`
  } catch { /* Missing/destroyed/foreign frames never receive native identity. */ }
  return headers
}
export function externalURL(value: unknown): string {
  if (typeof value !== 'string' || value.length > 8192) throw new Error('Invalid link.')
  const parsed = new URL(value)
  if (!['https:', 'http:', 'mailto:'].includes(parsed.protocol) || parsed.username || parsed.password) throw new Error('This link cannot be opened externally.')
  return parsed.href
}
