import type { StorageCleanupOperation, StorageConversationOperation } from './storage'
import { randomID } from './browser'

export type StorageTaskRequest = {
  operation_id: string
  kind: 'application' | 'archive' | 'retention' | 'reset'
  conversation_id?: string
  tokens?: string[]
  before?: string
  retention_days?: number
  revision?: number
}
export interface StorageTaskMemory {
  requests: Record<string, StorageTaskRequest>
  conversations: Record<string, StorageConversationOperation>
  application?: StorageCleanupOperation
}
const key = 'astral:storage-tasks:v1'
function unfinished(memory: Partial<StorageTaskMemory>): StorageTaskMemory {
  return {
    requests: memory.requests || {},
    conversations: Object.fromEntries(Object.entries(memory.conversations || {}).filter(([, value]) => value.state !== 'complete')),
    ...(memory.application && memory.application.state !== 'complete' ? { application: memory.application } : {}),
  }
}
export function readStorageTasks(storage: Pick<Storage, 'getItem'>): StorageTaskMemory {
  try {
    const value = JSON.parse(storage.getItem(key) || '{}') as Partial<StorageTaskMemory>
    return unfinished(value)
  } catch { return { requests: {}, conversations: {} } }
}
export function writeStorageTasks(storage: Pick<Storage, 'setItem'>, memory: StorageTaskMemory) {
  try { storage.setItem(key, JSON.stringify(unfinished(memory))) }
  catch { /* Storage access can be blocked; the in-memory task remains usable. */ }
}
// Keep the exact confirmed request until the Server acknowledges it. A response
// lost after acceptance must never create a second destructive operation.
export function storageTaskRequest(requests: Record<string, StorageTaskRequest>, key: string, value: Omit<StorageTaskRequest, 'operation_id'>) {
  return requests[key] ||= { ...value, operation_id: randomID() }
}
