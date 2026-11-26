import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, getCurrentInstance } from 'vue'
import { renderToString } from 'vue/server-renderer'
import BotACPAccess from './BotACPAccess.vue'

interface Setup {
  load(): Promise<unknown>
  create(): Promise<void>
  revoke(id: string): Promise<void>
  closeCreation(): void
  name: string
  createdToken: string
  directory: { endpoint: string; tokens: Array<{ id: string; name: string; created_at: string }> }
}

const fixture = vi.hoisted(() => ({ setup: undefined as Setup | undefined, list: vi.fn(), create: vi.fn(), revoke: vi.fn() }))
vi.mock('@astral/sdk', () => ({ listAcpTokens: fixture.list, createAcpToken: fixture.create, revokeAcpToken: fixture.revoke }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }) }))
vi.mock('../../stores/app', () => ({ useAppStore: () => ({ displayTimezone: 'UTC' }) }))
vi.mock('@astral/ui', async () => ({
  ...await import('../../../../../packages/ui/src/useDelayedPending'),
  SettingsRow: { render: () => null },
  SettingsSection: {
    setup() {
      fixture.setup = (getCurrentInstance()!.parent as unknown as { setupState: Setup }).setupState
      return () => null
    },
  },
  Modal: { render: () => null },
  ConfirmPopover: { render: () => null },
  Button: { render: () => null },
}))

const tokenResponse = { data: { id: 'token1', name: 'Demo client', created_at: '2026-11-26T00:00:00Z', token: 'new-secret' } }

beforeEach(() => {
  fixture.setup = undefined
  fixture.list.mockReset().mockResolvedValue({ data: { endpoint: 'wss://astral.example/acp', tokens: [] } })
  fixture.create.mockReset().mockResolvedValue(tokenResponse)
  fixture.revoke.mockReset().mockResolvedValue({ data: { ok: true } })
})

async function panel() {
  await renderToString(createSSRApp(BotACPAccess, { agentId: 'bot1' }))
  const setup = fixture.setup!
  await setup.load()
  setup.name = '  Demo client  '
  return setup
}

describe('Bot ACP tokens', () => {
  it('keeps the returned secret separate from the token directory and clears it when closed', async () => {
    const setup = await panel()
    await setup.create()
    expect(fixture.create).toHaveBeenCalledWith({ path: { id: 'bot1' }, body: { name: 'Demo client' } })
    expect(setup.createdToken).toBe('new-secret')
    expect(setup.directory.tokens[0]).toMatchObject({ id: 'token1', name: 'Demo client' })
    expect(setup.directory.tokens[0]).not.toHaveProperty('token')
    expect(fixture.list).toHaveBeenCalledTimes(1)
    setup.closeCreation()
    expect(setup.createdToken).toBe('')
  })

  it('discards a secret from an earlier dialog after the user leaves', async () => {
    let respond!: (value: typeof tokenResponse) => void
    fixture.create.mockReturnValue(new Promise(resolve => { respond = resolve }))
    const setup = await panel()
    const creation = setup.create()
    setup.closeCreation()
    respond(tokenResponse)
    await creation
    expect(setup.createdToken).toBe('')
    expect(setup.directory.tokens[0]?.id).toBe('token1')
  })

  it('removes a revoked token without fetching the directory again', async () => {
    const existing = { id: 'token1', name: 'Demo client', created_at: '2026-11-26T00:00:00Z' }
    const other = { ...existing, id: 'token2', name: 'Another client' }
    fixture.list.mockResolvedValue({ data: { endpoint: 'wss://astral.example/acp', tokens: [existing, other] } })
    const setup = await panel()

    await setup.revoke(existing.id)

    expect(fixture.revoke).toHaveBeenCalledWith({ path: { id: 'bot1', token_id: 'token1' } })
    expect(setup.directory.tokens).toEqual([other])
    expect(fixture.list).toHaveBeenCalledTimes(1)
  })
})
