<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import { Button, ConfirmPopover, Modal, SettingsRow, SettingsSection, Switch, useDelayedPending } from '@astral/ui'
import AuthenticationForm from '../AuthenticationForm.vue'
import { authAPI, AuthError, createPasskey, passkeySupported, sessionDevice, type AuthSettings, type PasskeyView, type TOTPSetup, type WebSession } from '../../lib/auth'
import { useAuthStore } from '../../stores/auth'
import { useAppStore } from '../../stores/app'
import { useAction } from '../../composables/useAction'
import { useClipboard } from '../../composables/useClipboard'
import { formatDateTime } from '../../lib/timezone'

const { t, locale } = useI18n()
const auth = useAuthStore()
const app = useAppStore()
const settings = ref<AuthSettings>()
const loading = ref(false)
const loadError = ref('')
const waiting = useDelayedPending(() => loading.value)
const accountAction = useAction()
const factorAction = useAction()
const passkeyAction = useAction()
const deviceAction = useAction()
const passwordlessAction = useAction()
const passwordlessIntent = ref<boolean>()
const accountWaiting = useDelayedPending(() => accountAction.busy.value && !changingPassword.value)
const passwordlessWaiting = useDelayedPending(() => passwordlessAction.busy.value)
const passkeyTask = ref('')
const factorTask = ref('')
const { copy } = useClipboard()
const username = ref('')
const usernameDirty = ref(false)
const password = ref('')
const confirmPassword = ref('')
const changingPassword = ref(false)
const setup = ref<TOTPSetup>()
const setupCode = ref('')
const qrImage = ref('')
const recoveryCodes = ref<string[]>([])
const passkeyName = ref('')
const confirmRemove = ref('')
const reauthOpen = ref(false)
let reauth: { promise: Promise<void>; resolve: () => void; reject: (error: Error) => void } | undefined
let lifecycle = 0
let refreshEpoch = 0
let passkeyController: AbortController | undefined
const hasFactors = computed(() => settings.value?.totp_enabled || Boolean(settings.value?.passkeys?.length))
const canPasskey = computed(() => passkeySupported() && auth.status?.passkey_available === true)
const httpsAddress = computed(() => app.data?.network.public_urls.value.find(url => {
  const address = new URL(url)
  return address.protocol === 'https:' && !address.hostname.includes(':') && !/^(?:[0-9]{1,3}\.){3}[0-9]{1,3}$/.test(address.hostname)
}))
function date(value?: string) {
  return formatDateTime(value, locale.value, app.displayTimezone)
}
async function refresh() {
  const current = lifecycle
  const epoch = ++refreshEpoch
  loading.value = true
  try {
    const next = await authAPI.settings()
    if (current !== lifecycle || epoch !== refreshEpoch) return
    settings.value = next
    if (!usernameDirty.value) username.value = next.username
    loadError.value = ''
    await auth.refresh()
  } catch (e) { if (current === lifecycle && epoch === refreshEpoch) loadError.value = e instanceof Error ? e.message : t('authLoadSettings') }
  finally { if (current === lifecycle && epoch === refreshEpoch) loading.value = false }
}
onMounted(() => { void refresh() })
onActivated(() => { if (settings.value) void refresh() })
onBeforeUnmount(() => {
  lifecycle++; passkeyController?.abort()
  reauth?.reject(new Error(t('authReauthCancelled'))); reauth = undefined
  password.value = ''; confirmPassword.value = ''; recoveryCodes.value = []; setup.value = undefined
})
function requestReauthentication() {
  if (!reauth) {
    let resolve!: () => void
    let reject!: (error: Error) => void
    const promise = new Promise<void>((done, fail) => { resolve = done; reject = fail })
    reauth = { promise, resolve, reject }; reauthOpen.value = true
  }
  return reauth.promise
}
async function recent<T>(fn: () => Promise<T>): Promise<T> {
  try { return await fn() }
  catch (error) {
    if (!(error instanceof AuthError) || error.code !== 'reauth_required') throw error
    await requestReauthentication()
    return fn()
  }
}
function reauthenticated() {
  const current = reauth; reauth = undefined; reauthOpen.value = false
  current?.resolve()
}
function closeReauth(open: boolean) {
  reauthOpen.value = open
  if (!open) { const current = reauth; reauth = undefined; current?.reject(new Error(t('authReauthCancelled'))) }
}
async function saveUsername() {
  if (!settings.value?.configured || accountAction.busy.value || username.value.trim() === settings.value.username) return
  const submitted = username.value.trim()
  const applied = await accountAction.run(async () => {
    const value = submitted
    if (!value) throw new Error(t('authUsernameRequired'))
    if (new TextEncoder().encode(value).byteLength > 128 || /[\r\n\0]/.test(value)) throw new Error(t('authUsernameInvalid'))
    await recent(() => authAPI.account(value))
    await auth.refresh()
    if (username.value.trim() === value) { username.value = value; usernameDirty.value = false }
    if (auth.authenticated) await refresh()
    return true
  })
  if (applied && auth.authenticated && usernameDirty.value && username.value.trim() !== submitted) await saveUsername()
}
async function savePassword() {
  if (accountAction.busy.value || !settings.value) return
  await accountAction.run(async () => {
    if (!username.value.trim()) throw new Error(t('authUsernameRequired'))
    if (!password.value) throw new Error(t('authPasswordRequired'))
    const length = new TextEncoder().encode(password.value).byteLength
    if (length < 12 || length > 1024) throw new Error(t('authPasswordLength'))
    if (new TextEncoder().encode(username.value.trim()).byteLength > 128 || /[\r\n\0]/.test(username.value)) throw new Error(t('authUsernameInvalid'))
    if (password.value !== confirmPassword.value) throw new Error(t('authPasswordMismatch'))
    const name = settings.value!.configured ? settings.value!.username : username.value.trim()
    await recent(() => authAPI.account(name, password.value))
    password.value = ''; confirmPassword.value = ''; changingPassword.value = false; usernameDirty.value = false
    await auth.refresh()
    if (auth.authenticated) await refresh()
  })
}
async function beginTOTP() {
  factorTask.value = 'add'
  await factorAction.run(async () => {
    const next = await recent(authAPI.totpBegin)
    const image = await QRCode.toDataURL(next.uri, { width: 240, margin: 2, color: { dark: '#111111', light: '#ffffff' } })
    setup.value = next; setupCode.value = ''; qrImage.value = image
  })
}
async function confirmTOTP() {
  if (!setup.value) return
  await factorAction.run(async () => {
    const result = await recent(() => authAPI.totpConfirm(setup.value!.setup_id, setupCode.value.trim()))
    setup.value = undefined; setupCode.value = ''; qrImage.value = ''
    recoveryCodes.value = result.codes ?? []
    await refresh()
  })
}
async function removeTOTP() {
  await factorAction.run(async () => {
    await recent(authAPI.totpRemove); confirmRemove.value = ''; await refresh()
  })
}
async function newRecoveryCodes() {
  factorTask.value = 'recovery'
  await factorAction.run(async () => {
    const result = await recent(authAPI.recoveryCodes)
    confirmRemove.value = ''; recoveryCodes.value = result.codes; await refresh()
  })
}
async function addPasskey() {
  if (!canPasskey.value || passkeyAction.busy.value) return
  passkeyTask.value = 'add'
  await passkeyAction.run(async () => {
    passkeyController?.abort(); passkeyController = new AbortController()
    const ceremony = await recent(() => authAPI.passkeyBegin(passkeyName.value.trim()))
    const credential = await createPasskey(ceremony, passkeyController.signal)
    await recent(() => authAPI.passkeyFinish(ceremony.challenge_id, credential))
    passkeyName.value = ''; await refresh()
  })
}
async function removePasskey(key: PasskeyView) {
  passkeyTask.value = 'remove'
  await passkeyAction.run(async () => {
    await recent(() => authAPI.passkeyRemove(key.id)); confirmRemove.value = ''; await refresh()
  })
}
async function setPasswordless(enabled: boolean) {
  passwordlessIntent.value = enabled
  await passwordlessAction.run(async () => {
    try {
      const next = await recent(() => authAPI.passwordless(enabled))
      refreshEpoch++; loading.value = false; settings.value = next
    }
    catch (error) {
      if (!(error instanceof TypeError)) throw error
      const confirmed = await authAPI.settings()
      settings.value = confirmed
      if (confirmed.passwordless !== enabled) throw error
    }
    await auth.refresh()
  })
  passwordlessIntent.value = undefined
}
async function removeSession(session: WebSession) {
  await deviceAction.run(async () => {
    await recent(() => authAPI.revokeSession(session.id)); confirmRemove.value = ''
    if (session.current) await auth.refresh()
    else await refresh()
  })
}
async function removeAllSessions() {
  await deviceAction.run(async () => {
    await recent(authAPI.revokeSessions); confirmRemove.value = ''; await auth.refresh()
    if (auth.authenticated) await refresh()
  })
}
async function logout() { await deviceAction.run(async () => { await authAPI.logout(); await auth.refresh() }) }
function closeSetup(open: boolean) { if (!open && !factorAction.busy.value) { setup.value = undefined; setupCode.value = ''; qrImage.value = '' } }
async function openHTTPS() { if (httpsAddress.value) await window.astralDesktop?.openExternal(httpsAddress.value) }
function lastPasskey(key: PasskeyView) { return settings.value?.passkeys.length === 1 && settings.value.passkeys[0]?.id === key.id }
</script>
<template>
  <template v-if="settings">
    <SettingsSection
      :title="t('authWebAccount')"
      :description="settings.desktop ? t('authWebAccountHint') : undefined"
    >
      <template v-if="settings.configured">
        <SettingsRow
          :label="t('authUsername')"
          :description="t('authUsernameChangedHint')"
        >
          <input
            v-model="username"
            :aria-label="t('authUsername')"
            autocomplete="username"
            @input="usernameDirty = true"
            @blur="saveUsername"
            @keydown.enter.prevent="saveUsername"
          >
        </SettingsRow>
        <SettingsRow
          :label="t('authPassword')"
          :description="t('authPasswordChangedHint')"
        >
          <Button
            :disabled="accountAction.busy.value"
            @click="changingPassword = !changingPassword"
          >
            {{ t(changingPassword ? 'cancel' : 'authChangePassword') }}
          </Button>
        </SettingsRow>
      </template>
      <p
        v-else-if="!settings.desktop"
        class="empty"
      >
        {{ t('authAccountSetupLocal') }}
      </p>
      <form
        v-if="!settings.configured && settings.desktop || changingPassword"
        class="auth-settings-form"
        @submit.prevent="savePassword"
      >
        <SettingsRow
          v-if="!settings.configured"
          :label="t('authUsername')"
        >
          <input
            v-model="username"
            :aria-label="t('authUsername')"
            autocomplete="username"
            required
            @input="usernameDirty = true"
          >
        </SettingsRow>
        <SettingsRow :label="t('authNewPassword')">
          <input
            v-model="password"
            :aria-label="t('authNewPassword')"
            type="password"
            autocomplete="new-password"
            required
          >
        </SettingsRow>
        <SettingsRow :label="t('authConfirmPassword')">
          <input
            v-model="confirmPassword"
            :aria-label="t('authConfirmPassword')"
            type="password"
            autocomplete="new-password"
            required
          >
        </SettingsRow>
        <p
          v-if="accountAction.error.value"
          class="error-box auth-group-error"
          role="alert"
        >
          {{ accountAction.error.value }}
        </p>
        <div class="auth-form-actions">
          <Button
            type="submit"
            variant="primary"
            :busy="accountAction.busy.value"
          >
            {{ t(settings.configured ? 'authChangePassword' : 'authCreateAccount') }}
          </Button>
        </div>
      </form>
      <p
        v-else-if="accountAction.error.value"
        class="error-box auth-group-error"
        role="alert"
      >
        {{ accountAction.error.value }}
      </p>
      <div
        v-if="accountWaiting"
        class="settings-feedback"
        role="status"
      >
        <span
          class="spinner"
          aria-hidden="true"
        /><span class="sr-only">{{ t('settingsSaving') }}</span>
      </div>
    </SettingsSection>

    <SettingsSection
      v-if="settings.configured"
      :title="t('authProtection')"
      :description="t(hasFactors ? 'authProtectionEnabled' : 'authProtectionOff')"
    >
      <SettingsRow
        :label="t('authAuthenticator')"
        :description="t(settings.totp_enabled ? 'authAuthenticatorAdded' : 'authAuthenticatorNotAdded')"
      >
        <ConfirmPopover
          v-if="settings.totp_enabled"
          :open="confirmRemove === 'totp'"
          :title="t(!settings.passkeys?.length ? 'authRemoveLastFactor' : 'authRemoveAuthenticator')"
          :description="t(!settings.passkeys?.length ? 'authRemoveLastFactorHint' : 'authRemoveFactorHint')"
          :confirm-label="t('authRemoveAuthenticator')"
          :cancel-label="t('cancel')"
          :busy="factorAction.busy.value"
          :error="factorAction.error.value"
          @update:open="open => { confirmRemove = open ? 'totp' : ''; factorAction.error.value = '' }"
          @confirm="removeTOTP"
        >
          <template #trigger>
            <Button :disabled="factorAction.busy.value">
              {{ t('authRemoveAuthenticator') }}
            </Button>
          </template>
        </ConfirmPopover>
        <Button
          v-else
          :busy="factorAction.busy.value && factorTask === 'add'"
          :disabled="factorAction.busy.value"
          @click="beginTOTP"
        >
          {{ t('authAddAuthenticator') }}
        </Button>
      </SettingsRow>
      <SettingsRow
        v-if="hasFactors"
        :label="t('authRecoveryCodes')"
        :description="t('authRecoveryRemaining', { count: settings.recovery_codes_remaining })"
      >
        <ConfirmPopover
          :open="confirmRemove === 'recovery'"
          :title="t('authGenerateRecovery')"
          :description="t('authRegenerateHint')"
          :confirm-label="t('authGenerateRecovery')"
          :cancel-label="t('cancel')"
          :busy="factorAction.busy.value"
          :error="factorAction.error.value"
          @update:open="open => { confirmRemove = open ? 'recovery' : ''; factorAction.error.value = '' }"
          @confirm="newRecoveryCodes"
        >
          <template #trigger>
            <Button :disabled="factorAction.busy.value">
              {{ t('authGenerateRecovery') }}
            </Button>
          </template>
        </ConfirmPopover>
      </SettingsRow>
      <p
        v-if="factorAction.error.value && !setup && !confirmRemove"
        class="error-box auth-group-error"
        role="alert"
      >
        {{ factorAction.error.value }}
      </p>
    </SettingsSection>

    <SettingsSection
      v-if="settings.configured"
      :title="t('authPasskeys')"
      class="auth-list-section"
    >
      <div
        v-for="key in settings.passkeys"
        :key="key.id"
        class="resource-row"
      >
        <div class="auth-device-copy">
          <strong>{{ key.name }}</strong><small>{{ key.rp_id }}</small><small>{{ t('authPasskeyCreated', { time: date(key.created_at) }) }}</small><small v-if="key.last_used_at && !key.last_used_at.startsWith('0001-')">{{ t('authPasskeyLastUsed', { time: date(key.last_used_at) }) }}</small>
        </div>
        <ConfirmPopover
          :open="confirmRemove === key.id"
          :title="t(lastPasskey(key) && !settings.totp_enabled ? 'authRemoveLastFactor' : 'authRemovePasskey')"
          :description="[t(lastPasskey(key) && !settings.totp_enabled ? 'authRemoveLastFactorHint' : 'authRemovePasskeyHint'), lastPasskey(key) && settings.passwordless ? t('authLastPasskeyHint') : ''].filter(Boolean).join(' ')"
          :confirm-label="t('authRemovePasskey')"
          :cancel-label="t('cancel')"
          :busy="passkeyAction.busy.value"
          :error="passkeyAction.error.value"
          @update:open="open => { confirmRemove = open ? key.id : ''; passkeyAction.error.value = '' }"
          @confirm="removePasskey(key)"
        >
          <template #trigger>
            <Button
              variant="ghost"
              size="small"
              :disabled="passkeyAction.busy.value"
            >
              {{ t('delete') }}
            </Button>
          </template>
        </ConfirmPopover>
      </div>

      <SettingsRow
        v-if="canPasskey"
        :label="t('authAddPasskey')"
      >
        <form
          class="auth-inline-form"
          @submit.prevent="addPasskey"
        >
          <input
            v-model="passkeyName"
            :aria-label="t('authPasskeyName')"
            :placeholder="t('authPasskeyName')"
            maxlength="100"
            required
          ><Button
            type="submit"
            :busy="passkeyAction.busy.value && passkeyTask === 'add'"
            :disabled="passkeyAction.busy.value"
          >
            {{ t('add') }}
          </Button>
        </form>
      </SettingsRow>
      <SettingsRow
        v-else
        :label="t('authAddPasskey')"
        :description="t('authPasskeyHTTPSHint')"
      >
        <Button
          v-if="settings.desktop && httpsAddress"
          @click="openHTTPS"
        >
          {{ t('authOpenHTTPS') }}
        </Button>
        <span
          v-else-if="settings.desktop"
          class="muted small"
        >{{ t('authNoHTTPSAddress') }}</span>
      </SettingsRow>
      <SettingsRow
        :label="t('authPasswordless')"
        :description="t(settings.passkeys?.length ? 'authPasswordlessHint' : 'authPasswordlessNeedsKey')"
      >
        <Switch
          :model-value="passwordlessIntent ?? settings.passwordless"
          :disabled="!settings.passkeys?.length || passwordlessAction.busy.value"
          :label="t('authPasswordless')"
          @update:model-value="setPasswordless"
        />
      </SettingsRow>
      <div
        v-if="passwordlessWaiting || passwordlessAction.error.value"
        class="settings-feedback"
        :class="{ 'settings-feedback-error': passwordlessAction.error.value }"
        role="status"
      >
        <span
          v-if="passwordlessWaiting"
          class="spinner"
          aria-hidden="true"
        />
        <span v-if="passwordlessAction.error.value">{{ passwordlessAction.error.value }}</span>
      </div>
      <p
        v-if="passkeyAction.error.value && !confirmRemove"
        class="error-box auth-group-error"
        role="alert"
      >
        {{ passkeyAction.error.value }}
      </p>
    </SettingsSection>

    <SettingsSection
      :title="t('authDevices')"
      class="auth-list-section"
      :description="t('authDevicesHint')"
    >
      <div
        v-for="session in settings.sessions"
        :key="session.id"
        class="resource-row"
      >
        <div class="auth-device-copy">
          <strong>{{ sessionDevice(session.user_agent) || t('authUnknownBrowser') }}<span
            v-if="session.current"
            class="muted"
          > · {{ t('authThisDevice') }}</span></strong><small>{{ t('authDeviceCreated', { time: date(session.created_at) }) }}</small><small>{{ t('authDeviceLastActive', { time: date(session.last_used_at) }) }}</small>
        </div>
        <ConfirmPopover
          v-if="session.current"
          :open="confirmRemove === session.id"
          :title="t('authRemoveCurrentDevice')"
          :description="t('authRemoveCurrentHint')"
          :confirm-label="t('authSignOut')"
          :cancel-label="t('cancel')"
          :busy="deviceAction.busy.value"
          :error="deviceAction.error.value"
          @update:open="open => { confirmRemove = open ? session.id : ''; deviceAction.error.value = '' }"
          @confirm="removeSession(session)"
        >
          <template #trigger>
            <Button
              variant="ghost"
              size="small"
              :disabled="deviceAction.busy.value"
            >
              {{ t('authRemoveDevice') }}
            </Button>
          </template>
        </ConfirmPopover>
        <Button
          v-else
          variant="ghost"
          size="small"
          :busy="deviceAction.busy.value"
          @click="removeSession(session)"
        >
          {{ t('authRemoveDevice') }}
        </Button>
      </div>
      <p
        v-if="!settings.sessions?.length"
        class="empty"
      >
        {{ t('authNoDevices') }}
      </p>
      <SettingsRow
        v-if="settings.sessions?.length"
        :label="t('authSignOutAll')"
      >
        <ConfirmPopover
          :open="confirmRemove === 'sessions'"
          :title="t('authSignOutAll')"
          :description="t('authSignOutAllHint')"
          :confirm-label="t('authSignOutAll')"
          :cancel-label="t('cancel')"
          :busy="deviceAction.busy.value"
          :error="deviceAction.error.value"
          @update:open="open => { confirmRemove = open ? 'sessions' : ''; deviceAction.error.value = '' }"
          @confirm="removeAllSessions"
        >
          <template #trigger>
            <Button :disabled="deviceAction.busy.value">
              {{ t('authSignOutAll') }}
            </Button>
          </template>
        </ConfirmPopover>
      </SettingsRow>
      <SettingsRow
        v-if="!settings.desktop"
        :label="t('authSignOut')"
      >
        <Button
          :busy="deviceAction.busy.value"
          @click="logout"
        >
          {{ t('authSignOut') }}
        </Button>
      </SettingsRow>
      <p
        v-if="deviceAction.error.value && !confirmRemove"
        class="error-box auth-group-error"
        role="alert"
      >
        {{ deviceAction.error.value }}
      </p>
    </SettingsSection>
  </template>
  <div
    v-if="waiting || loadError"
    class="settings-feedback"
    role="status"
  >
    <span
      v-if="waiting"
      class="spinner"
      aria-hidden="true"
    />
    <span v-if="loadError">{{ loadError }}</span><button
      v-if="loadError"
      class="text-button"
      @click="refresh"
    >
      {{ t('retry') }}
    </button>
  </div>

  <Modal
    :open="reauthOpen"
    :title="t('authReauthenticate')"
    :description="t('authReauthenticateHint')"
    @update:open="closeReauth"
  >
    <AuthenticationForm
      v-if="reauthOpen"
      reauthenticate
      :username="settings?.username"
      :passwordless="settings?.passwordless"
      :passkey-available="auth.status?.passkey_available"
      @authenticated="reauthenticated"
    />
  </Modal>
  <Modal
    :open="!!setup"
    :title="t('authTOTPSetup')"
    :description="t('authTOTPSetupHint')"
    @update:open="closeSetup"
  >
    <form
      class="stack"
      @submit.prevent="confirmTOTP"
    >
      <img
        v-if="qrImage"
        :src="qrImage"
        :alt="t('authTOTPSetup')"
        class="auth-setup-qr"
      >
      <label class="label">{{ t('authTOTPSecret') }}<code class="auth-secret">{{ setup?.secret }}</code></label>
      <label class="label">{{ t('authTOTPCode') }}<input
        v-model="setupCode"
        inputmode="numeric"
        autocomplete="one-time-code"
        pattern="[0-9]{6}"
        required
        autofocus
      ></label>
      <p
        v-if="factorAction.error.value"
        class="error-box"
        role="alert"
      >
        {{ factorAction.error.value }}
      </p>
      <Button
        type="submit"
        variant="primary"
        :busy="factorAction.busy.value"
      >
        {{ t('authTOTPConfirm') }}
      </Button>
    </form>
  </Modal>
  <Modal
    :open="recoveryCodes.length > 0"
    :title="t('authRecoveryCodes')"
    :description="t('authRecoveryCodesHint')"
    @update:open="open => { if (!open) recoveryCodes = [] }"
  >
    <div class="stack">
      <div class="auth-recovery-codes">
        <code
          v-for="code in recoveryCodes"
          :key="code"
        >{{ code }}</code>
      </div><div class="row between">
        <Button @click="copy(recoveryCodes.join('\n'))">
          {{ t('authCopyCodes') }}
        </Button><Button
          variant="primary"
          @click="recoveryCodes = []"
        >
          {{ t('authCodesSaved') }}
        </Button>
      </div>
    </div>
  </Modal>
</template>
