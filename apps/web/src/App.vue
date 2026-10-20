<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { login } from '@astral/sdk'
import { Button } from '@astral/ui'
import { ArrowRight } from '@astral/icons'
import { useAppStore } from './stores/app'
import { request, errorText } from './lib/api'

const app = useAppStore()
const { t } = useI18n()
const token = ref('')
const busy = ref(false)
const error = ref('')
async function signIn() {
  busy.value = true
  error.value = ''
  try {
    await request(login({ body: { token: token.value.trim() } }))
    token.value = ''
    app.authorized = true
    await app.refresh()
  } catch (e) { error.value = errorText(e) }
  finally { busy.value = false }
}
</script>
<template>
  <div
    v-if="!app.authorized"
    class="login-screen"
  >
    <form
      class="login-form stack"
      @submit.prevent="signIn"
    >
      <span class="wordmark">✳ astral</span>
      <h1>{{ t('signIn') }}</h1>
      <p class="muted">
        {{ t('tokenHint') }}
      </p>
      <label class="label">{{ t('token') }}<input
        v-model="token"
        type="password"
        autocomplete="current-password"
        required
        autofocus
      ></label>
      <p
        v-if="error"
        class="error-box"
      >
        {{ error }}
      </p>
      <Button
        type="submit"
        variant="primary"
        :busy="busy"
      >
        {{ t('connect') }}<ArrowRight />
      </Button>
    </form>
  </div>
  <RouterView v-else-if="app.data" />
  <div
    v-else
    class="login-screen"
  >
    <div class="stack">
      <span class="wordmark">✳ astral</span>
      <p
        v-if="app.query.error"
        class="error-box"
      >
        {{ errorText(app.query.error) }}
      </p>
      <p
        v-else
        class="muted"
      >
        {{ t('connecting') }}
      </p>
      <Button
        v-if="app.query.error"
        @click="app.refresh"
      >
        {{ t('retry') }}
      </Button>
    </div>
  </div>
</template>
