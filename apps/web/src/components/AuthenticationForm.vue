<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@astral/ui'
import { authAPI, AuthError, getPasskey, passkeySupported, type LoginResult } from '../lib/auth'
import { errorText } from '../lib/api'
const props = defineProps<{ reauthenticate?: boolean; username?: string; passwordless?: boolean; passkeyAvailable?: boolean }>()
const emit = defineEmits<{ authenticated: [] }>()
const { t } = useI18n()
const username = ref(props.username || '')
const password = ref('')
const code = ref('')
const codeInput = ref<HTMLInputElement>()
const busy = ref(false)
const error = ref('')
const challenge = ref<LoginResult>()
const method = ref('totp')
watch(() => [challenge.value?.challenge_id, method.value], async () => { await nextTick(); codeInput.value?.focus() })
const canPasskey = computed(() => passkeySupported() && props.passkeyAvailable === true)
let operation = 0
let controller: AbortController | undefined
onBeforeUnmount(() => { operation++; controller?.abort(); password.value = ''; code.value = '' })
function message(e: unknown) {
  if (e instanceof AuthError) {
    if (e.code === 'invalid_credentials') return t('authInvalidCredentials')
    if (e.code === 'rate_limited') return t('authRateLimited')
    if (e.code === 'passkey_unavailable') return t('authPasskeyHTTPS')
    if (e.code === 'challenge_expired') return t('authChallengeExpired')
  }
  if (e instanceof DOMException && ['NotAllowedError', 'AbortError'].includes(e.name)) return t('authCancelled')
  return errorText(e)
}
async function run(fn: () => Promise<LoginResult>) {
  if (busy.value) return
  busy.value = true; error.value = ''
  const current = ++operation
  try {
    const result = await fn()
    if (current !== operation) return
    password.value = ''; code.value = ''
    if (result.authenticated) { emit('authenticated'); return }
    if (!result.challenge_id) throw new Error(t('authChallengeExpired'))
    challenge.value = result
    method.value = result.methods?.includes('totp') ? 'totp' : result.methods?.includes('passkey') ? 'passkey' : 'recovery'
  } catch (e) { if (current === operation) error.value = message(e) }
  finally { if (current === operation) busy.value = false }
}
async function submitPassword() {
  if (!props.reauthenticate && !username.value.trim()) { error.value = t('authUsernameRequired'); return }
  await run(() => props.reauthenticate ? authAPI.reauthenticate(password.value) : authAPI.login(username.value.trim(), password.value))
}
async function submitCode() {
  if (!challenge.value?.challenge_id) return
  await run(() => method.value === 'recovery' ? authAPI.recovery(challenge.value!.challenge_id!, code.value.trim()) : authAPI.totp(challenge.value!.challenge_id!, code.value.trim()))
}
async function passkey() {
  controller?.abort(); controller = new AbortController()
  const signal = controller.signal
  await run(async () => {
    if (props.reauthenticate && !challenge.value) {
      const result = await authAPI.reauthenticatePasskey()
      if (!result.challenge_id) throw new Error(t('authChallengeExpired'))
      challenge.value = result; method.value = 'passkey'
    }
    const ceremony = await authAPI.passkeyLoginBegin(challenge.value?.challenge_id)
    const credential = await getPasskey(ceremony, signal)
    return authAPI.passkeyLoginFinish(ceremony.challenge_id, credential)
  })
}
function back() { challenge.value = undefined; code.value = ''; error.value = '' }
function choose(next: string) { method.value = next; code.value = ''; error.value = '' }
</script>
<template>
  <div class="stack auth-form">
    <form
      v-if="!challenge"
      class="stack"
      @submit.prevent="submitPassword"
    >
      <label
        v-if="!reauthenticate"
        class="label"
      >{{ t('authUsername') }}<input
        v-model="username"
        autocomplete="username"
        required
        autofocus
        :disabled="busy"
      ></label>
      <label class="label">{{ t('authPassword') }}<input
        v-model="password"
        type="password"
        autocomplete="current-password"
        required
        :autofocus="reauthenticate"
        :disabled="busy"
      ></label>
      <p
        v-if="error"
        class="error-box"
        role="alert"
      >
        {{ error }}
      </p>
      <Button
        type="submit"
        variant="primary"
        :busy="busy"
      >
        {{ t(reauthenticate ? 'authContinueAction' : 'authSignInAction') }}
      </Button>
      <Button
        v-if="passwordless && canPasskey"
        :busy="busy"
        @click="passkey"
      >
        {{ t(reauthenticate ? 'authUsePasskey' : 'authPasskeySignIn') }}
      </Button>
    </form>
    <template v-else>
      <h2>{{ t('authSecondStep') }}</h2>
      <form
        v-if="method !== 'passkey'"
        class="stack"
        @submit.prevent="submitCode"
      >
        <label class="label">{{ t(method === 'recovery' ? 'authRecoveryCode' : 'authTOTPCode') }}<input
          ref="codeInput"
          v-model="code"
          :inputmode="method === 'totp' ? 'numeric' : 'text'"
          :autocomplete="method === 'totp' ? 'one-time-code' : 'off'"
          :pattern="method === 'totp' ? '[0-9]{6}' : undefined"
          required
          autofocus
          :disabled="busy"
        ></label>
        <p
          v-if="error"
          class="error-box"
          role="alert"
        >
          {{ error }}
        </p>
        <Button
          type="submit"
          variant="primary"
          :busy="busy"
        >
          {{ t('authVerify') }}
        </Button>
      </form>
      <template v-else>
        <p
          v-if="!canPasskey"
          class="muted"
        >
          {{ t('authPasskeyOnlyHTTP') }}
        </p>
        <p
          v-if="error"
          class="error-box"
          role="alert"
        >
          {{ error }}
        </p>
        <Button
          v-if="canPasskey"
          variant="primary"
          :busy="busy"
          @click="passkey"
        >
          {{ t('authUsePasskey') }}
        </Button>
      </template>
      <div class="auth-methods">
        <button
          v-if="challenge.methods?.includes('totp') && method !== 'totp'"
          class="text-button"
          :disabled="busy"
          @click="choose('totp')"
        >
          {{ t('authUseTOTP') }}
        </button>
        <button
          v-if="challenge.methods?.includes('passkey') && method !== 'passkey' && canPasskey"
          class="text-button"
          :disabled="busy"
          @click="choose('passkey')"
        >
          {{ t('authUsePasskey') }}
        </button>
        <button
          v-if="challenge.methods?.includes('recovery') && method !== 'recovery'"
          class="text-button"
          :disabled="busy"
          @click="choose('recovery')"
        >
          {{ t('authUseRecovery') }}
        </button>
        <button
          class="text-button"
          :disabled="busy"
          @click="back"
        >
          {{ t('authBackToPassword') }}
        </button>
      </div>
    </template>
  </div>
</template>
