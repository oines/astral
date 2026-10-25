// getRandomValues is available on HTTP origins, unlike randomUUID. Keep request
// identities cryptographically random on LAN and Tailscale addresses as well.
export function randomID(): string {
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16))
  bytes[6] = (bytes[6]! & 0x0f) | 0x40
  bytes[8] = (bytes[8]! & 0x3f) | 0x80
  const hex = Array.from(bytes, value => value.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    const clipboard: Partial<Clipboard> | undefined = globalThis.navigator?.clipboard
    if (clipboard?.writeText) {
      await clipboard.writeText(text)
      return true
    }
  } catch { /* A denied clipboard permission can still allow a user-initiated copy. */ }
  if (typeof document === 'undefined' || typeof document.execCommand !== 'function') return false
  const previousFocus = document.activeElement
  const selection = document.getSelection()
  const ranges = selection ? Array.from({ length: selection.rangeCount }, (_, index) => selection.getRangeAt(index).cloneRange()) : []
  const input = document.createElement('textarea')
  input.value = text
  input.readOnly = true
  input.style.position = 'fixed'
  input.style.opacity = '0'
  input.style.pointerEvents = 'none'
  document.body.append(input)
  try {
    input.focus({ preventScroll: true })
    input.select()
    return document.execCommand('copy')
  } catch { return false }
  finally {
    input.remove()
    if (previousFocus instanceof HTMLElement) previousFocus.focus({ preventScroll: true })
    selection?.removeAllRanges()
    for (const range of ranges) selection?.addRange(range)
  }
}
