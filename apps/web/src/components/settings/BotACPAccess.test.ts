import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, getCurrentInstance } from 'vue'
import { createI18n } from 'vue-i18n'
import { renderToString } from 'vue/server-renderer'
import { settingsEn, settingsZh } from '../../i18n/settings'
import BotACPAccess from './BotACPAccess.vue'

type Purpose = 'botApi' | 'acp'
interface Token { id: string; name: string; created_at: string; last_used_at?: string }
type AccessToken = Token & { purpose: Purpose }
interface Setup {
  load(): Promise<unknown>
  create(): Promise<void>
  revoke(token: AccessToken): Promise<void>
  closeCreation(): void
  name: string
  purpose: Purpose | ''
  createdToken: string
  directories: Partial<Record<Purpose, { endpoint: string; tokens: Token[] }>>
  tokens: AccessToken[]
  loadErrors: Partial<Record<Purpose, string>>
}

const fixture = vi.hoisted(() => ({
  setup: undefined as Setup | undefined,
  initialDirectories: undefined as Setup['directories'] | undefined,
  acp: { list: vi.fn(), create: vi.fn(), revoke: vi.fn() },
  botApi: { list: vi.fn(), create: vi.fn(), revoke: vi.fn() },
}))
vi.mock('@astral/sdk', () => ({
  listAcpTokens: fixture.acp.list, createAcpToken: fixture.acp.create, revokeAcpToken: fixture.acp.revoke,
  listBotApiTokens: fixture.botApi.list, createBotApiToken: fixture.botApi.create, revokeBotApiToken: fixture.botApi.revoke,
}))
vi.mock('../../stores/app', () => ({ useAppStore: () => ({ displayTimezone: 'UTC' }) }))
vi.mock('@astral/ui', async () => ({
  ...await import('../../../../../packages/ui/src/useDelayedPending'),
  SettingsRow: (await import('../../../../../packages/ui/src/SettingsRow.vue')).default,
  SettingsSection: {
    setup(_: unknown, { slots }: { slots: { default?: () => unknown } }) {
      fixture.setup = (getCurrentInstance()!.parent as unknown as { setupState: Setup }).setupState
      Object.assign(fixture.setup.directories, fixture.initialDirectories)
      return () => slots.default?.()
    },
  },
  Modal: { render: () => null },
  ConfirmPopover: { render: () => null },
  Button: { render: () => null },
  Select: { render: () => null },
}))

const tokenResponse = { data: { id: 'token1', name: 'Demo client', created_at: '2026-11-26T00:00:00Z', token: 'new-secret' } }

beforeEach(() => {
  fixture.setup = undefined
  fixture.initialDirectories = undefined
  fixture.acp.list.mockReset().mockResolvedValue({ data: { endpoint: 'wss://astral.example/acp', tokens: [] } })
  fixture.botApi.list.mockReset().mockResolvedValue({ data: { endpoint: 'https://astral.example/api/v1/bot', tokens: [] } })
  for (const api of [fixture.acp, fixture.botApi]) {
    api.create.mockReset().mockResolvedValue(tokenResponse)
    api.revoke.mockReset().mockResolvedValue({ data: { ok: true } })
  }
})

function renderPanel(locale = 'en') {
  const app = createSSRApp(BotACPAccess, { agentId: 'bot1' })
  app.use(createI18n({ legacy: false, locale, messages: { en: settingsEn, 'zh-CN': settingsZh } }))
  return renderToString(app)
}

async function panel() {
  await renderPanel()
  const setup = fixture.setup!
  await setup.load()
  setup.name = '  Demo client  '
  return setup
}

