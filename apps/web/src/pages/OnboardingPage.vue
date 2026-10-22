<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { completeOnboarding } from '@astral/sdk'
import { bootstrapQueryKey, listProvidersQuery } from '@astral/sdk/colada'
import { ArrowLeft, ArrowRight, AstralLogo, Check, Monitor, Plus, Server } from '@astral/icons'
import { Button, Select, SettingsRow, SettingsSection } from '@astral/ui'
import { useAppStore } from '../stores/app'
import { errorText, operatingSystem, request } from '../lib/api'
import ModelsSettings from '../components/settings/ModelsSettings.vue'
import AddComputerDialog from '../components/settings/AddComputerDialog.vue'

const app = useAppStore()
const router = useRouter()
const cache = useQueryCache()
const providers = useQuery(listProvidersQuery())
const { t, locale } = useI18n()
const bot = computed(() => app.agents.find(agent => agent.id === app.data?.onboarding.agent_id))
const storageKey = `astral:setup:${app.data?.public_url}:${bot.value?.id}`
let restored: { step?: number; model?: string; computer?: string } = {}
try {
  const saved = JSON.parse(sessionStorage.getItem(storageKey) || '{}')
  if (saved && typeof saved === 'object') restored = {
    step: saved.step === 2 ? 2 : 1,
    model: typeof saved.model === 'string' ? saved.model : '',
    computer: typeof saved.computer === 'string' ? saved.computer : '',
  }
} catch { /* Start with the saved server configuration. */ }
const step = ref(restored.step === 2 ? 2 : 1)
const modelID = ref(restored.model || bot.value?.default_model_id || '')
const computerID = ref(restored.computer || bot.value?.default_computer_id || '')
const selectedModel = computed(() => app.availableModels.find(model => model.id === modelID.value))
const computers = computed(() => app.computers.filter(computer => !computer.revoked).sort((a, b) => Number(b.online) - Number(a.online) || Number(b.local) - Number(a.local) || a.name.localeCompare(b.name)))
const selectedComputer = computed(() => computers.value.find(computer => computer.id === computerID.value))
const addingComputer = ref(false)
const busy = ref(false)
const error = ref('')
const scroll = ref<HTMLElement>()
const heading = ref<HTMLElement>()
const modelOptions = computed(() => app.availableModels.map(model => ({ value: model.id, label: model.name, description: providers.data.value?.find(provider => provider.id === model.provider_id)?.name })))

