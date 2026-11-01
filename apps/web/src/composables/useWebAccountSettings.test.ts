import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useWebAccountSettings } from './useWebAccountSettings'

const api = vi.hoisted(() => ({ account: vi.fn<(username: string, password?: string) => Promise<{ ok: boolean }>>() }))
vi.mock('../lib/auth', () => ({ authAPI: api }))
beforeEach(() => { api.account.mockReset() })

function setup(configured = true) {
  const remote = { configured, username: configured ? 'confirmed-user' : '' }
  const settings = ref({ ...remote })
  const refreshAuthentication = vi.fn(async () => undefined)
  const refreshSettings = vi.fn(async () => {
    settings.value = { ...remote }
    if (!editor.usernameDirty.value) editor.username.value = remote.username
  })
  const editor = useWebAccountSettings({ settings, authenticated: () => true, refreshAuthentication, refreshSettings, recent: operation => operation(), message: key => key })
  editor.username.value = remote.username
  api.account.mockImplementation(async username => { remote.username = username; remote.configured = true; return { ok: true } })
  return { editor, remote, settings, refreshAuthentication, refreshSettings }
}

it('leaves username and password drafts unapplied until their named action is submitted', async () => {
  const { editor, remote } = setup()
  editor.username.value = 'new-user'; editor.usernameDirty.value = true
  editor.password.value = 'a new password'; editor.confirmPassword.value = 'a new password'
  await Promise.resolve()
  expect(api.account).not.toHaveBeenCalled()
  expect(remote.username).toBe('confirmed-user')
  await editor.saveUsername()
  expect(api.account).toHaveBeenCalledExactlyOnceWith('new-user')
  expect(remote.username).toBe('new-user')
  expect(editor.password.value).toBe('a new password')
  expect(editor.confirmPassword.value).toBe('a new password')
})

describe('separate account submission scopes', () => {
  it('changes the password for the confirmed username even when its independent draft is empty', async () => {
    const { editor, remote } = setup()
    editor.username.value = ''; editor.usernameDirty.value = true
    editor.password.value = 'a new password'; editor.confirmPassword.value = 'a new password'; editor.changingPassword.value = true
    await editor.savePassword()
    expect(api.account).toHaveBeenCalledExactlyOnceWith('confirmed-user', 'a new password')
    expect(remote.username).toBe('confirmed-user')
    expect(editor.username.value).toBe('')
    expect(editor.usernameDirty.value).toBe(true)
    expect(editor.password.value).toBe('')
  })

  it('does not validate or submit the password draft during a username change', async () => {
    const { editor } = setup()
    editor.username.value = 'new-user'; editor.usernameDirty.value = true
    editor.password.value = 'short'; editor.confirmPassword.value = 'different'
    await editor.saveUsername()
    expect(api.account).toHaveBeenCalledExactlyOnceWith('new-user')
    expect(editor.password.value).toBe('short')
    expect(editor.confirmPassword.value).toBe('different')
  })

  it('retains newer username typing after an earlier explicit change completes instead of automatically submitting it', async () => {
    const { editor, remote } = setup()
    let finish!: () => void
    const pending = new Promise<void>(resolve => { finish = resolve })
    api.account.mockImplementationOnce(async username => { await pending; remote.username = username; return { ok: true } })
    editor.username.value = 'submitted-user'; editor.usernameDirty.value = true
    const saving = editor.saveUsername()
    await Promise.resolve()
    editor.username.value = 'newer-draft'
    finish(); await saving
    expect(remote.username).toBe('submitted-user')
    expect(editor.username.value).toBe('newer-draft')
    expect(editor.usernameDirty.value).toBe(true)
    expect(api.account).toHaveBeenCalledOnce()
    await editor.saveUsername()
    expect(api.account).toHaveBeenCalledTimes(2)
    expect(remote.username).toBe('newer-draft')
  })

  it('preserves the username draft after a rejected change', async () => {
    const { editor, remote } = setup()
    api.account.mockRejectedValueOnce(new Error('Username change rejected'))
    editor.username.value = 'new-user'; editor.usernameDirty.value = true
    await editor.saveUsername()
    expect(remote.username).toBe('confirmed-user')
    expect(editor.username.value).toBe('new-user')
    expect(editor.usernameDirty.value).toBe(true)
    expect(editor.usernameAction.error.value).toBe('Username change rejected')
  })

  it('preserves the password form and unrelated username draft after a rejected password change', async () => {
    const { editor } = setup()
    api.account.mockRejectedValueOnce(new Error('Password change rejected'))
    editor.username.value = 'unsubmitted-user'; editor.usernameDirty.value = true
    editor.password.value = 'a new password'; editor.confirmPassword.value = 'a new password'; editor.changingPassword.value = true
    await editor.savePassword()
    expect(editor.password.value).toBe('a new password')
    expect(editor.confirmPassword.value).toBe('a new password')
    expect(editor.changingPassword.value).toBe(true)
    expect(editor.username.value).toBe('unsubmitted-user')
    expect(editor.passwordAction.error.value).toBe('Password change rejected')
  })

  it('does not send a second account mutation while a username change is pending', async () => {
    const { editor } = setup()
    let finish!: () => void
    api.account.mockImplementationOnce(() => new Promise(resolve => { finish = () => resolve({ ok: true }) }))
    editor.username.value = 'new-user'; editor.usernameDirty.value = true
    editor.password.value = 'a new password'; editor.confirmPassword.value = 'a new password'
    const saving = editor.saveUsername()
    await editor.savePassword()
    expect(api.account).toHaveBeenCalledExactlyOnceWith('new-user')
    finish(); await saving
  })

  it('still creates the first web account with its complete username and password form', async () => {
    const { editor, remote } = setup(false)
    editor.username.value = ' new-user '; editor.usernameDirty.value = true
    editor.password.value = 'a new password'; editor.confirmPassword.value = 'a new password'
    await editor.savePassword()
    expect(api.account).toHaveBeenCalledExactlyOnceWith('new-user', 'a new password')
    expect(remote.configured).toBe(true)
    expect(editor.usernameDirty.value).toBe(false)
  })
})
