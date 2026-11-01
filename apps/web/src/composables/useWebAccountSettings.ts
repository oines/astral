import { ref, type Ref } from 'vue'
import { authAPI } from '../lib/auth'
import { useAction } from './useAction'

type Account = { configured: boolean; username: string }
type Options = {
  settings: Readonly<Ref<Account | undefined>>
  authenticated: () => boolean
  refreshAuthentication: () => Promise<unknown>
  refreshSettings: () => Promise<unknown>
  recent: <T>(operation: () => Promise<T>) => Promise<T>
  message: (key: string) => string
}

export function useWebAccountSettings(options: Options) {
  const username = ref('')
  const usernameDirty = ref(false)
  const password = ref('')
  const confirmPassword = ref('')
  const changingPassword = ref(false)
  const usernameAction = useAction()
  const passwordAction = useAction()
  function validateUsername(value: string) {
    if (!value) throw new Error(options.message('authUsernameRequired'))
    if (new TextEncoder().encode(value).byteLength > 128 || /[\r\n\0]/.test(value)) throw new Error(options.message('authUsernameInvalid'))
  }
  async function saveUsername() {
    if (!options.settings.value?.configured || usernameAction.busy.value || passwordAction.busy.value) return
    const submitted = username.value.trim()
    if (submitted === options.settings.value.username) {
      username.value = submitted; usernameDirty.value = false; usernameAction.error.value = ''
      return
    }
    await usernameAction.run(async () => {
      validateUsername(submitted)
      await options.recent(() => authAPI.account(submitted))
      if (username.value.trim() === submitted) { username.value = submitted; usernameDirty.value = false }
      await options.refreshAuthentication()
      if (options.authenticated()) await options.refreshSettings()
    })
  }
  async function savePassword() {
    if (!options.settings.value || passwordAction.busy.value || usernameAction.busy.value) return
    const configured = options.settings.value.configured
    const name = configured ? options.settings.value.username : username.value.trim()
    await passwordAction.run(async () => {
      validateUsername(name)
      if (!password.value) throw new Error(options.message('authPasswordRequired'))
      const length = new TextEncoder().encode(password.value).byteLength
      if (length < 12 || length > 1024) throw new Error(options.message('authPasswordLength'))
      if (password.value !== confirmPassword.value) throw new Error(options.message('authPasswordMismatch'))
      await options.recent(() => authAPI.account(name, password.value))
      password.value = ''; confirmPassword.value = ''; changingPassword.value = false
      if (!configured) { username.value = name; usernameDirty.value = false }
      await options.refreshAuthentication()
      if (options.authenticated()) await options.refreshSettings()
    })
  }
  return { username, usernameDirty, password, confirmPassword, changingPassword, usernameAction, passwordAction, saveUsername, savePassword }
}
