<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ChatPage from './pages/ChatPage.vue'
import SettingsPage from './pages/SettingsPage.vue'
import OnboardingPage from './pages/OnboardingPage.vue'
import ChatPageSkeleton from './components/ChatPageSkeleton.vue'
import ManualCopyDialog from './components/ManualCopyDialog.vue'
import { useI18n } from 'vue-i18n'
import { Button, useDelayedPending } from '@astral/ui'
import { AstralLogo } from '@astral/icons'
import { useAppStore } from './stores/app'
import { errorText } from './lib/api'

import LoginPage from './pages/LoginPage.vue'
import { useAuthStore } from './stores/auth'

const auth = useAuthStore()
const app = useAppStore()
void auth.refresh()
const route = useRoute()
const router = useRouter()
const unsubscribeSettings = window.astralDesktop?.onSettings?.(() => { void router.push('/settings/general') })
onBeforeUnmount(() => unsubscribeSettings?.())
const inSettings = computed(() => route.path.startsWith('/settings'))
const showInitialLoading = useDelayedPending(() => app.authorized && !app.data && !app.query.error)
watch(() => route.path, path => {
  if (!path.startsWith('/settings')) app.lastChatPath = path
}, { immediate: true })
const { t } = useI18n()
</script>
<template>
  <div
    v-if="!auth.ready"
    class="login-screen"
  >
    <div class="login-form stack">
      <AstralLogo lockup />
      <p class="muted">
        {{ auth.error || t('authLoading') }}
      </p>
      <Button
        v-if="auth.error"
        @click="auth.refresh"
      >
        {{ t('retry') }}
      </Button>
    </div>
  </div>
  <LoginPage v-else-if="!app.authorized" />
  <template v-else-if="app.data">
    <OnboardingPage v-if="app.data.onboarding?.completed === false && !inSettings" />
    <KeepAlive v-else>
      <component :is="inSettings ? SettingsPage : ChatPage" />
    </KeepAlive>
  </template>
  <ChatPageSkeleton
    v-else-if="showInitialLoading && !inSettings && !app.query.error"
    :sidebar="app.sidebar"
    :welcome="!route.params.id"
  />
  <div
    v-else-if="app.query.error || showInitialLoading"
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
  <ManualCopyDialog />
</template>