watch(() => app.availableModels, models => {
  if (!models.some(model => model.id === modelID.value)) modelID.value = models.length === 1 ? models[0]!.id : ''
  if (!modelID.value) step.value = 1
}, { immediate: true })
watch([step, modelID, computerID], () => {
  sessionStorage.setItem(storageKey, JSON.stringify({ step: step.value, model: modelID.value, computer: computerID.value }))
  error.value = ''
})
watch(step, async () => { await nextTick(); scroll.value?.scrollTo({ top: 0 }); heading.value?.focus() })
async function language(value: string) {
  try { await app.preferences({ language: value }) }
  catch (cause) { error.value = errorText(cause) }
}
async function finish() {
  if (!selectedModel.value || !selectedComputer.value?.online || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const agent = await request(completeOnboarding({ body: { model_id: modelID.value, computer_id: computerID.value } }))
    app.activeAgentID = agent.id
    sessionStorage.removeItem(storageKey)
    await router.replace('/')
    if (app.data) cache.setQueryData(bootstrapQueryKey(), {
      ...app.data,
      agents: app.agents.map(current => current.id === agent.id ? agent : current),
      onboarding: { ...app.data.onboarding, completed: true },
    })
    await app.refresh().catch(() => undefined)
  } catch (cause) {
    // A dropped response may follow a successful atomic commit.
    await app.refresh().catch(() => undefined)
    error.value = errorText(cause)
  } finally { busy.value = false }
}
</script>
<template>
  <div class="onboarding-layout">
    <main
      ref="scroll"
      class="settings-main onboarding-main"
    >
      <div class="settings-content onboarding-content">
        <div class="onboarding-brand">
          <AstralLogo lockup />
          <Select
            :model-value="locale"
            :label="t('language')"
            :options="[{ value: 'zh', label: '简体中文' }, { value: 'en', label: 'English' }]"
            @update:model-value="language"
          />
        </div>
        <header class="onboarding-heading">
          <p class="muted">
            {{ t('onboardingBotReady', { name: bot?.name || 'Astral' }) }}
          </p>
          <h1
            ref="heading"
            tabindex="-1"
          >
            {{ t(step === 1 ? 'onboardingModelTitle' : 'onboardingComputerTitle', { name: bot?.name || 'Astral' }) }}
          </h1>
          <p>{{ t(step === 1 ? 'onboardingModelHint' : 'onboardingComputerHint', { name: bot?.name || 'Astral' }) }}</p>
        </header>
        <nav
          class="onboarding-steps"
          :aria-label="t('onboardingSteps')"
        >
          <button
            :aria-current="step === 1 ? 'step' : undefined"
            @click="step = 1"
          >
            <span><Check v-if="step === 2" /><template v-else>1</template></span>{{ t('model') }}
          </button>
          <ArrowRight aria-hidden="true" />
          <button
            :disabled="!selectedModel"
            :aria-current="step === 2 ? 'step' : undefined"
            @click="step = 2"
          >
            <span>2</span>{{ t('computers') }}
          </button>
        </nav>
        <div
          v-show="step === 1"
          class="stack"
        >
          <SettingsSection v-if="app.availableModels.length">
            <SettingsRow
              :label="t('onboardingModelChoice')"
              :description="t('onboardingDefaultHint', { name: bot?.name || 'Astral' })"
            >
              <Select
                v-model="modelID"
                :label="t('onboardingModelChoice')"
                :placeholder="t('select')"
                :options="modelOptions"
              />
            </SettingsRow>
          </SettingsSection>
          <ModelsSettings embedded />
        </div>
        <div
          v-if="step === 2"
          class="stack"
        >
          <SettingsSection :title="t('onboardingConnectedComputers')">
            <template #actions>
              <Button
                v-if="computers.length"
                size="small"
                @click="addingComputer = true"
              >
                <Plus />{{ t('newComputer') }}
              </Button>
            </template>
            <label
              v-for="computer in computers"
              :key="computer.id"
              class="resource-row onboarding-computer"
              :class="{ selected: computerID === computer.id, offline: !computer.online }"
            >
              <input
                v-model="computerID"
                type="radio"
                name="onboarding-computer"
                :value="computer.id"
                :disabled="!computer.online || busy"
              >
              <span class="resource-symbol"><Server v-if="computer.kind === 'container'" /><Monitor v-else /></span>
              <span><strong>{{ computer.name }}</strong><small>{{ [computer.name !== computer.hostname ? computer.hostname : '', operatingSystem(computer.os), computer.local ? t('onboardingServerMachine') : ''].filter(Boolean).join(' · ') }}</small></span>
              <span
                class="badge"
                :class="{ good: computer.online }"
              >{{ t(computer.online ? 'online' : 'offline') }}</span>
            </label>
            <div
              v-if="!computers.length"
              class="empty-state"
            >
              <Monitor /><strong>{{ t('noComputers') }}</strong><p>{{ t('onboardingAddComputerHint') }}</p>
              <Button
                variant="primary"
                @click="addingComputer = true"
              >
                <Plus />{{ t('newComputer') }}
              </Button>
            </div>
          </SettingsSection>
          <p
            v-if="app.data?.host.local_error"
            class="error-box"
            role="alert"
          >
            {{ app.data.host.local_error }}
          </p>
          <p class="muted onboarding-access">
            {{ t('onboardingComputerAccess', { name: bot?.name || 'Astral' }) }}
          </p>
          <p
            v-if="selectedComputer && !selectedComputer.online"
            class="error-box"
            role="alert"
          >
            {{ t('onboardingComputerOffline') }}
          </p>
        </div>
      </div>
    </main>
    <footer class="onboarding-footer">
      <div class="onboarding-footer-content">
        <p
          v-if="error"
          class="error-box"
          role="alert"
        >
          {{ error }}
        </p>
        <div class="onboarding-actions">
          <Button
            v-if="step === 2"
            :disabled="busy"
            variant="ghost"
            @click="step = 1"
          >
            <ArrowLeft />{{ t('onboardingPrevious') }}
          </Button>
          <p
            v-else
            class="muted"
          >
            {{ selectedModel ? t('onboardingModelSelected', { name: selectedModel.name }) : t('onboardingChooseModelHint') }}
          </p>
          <Button
            v-if="step === 1"
            variant="primary"
            :disabled="!selectedModel"
            @click="step = 2"
          >
            {{ t('onboardingNextComputer') }}<ArrowRight />
          </Button>
          <Button
            v-else
            variant="primary"
            :busy="busy"
            :disabled="!selectedModel || !selectedComputer?.online"
            @click="finish"
          >
            {{ t('onboardingStart') }}<ArrowRight />
          </Button>
        </div>
      </div>
    </footer>
    <AddComputerDialog
      v-model:open="addingComputer"
      :confirm-label="t('onboardingUseComputer')"
      @connected="computerID = $event.id"
    />
  </div>
</template>
