<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { listProviders, saveProvider, type AstralInternalModelbridgeProviderPreset as Preset } from '@astral/sdk'
import { listProvidersQueryKey } from '@astral/sdk/colada'
import { Button, Select, SettingsRow, SettingsSection } from '@astral/ui'
import { ExternalLink } from '@astral/icons'
import { useI18n } from 'vue-i18n'
import type { Provider } from '../../lib/types'
import { request, newMeta, jsonEqual } from '../../lib/api'
import { useSettingsSave } from '../../composables/useSettingsSave'
import SettingsFeedback from './SettingsFeedback.vue'
import ReasoningFormatSelect from './ReasoningFormatSelect.vue'
const props = defineProps<{ provider?: Provider; preset?: Preset; compact?: boolean }>()
const emit = defineEmits<{ saved: [provider: Provider, autoImport: boolean, origin: string]; cancel: [] }>()
const { t } = useI18n()
const saves = useSettingsSave()
type Fields = { name: string; base_url: string; protocol: string; dialect: string; reasoning_format: string; reasoning_mapping: string; api_key: string; headers: string }
function fields(): Fields { return { name: props.provider?.name || props.preset?.name || '', base_url: props.provider?.base_url || props.preset?.base_url || '', protocol: props.provider?.protocol || props.preset?.protocol || 'openai-completions', dialect: props.provider?.dialect || props.preset?.dialect || 'compatible', reasoning_format: props.provider?.reasoning_format || (props.preset ? '' : props.provider ? '' : 'protocol-default'), reasoning_mapping: JSON.stringify(props.provider?.reasoning_mapping || {}, null, 2), api_key: '', headers: '' } }
const resource = computed(() => `provider:${props.provider?.id || `new:${props.preset?.id || 'custom'}`}`)
const draft = computed(() => saves.form(resource.value, () => { const value = fields(); return { fields: value, baseline: JSON.stringify(value) } }))
const form = computed(() => draft.value.fields)
const autoImport = ref(true)
const dirty = computed(() => JSON.stringify(form.value) !== draft.value.baseline)
watch(() => props.provider, () => {
  if (!dirty.value) { draft.value.fields = fields(); draft.value.baseline = JSON.stringify(draft.value.fields) }
})
const region = computed(() => props.preset?.regions?.find(r => r.base_url === form.value.base_url)?.id || '')
const protocolOptions = [{ value: 'openai-responses', label: 'Responses' }, { value: 'openai-completions', label: 'Chat Completions' }, { value: 'anthropic-messages', label: 'Anthropic Messages' }, { value: 'google-generative-ai', label: 'Google Gemini' }]
const protocols = computed(() => protocolOptions.filter(option => !props.preset?.protocols?.length || props.preset.protocols.includes(option.value) || option.value === form.value.protocol))
function selectProtocol(value: string) {
  form.value.protocol = value
  if (value === 'anthropic-messages') form.value.dialect = 'anthropic'
  else if (value === 'google-generative-ai') form.value.dialect = 'google'
  else if (value === 'openai-responses') form.value.dialect = 'openai'
  else if (['anthropic', 'google'].includes(form.value.dialect)) form.value.dialect = 'compatible'
  form.value.reasoning_format = props.preset ? '' : 'protocol-default'
}
async function submit() {
  const snapshot = { ...form.value }
  const id = props.provider?.id
  const preset = props.preset
  const key = resource.value
  const editing = draft.value
  const importModels = !id && autoImport.value && preset?.auth !== 'oauth'
  let beforeCredential: string | undefined
  let beforeHeaders: string | undefined
  const accept = (saved: Provider) => {
    saves.confirm(listProvidersQueryKey(), saved)
    const confirmed = { ...snapshot, name: saved.name, base_url: saved.base_url, protocol: saved.protocol, dialect: saved.dialect, reasoning_format: saved.reasoning_format || '', reasoning_mapping: JSON.stringify(saved.reasoning_mapping || {}, null, 2), api_key: '', headers: '' }
    for (const field of Object.keys(snapshot) as (keyof Fields)[]) if (editing.fields[field] === snapshot[field]) editing.fields[field] = confirmed[field]
    editing.baseline = JSON.stringify(confirmed)
    if (!id) saves.discardForm(key)
    emit('saved', saved, importModels, key)
  }
  await saves.run(key, 'config', {
    write: async () => {
      let headers: Record<string, string> | undefined
      if (snapshot.headers.trim()) {
        let data: unknown
        try { data = JSON.parse(snapshot.headers) } catch { throw { code: 'invalid', message: t('headersObjectRequired') } }
        if (!data || Array.isArray(data) || typeof data !== 'object' || !Object.values(data).every(x => typeof x === 'string')) throw { code: 'invalid', message: t('headersObjectRequired') }
        headers = data as Record<string, string>
      }
      let mapping: Provider['reasoning_mapping']
      if (snapshot.reasoning_format === 'custom') {
        let parsed: unknown
        try { parsed = JSON.parse(snapshot.reasoning_mapping) } catch { throw { code: 'invalid', message: t('modelObjectRequired') } }
        if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw { code: 'invalid', message: t('modelObjectRequired') }
        mapping = parsed as Provider['reasoning_mapping']
      }
      const current = id ? (await request(listProviders())).find(p => p.id === id) : undefined
      if (id && !current) throw { code: 'not_found', message: t('missing') }
      beforeCredential = current?.credential_id
      beforeHeaders = current?.headers_credential_id
      const saved = await request(saveProvider({ body: { ...(current || newMeta()), preset_id: preset?.id, enabled: current?.enabled ?? true, name: snapshot.name.trim(), base_url: snapshot.base_url.trim(), protocol: snapshot.protocol, dialect: snapshot.dialect, reasoning_format: snapshot.reasoning_format, reasoning_mapping: mapping, ...(snapshot.api_key ? { api_key: snapshot.api_key } : {}), ...(headers ? { headers } : {}) } }))
      accept(saved)
    },
    reconcile: async () => {
      if (!id) return 'unknown'
      const current = (await request(listProviders())).find(p => p.id === id)
      if (!current || current.name !== snapshot.name.trim() || current.base_url !== snapshot.base_url.trim() || current.protocol !== snapshot.protocol || current.dialect !== snapshot.dialect || (current.reasoning_format || '') !== snapshot.reasoning_format) return 'not-applied'
      if (snapshot.reasoning_format === 'custom' && !jsonEqual(current.reasoning_mapping, JSON.parse(snapshot.reasoning_mapping))) return 'not-applied'
      if (snapshot.api_key && current.credential_id === beforeCredential || snapshot.headers.trim() && current.headers_credential_id === beforeHeaders) return 'unknown'
      accept(current)
      return 'applied'
    },
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
        <SettingsRow :label="t('protocol')">
          <Select
            :model-value="form.protocol"
            :label="t('protocol')"
            :options="protocols"
            @update:model-value="selectProtocol"
          />
        </SettingsRow>
        <details
          v-if="!preset"
          class="form-disclosure"
        >
          <summary>{{ t('advancedOptions') }}</summary>
          <div class="stack">
            <SettingsRow :label="t('dialect')">
              <Select
                v-model="form.dialect"
                :label="t('dialect')"
                :options="[{ value: 'compatible', label: 'OpenAI compatible' }, { value: 'openai', label: 'OpenAI' }, { value: 'deepseek', label: 'DeepSeek' }, { value: 'qwen', label: 'Qwen' }, { value: 'openrouter', label: 'OpenRouter' }, { value: 'mimo', label: 'MiMo' }, { value: 'anthropic', label: 'Anthropic' }, { value: 'google', label: 'Google' }]"
              />
            </SettingsRow>
            <SettingsRow :label="t('reasoningRequestFormat')">
              <ReasoningFormatSelect
                v-model="form.reasoning_format"
                :protocol="form.protocol"
                :inherit-label="preset ? t('presetReasoningDefault') : t('legacyReasoningDefault')"
              />
            </SettingsRow>
            <SettingsRow
              v-if="form.reasoning_format === 'custom'"
              :label="t('reasoningParameterMapping')"
            >
              <textarea
                v-model="form.reasoning_mapping"
                class="mono"
                :aria-label="t('reasoningParameterMapping')"
                rows="7"
                :placeholder="t('reasoningMappingExample')"
              />
              <span class="muted small">{{ t('reasoningMappingHint') }}</span>
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
        <div
          v-if="!compact"
          class="settings-submit-row"
        >
          <p>{{ t('connectionDraftHint') }}</p>
          <Button
            type="submit"
            variant="primary"
            :busy="saves.state(`${resource}/config`).status === 'saving'"
            :disabled="!!provider && !dirty"
          >
            {{ preset?.auth === 'oauth' && !provider ? t('continueToSignIn') : t('saveConnectionSettings') }}
          </Button>
        </div>
        <SettingsFeedback
          v-if="!compact"
          :scope="`${resource}/config`"
        />
      </SettingsSection>
    </div>
    <SettingsFeedback
      v-if="compact"
      :scope="`${resource}/config`"
    />
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
        :busy="saves.state(`${resource}/config`).status === 'saving'"
      >
        {{ preset?.auth === 'oauth' ? t('continueToSignIn') : t('saveConnection') }}
      </Button>
    </div>
  </form>
</template>
