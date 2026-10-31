import { describe, expect, it } from 'vitest'
import { storageChatAccount, type StorageAccount } from './storage-presentation'
import type { StorageChat } from './storage'

const chat = (connection_id: string, platform = 'telegram', account_name = 'Telegram') => ({ connection_id, platform, account_name }) as StorageChat
const label = (value: StorageChat, chats: StorageChat[], accounts: StorageAccount[] = []) => storageChatAccount(value, chats, accounts, 'zh', number => `账号 ${number}`)

describe('storage chat identities', () => {
  it('shows the platform once when a Bot has one account on that platform', () => {
    const telegram = chat('t')
    const qq = chat('q', 'qq', 'QQ NapCat')
    expect(label(telegram, [telegram, qq])).toBe('Telegram')
    expect(label(qq, [telegram, qq])).toBe('QQ')
  })
  it('uses usernames to distinguish multiple accounts, including accounts without archived chats', () => {
    const first = chat('a')
    const accounts = [{ id: 'a', platform: 'telegram', name: 'Telegram', username: '@work_bot' }, { id: 'b', platform: 'telegram', name: 'Telegram', username: 'personal_bot' }]
    expect(label(first, [first], accounts)).toBe('Telegram · @work_bot')
    expect(label(chat('b'), [first], accounts)).toBe('Telegram · @personal_bot')
  })
  it('preserves meaningful names but does not repeat default names or ambiguous aliases', () => {
    const first = chat('a', 'qq', '工作账号')
    const second = chat('b', 'qq', 'QQ NapCat')
    expect(label(first, [first, second])).toBe('QQ · 工作账号')
    expect(label(second, [first, second])).toBe('QQ · 账号 2')
    const accounts = [{ id: 'a', platform: 'qq', name: 'QQ NapCat', account_id: '12345' }, { id: 'b', platform: 'qq', name: 'QQ NapCat', account_id: '67890' }]
    expect(label(second, [first, second], accounts)).toBe('QQ · 67890')
  })
  it('distinguishes old and new connections to the same platform account', () => {
    const first = chat('a')
    const second = chat('b')
    const accounts = [{ id: 'a', platform: 'telegram', name: '旧账号连接', username: 'same_bot', account_id: '123' }, { id: 'b', platform: 'telegram', name: '当前连接', username: 'same_bot', account_id: '123' }]
    expect(label(first, [first, second], accounts)).toBe('Telegram · 旧账号连接')
    expect(label(second, [first, second], accounts)).toBe('Telegram · 当前连接')
    expect(label(second, [first, second], accounts.map(value => ({ ...value, name: 'Telegram' })))).toBe('Telegram · 账号 2')
  })
})
