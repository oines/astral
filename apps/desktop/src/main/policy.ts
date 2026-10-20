/** Keep the native bridge restricted to the main frame of our own Web app. */
export function trustedSender(frameURL: string | undefined, parentFrame: unknown, serverURL: string): boolean {
  if (!frameURL || parentFrame) return false
  try { return new URL(frameURL).origin === new URL(serverURL).origin } catch { return false }
}
export function externalURL(value: unknown): string {
  if (typeof value !== 'string' || value.length > 8192) throw new Error('Invalid link.')
  const parsed = new URL(value)
  if (!['https:', 'http:', 'mailto:'].includes(parsed.protocol) || parsed.username || parsed.password) throw new Error('This link cannot be opened externally.')
  return parsed.href
}
