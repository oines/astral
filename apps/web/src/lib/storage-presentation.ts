import { adapterLabel, imAdapters, platformLabel } from './im-platforms'
import type { StorageChat } from './storage'

export interface StorageAccount {
  id: string
  platform: string
  name: string
  username?: string
  account_id?: string
}

export function storageChatAccount(chat: StorageChat, chats: StorageChat[], accounts: StorageAccount[], locale: string, fallback: (number: number) => string) {
  const platform = platformLabel(chat.platform, locale)
  const ids = [...new Set([
    ...accounts.filter(account => account.platform === chat.platform).map(account => account.id),
    ...chats.filter(value => value.platform === chat.platform).map(value => value.connection_id).filter((id): id is string => !!id),
  ])].sort()
  if (ids.length < 2 || !chat.connection_id) return platform

  const account = accounts.find(value => value.id === chat.connection_id)
  const peers = accounts.filter(value => value.platform === chat.platform && value.id !== chat.connection_id)
  const username = account?.username?.replace(/^@/, '')
  if (username && !peers.some(value => value.username?.replace(/^@/, '').toLocaleLowerCase() === username.toLocaleLowerCase())) return `${platform} · ${chat.platform === 'telegram' ? '@' : ''}${username}`

  const name = (account?.name || chat.account_name || '').trim()
  const defaultNames = [platform, chat.platform, ...imAdapters.filter(value => value.platform === chat.platform).flatMap(value => [value.name, adapterLabel(value.id, 'en'), adapterLabel(value.id, 'zh')])]
  const repeatedName = accounts.some(value => value.id !== chat.connection_id && value.platform === chat.platform && value.name.trim().toLocaleLowerCase() === name.toLocaleLowerCase())
    || chats.some(value => value.connection_id !== chat.connection_id && value.platform === chat.platform && value.account_name?.trim().toLocaleLowerCase() === name.toLocaleLowerCase())
  if (name && !repeatedName && !defaultNames.some(value => value.toLocaleLowerCase() === name.toLocaleLowerCase())) return `${platform} · ${name}`
  if (account?.account_id && !peers.some(value => value.account_id === account.account_id)) return `${platform} · ${account.account_id}`
  return `${platform} · ${fallback(ids.indexOf(chat.connection_id) + 1)}`
}
