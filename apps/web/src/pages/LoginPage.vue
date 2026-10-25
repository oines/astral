<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { AstralLogo } from '@astral/icons'
import { Button } from '@astral/ui'
import AuthenticationForm from '../components/AuthenticationForm.vue'
import { useAuthStore } from '../stores/auth'
import { useAppStore } from '../stores/app'
const auth = useAuthStore()
const app = useAppStore()
const { t } = useI18n()
async function signedIn() { await auth.refresh(); if (auth.authenticated) await app.refresh().catch(() => undefined) }
</script>
<template>
  <div class="login-screen">
    <div class="login-form stack">
      <AstralLogo lockup />
      <h1>{{ t('authSignIn') }}</h1>
      <template v-if="auth.status?.configured">
        <AuthenticationForm
          :passwordless="auth.status.passwordless"
          :passkey-available="auth.status.passkey_available"
          @authenticated="signedIn"
        />
      </template>
      <template v-else>
        <p>{{ t('authNotConfigured') }}</p>
        <p class="muted">
          {{ t('authSetupHint') }}
        </p>
        <Button @click="auth.refresh">
          {{ t('refresh') }}
        </Button>
      </template>
      <p
        v-if="auth.error"
        class="error-box"
        role="alert"
      >
        {{ auth.error }}
      </p>
    </div>
  </div>
</template>