describe('Bot external access', () => {
  it.each([
    { locale: 'en', prefix: '', label: 'docs' },
    { locale: 'zh-CN', prefix: 'zh-cn/', label: '使用文档' },
  ])('links each address to its own guide in $locale', async ({ locale, prefix, label }) => {
    fixture.initialDirectories = {
      botApi: { endpoint: 'https://astral.example/api/v1/bot', tokens: [] },
      acp: { endpoint: 'wss://astral.example/acp', tokens: [] },
    }
    const html = await renderPanel(locale)
    const links = [...html.matchAll(/<a\b[^>]*>/g)].map(match => match[0])
    expect(links).toHaveLength(2)
    for (const [index, { slug, name }] of [{ slug: 'bot-api', name: 'Bot API' }, { slug: 'acp', name: 'ACP' }].entries()) {
      expect(links[index]).toContain(`href="https://astral.oines.dev/${prefix}docs/hosting/${slug}/"`)
      expect(links[index]).toContain(`aria-label="${name} ${label}"`)
      expect(links[index]).toContain('target="_blank"')
      expect(links[index]).toContain('rel="noopener noreferrer"')
    }
  })

  it('loads both addresses, requires a purpose and only reveals the returned secret in the creation dialog', async () => {
    const setup = await panel()
    expect(setup.directories.acp?.endpoint).toBe('wss://astral.example/acp')
    expect(setup.directories.botApi?.endpoint).toBe('https://astral.example/api/v1/bot')
    await setup.create()
    expect(fixture.botApi.create).not.toHaveBeenCalled()
    expect(fixture.acp.create).not.toHaveBeenCalled()
    setup.purpose = 'botApi'
    await setup.create()
    expect(fixture.botApi.create).toHaveBeenCalledWith({ path: { id: 'bot1' }, body: { name: 'Demo client' } })
    expect(fixture.acp.create).not.toHaveBeenCalled()
    expect(setup.createdToken).toBe('new-secret')
    expect(setup.tokens[0]).toMatchObject({ id: 'token1', name: 'Demo client', purpose: 'botApi' })
    expect(setup.tokens[0]).not.toHaveProperty('token')
    expect(fixture.acp.list).toHaveBeenCalledTimes(1)
    expect(fixture.botApi.list).toHaveBeenCalledTimes(1)
    setup.closeCreation()
    expect(setup.createdToken).toBe('')
  })

  it('discards a secret from an earlier dialog after the user leaves', async () => {
    let respond!: (value: typeof tokenResponse) => void
    fixture.acp.create.mockReturnValue(new Promise(resolve => { respond = resolve }))
    const setup = await panel()
    setup.purpose = 'acp'
    const creation = setup.create()
    setup.closeCreation()
    respond(tokenResponse)
    await creation
    expect(setup.createdToken).toBe('')
    expect(setup.tokens[0]).toMatchObject({ id: 'token1', purpose: 'acp' })
  })

  it('shows both purposes in one list and revokes exactly the selected token', async () => {
    const existing = { id: 'token1', name: 'Demo client', created_at: '2026-11-26T00:00:00Z' }
    const other = { ...existing, name: 'Integration', created_at: '2026-11-27T00:00:00Z', last_used_at: '2026-11-27T00:01:00Z' }
    fixture.acp.list.mockResolvedValue({ data: { endpoint: 'wss://astral.example/acp', tokens: [existing] } })
    fixture.botApi.list.mockResolvedValue({ data: { endpoint: 'https://astral.example/api/v1/bot', tokens: [other] } })
    const setup = await panel()
    expect(setup.tokens).toEqual([{ ...other, purpose: 'botApi' }, { ...existing, purpose: 'acp' }])

    await setup.revoke({ ...existing, purpose: 'acp' })

    expect(fixture.acp.revoke).toHaveBeenCalledWith({ path: { id: 'bot1', token_id: 'token1' } })
    expect(fixture.botApi.revoke).not.toHaveBeenCalled()
    expect(setup.tokens).toEqual([{ ...other, purpose: 'botApi' }])
    expect(fixture.acp.list).toHaveBeenCalledTimes(1)
    expect(fixture.botApi.list).toHaveBeenCalledTimes(1)
  })

  it('keeps ACP access usable if the Bot API directory fails to load', async () => {
    fixture.botApi.list.mockRejectedValue(new Error('Unavailable'))
    const setup = await panel()
    expect(setup.loadErrors).toEqual({ botApi: 'Unavailable' })
    expect(setup.directories.acp?.endpoint).toBe('wss://astral.example/acp')
    setup.purpose = 'acp'
    await setup.create()
    expect(setup.createdToken).toBe('new-secret')
  })

  it('keeps a newly created token while the other directory finishes loading', async () => {
    const setup = await panel()
    let completeAcp!: (value: { data: { endpoint: string; tokens: Token[] } }) => void
    let completeBotApi!: (value: { data: { endpoint: string; tokens: Token[] } }) => void
    fixture.acp.list.mockReturnValue(new Promise(resolve => { completeAcp = resolve }))
    fixture.botApi.list.mockReturnValue(new Promise(resolve => { completeBotApi = resolve }))
    const loading = setup.load()
    setup.purpose = 'acp'
    await setup.create()
    completeAcp({ data: { endpoint: 'wss://astral.example/acp', tokens: [] } })
    completeBotApi({ data: { endpoint: 'https://astral.example/api/v1/bot', tokens: [
      { id: 'other', name: 'Integration', created_at: '2026-11-25T00:00:00Z' },
    ] } })
    await loading
    expect(setup.tokens.map(token => [token.id, token.purpose])).toEqual([['token1', 'acp'], ['other', 'botApi']])
  })
})
