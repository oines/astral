import { client } from '@astral/sdk/client'

client.setConfig({ baseUrl: '', credentials: 'same-origin', throwOnError: true })
client.interceptors.response.use(response => {
  if (response.status === 401) window.dispatchEvent(new Event('astral:unauthorized'))
  return response
})

export async function request<T>(pending: Promise<{ data?: T; error?: unknown }>): Promise<T> {
  const result = await pending
  if (result.error) throw result.error
  if (result.data === undefined) throw new Error('The server returned no response.')
  return result.data
}
export function errorText(error: unknown): string {
  if (error && typeof error === 'object' && 'message' in error && typeof error.message === 'string') return error.message
  return error instanceof Error ? error.message : 'Unable to complete this request. Check the server connection.'
}
export function newMeta() {
  // A zero Go timestamp lets the server assign creation/update times.
  return { id: '', revision: 0, created_at: '0001-01-01T00:00:00Z', updated_at: '0001-01-01T00:00:00Z' }
}
export function object(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}
export function clone<T>(value: T): T { return JSON.parse(JSON.stringify(value)) as T }
export function fileURL(id: string, sessionID: string, download = false) {
  return `/api/v1/files/${encodeURIComponent(id)}/content?session_id=${encodeURIComponent(sessionID)}&download=${download}`
}
export function imageURL(sessionID: string, entryID: string, index: number) {
  return `/api/v1/sessions/${encodeURIComponent(sessionID)}/entries/${encodeURIComponent(entryID)}/images/${index}`
}
export function bytes(value: number) {
  if (value < 1024) return `${value} B`
  const n = Math.floor(Math.log(value) / Math.log(1024))
  return `${(value / 1024 ** n).toFixed(1)} ${['B', 'KB', 'MB', 'GB', 'TB'][Math.min(n, 4)]}`
}
