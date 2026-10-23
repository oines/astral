<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ChatPage from './pages/ChatPage.vue'
import SettingsPage from './pages/SettingsPage.vue'
import OnboardingPage from './pages/OnboardingPage.vue'
import ChatPageSkeleton from './components/ChatPageSkeleton.vue'
import { useI18n } from 'vue-i18n'
import { login } from '@astral/sdk'
import { Button } from '@astral/ui'
import { ArrowRight, AstralLogo } from '@astral/icons'
import { useAppStore } from './stores/app'
import { request, errorText } from './lib/api'

const app = useAppStore()
const route = useRoute()
const router = useRouter()
const unsubscribeSettings = window.astralDesktop?.onSettings?.(() => { void router.push('/settings/general') })
onBeforeUnmount(() => unsubscribeSettings?.())
const inSettings = computed(() => route.path.startsWith('/settings'))
watch(() => route.path, path => {
  if (!path.startsWith('/settings')) app.lastChatPath = path
}, { immediate: true })
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
      <AstralLogo lockup />
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
  <template v-else-if="app.data">
    <OnboardingPage v-if="app.data.onboarding?.completed === false" />
    <KeepAlive v-else>
      <component :is="inSettings ? SettingsPage : ChatPage" />
    </KeepAlive>
  </template>
  <ChatPageSkeleton
    v-else-if="!inSettings && !app.query.error"
    :sidebar="app.sidebar"
    :welcome="!route.params.id"
  />
  <div
    v-else
    class="login-screen"
  >
    <div class="stack">
      <AstralLogo lockup />
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
