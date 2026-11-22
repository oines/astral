import type { AstralInternalStorageAgent, AstralInternalStorageChat, AstralInternalStorageSession, AstralInternalStorageSnapshot } from '@astral/sdk'
import { bytes } from './api'

export type StorageBot = AstralInternalStorageAgent
export type StorageChat = AstralInternalStorageChat
export type StorageSession = AstralInternalStorageSession
export type StorageSnapshot = AstralInternalStorageSnapshot
export type StorageSource = { kind: 'chat'; value: StorageChat; bot: StorageBot } | { kind: 'session'; value: StorageSession; bot: StorageBot }

export function storageSources(bots: StorageBot[], filter: string, search: string): StorageSource[] {
  const query = search.trim().toLocaleLowerCase()
  const rows: StorageSource[] = []
  for (const bot of bots) {
    if (filter && bot.agent_id !== filter) continue
    for (const value of bot.chats) rows.push({ kind: 'chat', value, bot })
    for (const value of bot.sessions) rows.push({ kind: 'session', value, bot })
  }
  return rows.filter(row => row.value.title.toLocaleLowerCase().includes(query))
    .sort((left, right) => right.value.logical_bytes - left.value.logical_bytes || left.value.title.localeCompare(right.value.title))
}

export function storageShare(value: number, total: number) { return total > 0 ? `${Math.max(0, Math.min(100, value / total * 100))}%` : '0%' }

export function storageBytes(value: number) { return bytes(value).replace('.0 ', ' ') }

export function storageSize(value: number, state: string, t: (key: string, args: { size: string }) => string) {
  const size = storageBytes(value)
  if (state === 'complete') return value === 0 ? size : t('storageEstimated', { size })
  if (value > 0) return t('storageEstimatePartial', { size })
  return '—'
}

export function retentionNeedsConfirmation(current: number, next: number) {
  return next > 0 && (current === 0 || next < current)
}
