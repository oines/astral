<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { saveProvider, type AstralInternalModelbridgeProviderPreset as Preset } from '@astral/sdk'
import { Button, Select, SettingsRow, SettingsSection } from '@astral/ui'
import { ExternalLink } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Provider } from '../../lib/types'
import { request, newMeta } from '../../lib/api'
import { useAction } from '../../composables/useAction'
const props = defineProps<{ provider?: Provider; preset?: Preset; compact?: boolean }>()
const emit = defineEmits<{ saved: [provider: Provider, autoImport: boolean]; cancel: [] }>()
const { t } = useI18n()
const action = useAction()
const form = reactive({ name: '', base_url: '', protocol: 'openai-completions', dialect: 'compatible', api_key: '', headers: '' })
const autoImport = ref(true)
const savedForm = ref('')
watch(() => [props.provider, props.preset], () => {
  Object.assign(form, { name: props.provider?.name || props.preset?.name || '', base_url: props.provider?.base_url || props.preset?.base_url || '', protocol: props.provider?.protocol || props.preset?.protocol || 'openai-completions', dialect: props.provider?.dialect || props.preset?.dialect || 'compatible', api_key: '', headers: '' })
  savedForm.value = JSON.stringify(form)
  action.error.value = ''; action.success.value = ''
}, { immediate: true })
const dirty = computed(() => JSON.stringify(form) !== savedForm.value)
const region = computed(() => props.preset?.regions?.find(r => r.base_url === form.base_url)?.id || '')
const protocols = [{ value: 'openai-completions', label: 'Chat Completions' }, { value: 'openai-responses', label: 'Responses' }, { value: 'anthropic-messages', label: 'Anthropic Messages' }, { value: 'google-generative-ai', label: 'Google Gemini' }]
function selectProtocol(value: string) {
  form.protocol = value
  if (value === 'anthropic-messages') form.dialect = 'anthropic'
  else if (value === 'google-generative-ai') form.dialect = 'google'
  else if (value === 'openai-responses') form.dialect = 'openai'
  else if (['anthropic', 'google'].includes(form.dialect)) form.dialect = 'compatible'
}
async function submit() {
  await action.run(async () => {
    let headers: Record<string, string> | undefined
    if (form.headers.trim()) {
      const data: unknown = JSON.parse(form.headers)
      if (!data || Array.isArray(data) || typeof data !== 'object' || !Object.values(data).every(x => typeof x === 'string')) throw new Error(t('headersObjectRequired'))
      headers = data as Record<string, string>
    }
    const saved = await request(saveProvider({ body: { ...(props.provider || newMeta()), preset_id: props.preset?.id, enabled: props.provider?.enabled ?? true, name: form.name.trim(), base_url: form.base_url.trim(), protocol: form.protocol, dialect: form.dialect, ...(form.api_key ? { api_key: form.api_key } : {}), ...(headers ? { headers } : {}) } }))
    form.api_key = ''; form.headers = ''; savedForm.value = JSON.stringify(form)
    emit('saved', saved, !props.provider && autoImport.value && props.preset?.auth !== 'oauth')
  })
}
</script>
<template>
  <form
    class="settings-form provider-form"
    :class="{ compact }"
    @submit.prevent="submit"
  >
    <div class="provider-form-content">
      <SettingsSection :title="compact ? undefined : t('connectionSettings')">
        <SettingsRow :label="t('name')">
          <input
            v-model="form.name"
            :aria-label="t('name')"
            autocomplete="off"
            required
          >
        </SettingsRow>
        <SettingsRow
          v-if="preset?.regions?.length"
          :label="t('region')"
        >
          <Select
            :model-value="region"
            :label="t('region')"
            :options="preset.regions.map(r => ({ value: r.id, label: t('region_' + r.id) }))"
            @update:model-value="form.base_url = preset?.regions?.find(r => r.id === $event)?.base_url || form.base_url"
          />
        </SettingsRow>
        <SettingsRow :label="t('endpoint')">
          <input
            v-model="form.base_url"
            type="url"
            :aria-label="t('endpoint')"
            :readonly="preset?.auth === 'oauth'"
            :placeholder="preset?.requires_url ? preset.base_url || 'https://api.example.com/v1' : 'https://api.example.com/v1'"
            required
          >
        </SettingsRow>
        <SettingsRow
          v-if="preset?.auth !== 'oauth'"
          :label="t('apiKey')"
        >
          <input
            v-model="form.api_key"
            type="password"
            :aria-label="t('apiKey')"
            autocomplete="new-password"
            :placeholder="provider?.credential_id ? t('storedSecret') : preset?.auth === 'optional_key' ? t('optional') : preset?.key_placeholder || 'sk-…'"
            :required="!provider?.credential_id && !!preset && preset.auth === 'api_key'"
          >
          <a
            v-if="preset?.key_url"
            :href="preset.key_url"
            target="_blank"
            rel="noopener noreferrer"
            class="provider-key-link"
          >{{ t('getAPIKey') }}<ExternalLink /></a>
        </SettingsRow>
        <SettingsRow
          v-if="!preset"
          :label="t('protocol')"
        >
          <Select
            :model-value="form.protocol"
            :label="t('protocol')"
            :options="protocols"
            @update:model-value="selectProtocol"
          />
        </SettingsRow>
        <details
          v-if="preset?.auth !== 'oauth'"
          class="form-disclosure"
        >
          <summary>{{ t('advancedOptions') }}</summary>
          <div class="stack">
            <SettingsRow
              v-if="preset"
              :label="t('protocol')"
            >
              <Select
                :model-value="form.protocol"
                :label="t('protocol')"
                :options="protocols"
                @update:model-value="selectProtocol"
              />
            </SettingsRow>
            <SettingsRow :label="t('dialect')">
              <Select
                v-model="form.dialect"
                :label="t('dialect')"
                :options="[{ value: 'compatible', label: 'OpenAI compatible' }, { value: 'openai', label: 'OpenAI' }, { value: 'deepseek', label: 'DeepSeek' }, { value: 'qwen', label: 'Qwen' }, { value: 'openrouter', label: 'OpenRouter' }, { value: 'mimo', label: 'MiMo' }, { value: 'anthropic', label: 'Anthropic' }, { value: 'google', label: 'Google' }]"
              />
            </SettingsRow>
            <SettingsRow :label="t('extraHeaders')">
              <textarea
                v-model="form.headers"
                :aria-label="t('extraHeaders')"
                rows="3"
                placeholder="{&quot;X-Custom-Header&quot;: &quot;value&quot;}"
              /><span class="muted small">{{ t('extraHeadersHint') }}</span>
            </SettingsRow>
          </div>
        </details>
        <label
          v-if="!provider && preset?.auth !== 'oauth'"
          class="check provider-auto-import"
        ><input
          v-model="autoImport"
          type="checkbox"
        >{{ t('autoImportModels') }}</label>
        <p
          v-if="!compact && action.error.value"
          class="error-box"
          role="alert"
        >
          {{ action.error.value }}
        </p><p
          v-if="action.success.value"
          class="success-note"
          role="status"
        >
          {{ action.success.value }}
        </p>
        <div
          v-if="!compact"
          class="form-footer"
        >
          <Button
            type="submit"
            variant="primary"
            :busy="action.busy.value"
            :disabled="!!provider && !dirty"
          >
            {{ provider ? t('save') : preset?.auth === 'oauth' ? t('continueToSignIn') : t('saveConnection') }}
          </Button>
        </div>
      </SettingsSection>
    </div>
    <p
      v-if="compact && action.error.value"
      class="error-box form-error"
      role="alert"
    >
      {{ action.error.value }}
    </p>
    <div
      v-if="compact"
      class="form-footer"
    >
      <Button @click="emit('cancel')">
        {{ t('cancel') }}
      </Button>
      <Button
        type="submit"
        variant="primary"
        :busy="action.busy.value"
      >
        {{ preset?.auth === 'oauth' ? t('continueToSignIn') : t('saveConnection') }}
      </Button>
    </div>
  </form>
</template>
