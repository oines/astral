import { client } from '@astral/sdk/client'
import { errorText, request } from './api'

export type EstimateState = 'pending' | 'idle' | 'running' | 'complete' | 'partial' | 'failed' | 'canceled'
export interface StorageChat {
  conversation_id: string
  title: string
  platform: string
  connection_id?: string
  account_name?: string
  binding_id: string
  binding_revision: number
  session_id: string
  session_revision: number
  logical_bytes: number
  archive_count?: number
  archive_oldest?: string
  archive_newest?: string
  retention_days: number
  im_bytes: number
  rollout_bytes: number
  estimate_state: EstimateState
  error?: string
}
export interface StorageSession {
  session_id: string
  title: string
  revision: number
  archived: boolean
  logical_bytes: number
  estimate_state: EstimateState
  error?: string
}
export interface StorageBot {
  agent_id: string
  name: string
  logical_bytes: number
  chats: StorageChat[]
  sessions: StorageSession[]
}
export interface StorageSnapshot {
  directory: string
  physical: {
    total_bytes: number
    disk_total_bytes?: number
    disk_used_bytes?: number
    database_bytes: number
    backup_bytes: number
    cache_bytes: number
    log_bytes: number
    temporary_bytes: number
    available_bytes: number
    items: { id: string; bytes: number }[]
    errors: string[]
  }
  estimate: { state: EstimateState; updated_at?: string; error?: string }
  agents: StorageBot[]
  reclaim: { state: 'idle' | 'queued' | 'waiting' | 'running' | 'complete' | 'failed'; requested_at?: string; updated_at?: string; error?: string }
}
export interface StorageCleanupItem {
  token: string
  kind: 'upgrade_backup' | 'model_cache' | 'temporary' | 'log' | 'update_cache'
  name: string
  bytes: number
  retained_default?: boolean
  protected?: boolean
  reason?: string
}
export interface StorageCleanup {
  items: StorageCleanupItem[]
  total_bytes: number
  error?: string
}
export interface StorageCleanupOperation {
  operation_id: string
  state: 'running' | 'complete' | 'failed'
  removed_bytes: number
  removed_tokens: string[]
  error?: string
}
export interface StorageConversationOperation {
  id: string
  kind: string
  state: 'queued' | 'running' | 'complete' | 'failed'
  conversation_id: string
  old_session_id?: string
  new_session_id?: string
  removed_count?: number
  error?: string
}
function get<T>(path: string) {
  return request(client.get<{ 200: T }, Record<number, unknown>>({ url: `/api/v1/storage/${path}` }))
}
export class StorageHttpError extends Error {
  constructor(public status: number, error: unknown) { super(errorText(error)) }
}
export function storageRequestRejected(error: unknown) { return error instanceof StorageHttpError && error.status >= 400 && error.status < 500 && ![408, 429].includes(error.status) }
async function post<T>(path: string, body: unknown = {}): Promise<T> {
  const result = await client.post<{ 200: T }, Record<number, unknown>>({ url: `/api/v1/storage/${path}`, body, headers: { 'Content-Type': 'application/json' }, throwOnError: false })
  if (result.error) throw new StorageHttpError(result.response?.status || 0, result.error)
  return request(Promise.resolve(result))
}
export const getStorage = () => get<StorageSnapshot>('manage')
export const refreshStorage = (cancel = false) => post<StorageSnapshot>('refresh', cancel ? { cancel: true } : {})
export const reclaimStorage = () => post<StorageSnapshot>('reclaim')
export const getStorageCleanup = () => get<StorageCleanup>('cleanup')
export const cleanStorageApplication = (operation_id: string, tokens: string[]) => post<StorageCleanupOperation>('cleanup', { operation_id, tokens })
export const getStorageCleanupOperation = (id: string) => get<StorageCleanupOperation>(`cleanup/${encodeURIComponent(id)}`)
export const retryStorageCleanupOperation = (id: string) => post<StorageCleanupOperation>(`cleanup/${encodeURIComponent(id)}/retry`)
export const cleanStorageChat = (id: string, operation_id: string, before?: string, retention_days?: number) => post<StorageConversationOperation>(`conversations/${encodeURIComponent(id)}/cleanup`, { operation_id, ...(before ? { before } : {}), ...(retention_days !== undefined ? { retention_days } : {}) })
export const resetStorageChat = (id: string, operation_id: string, revision: number) => post<StorageConversationOperation>(`conversations/${encodeURIComponent(id)}/reset`, { operation_id, revision })
export const getStorageConversationOperation = (id: string) => get<StorageConversationOperation>(`operations/${encodeURIComponent(id)}`)
export const retryStorageConversationOperation = (id: string) => post<StorageConversationOperation>(`operations/${encodeURIComponent(id)}/retry`)

export function storageShare(value: number, total: number) { return total > 0 ? `${Math.max(0, Math.min(100, value / total * 100))}%` : '0%' }
