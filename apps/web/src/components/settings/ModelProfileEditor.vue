<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { saveModel, refreshModel, type AstralInternalModelbridgeDiscovery as Discovery } from '@astral/sdk'
import { Button, Modal, Select } from '@astral/ui'
import { useI18n } from 'vue-i18n'
import type { Profile, Provider } from '../../lib/types'
import { clone, newMeta, object, request } from '../../lib/api'
import { useAction } from '../../composables/useAction'
const props = defineProps<{ open: boolean; provider?: Provider; profile?: Profile; suggestion?: Discovery; catalog: Discovery[] }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; saved: [profile: Profile] }>()
const { t } = useI18n()
const action = useAction()
const form = ref<Profile>()
const defaultsJSON = ref('{}')
const reasoningJSON = ref('{}')
const effective = computed(() => ({ ...form.value?.recommendations, ...form.value?.overrides }))
function editableProfile(profile: Profile): Profile {
  return { ...profile, recommendations: object(profile.recommendations), overrides: object(profile.overrides) }
}
function initialize() {
  if (!props.open || !props.provider) return
  form.value = editableProfile(props.profile ? clone(props.profile) : { ...newMeta(), name: '', model_id: '', provider_id: props.provider.id, recommendations: {}, overrides: { max_output_tokens: 4096 } })
  if (props.suggestion) chooseModel(props.suggestion.id)
  syncJSON()
  action.error.value = ''; action.success.value = ''
}
watch(() => props.open, initialize, { immediate: true })
function syncJSON() { defaultsJSON.value = JSON.stringify(effective.value.defaults || {}, null, 2); reasoningJSON.value = JSON.stringify(effective.value.reasoning || {}, null, 2) }
function chooseModel(id: string) {
  const match = props.catalog.find(m => m.id === id) || (props.suggestion?.id === id ? props.suggestion : undefined)
  if (!match || !form.value || props.profile && props.profile.model_id === id) return
  form.value.model_id = id
  if (!form.value.name || form.value.name === props.suggestion?.name) form.value.name = match.name
  form.value.recommendations = clone(match.recommendations)
  form.value.protocol = match.protocol; form.value.dialect = match.dialect
  form.value.catalog_source = match.source; form.value.catalog_at = new Date().toISOString()
  if (!props.profile && match.recommendations.max_output_tokens) delete form.value.overrides.max_output_tokens
  syncJSON()
}
function override(key: string, value: unknown) { if (form.value) form.value.overrides[key] = value }
function reset(key: string) { if (form.value) delete form.value.overrides[key] }
function numeric(key: string, event: Event) { const value = (event.target as HTMLInputElement).value; if (value === '') reset(key); else override(key, Number(value)) }
function capability(key: string, event: Event) { override(key, (event.target as HTMLInputElement).checked ? 'supported' : 'unsupported') }
const reasoning = computed(() => Object.keys(object(effective.value.reasoning)).length > 0)
function toggleReasoning(event: Event) {
  const enabled = (event.target as HTMLInputElement).checked
  if (enabled && form.value?.recommendations.reasoning) reset('reasoning')
  else override('reasoning', enabled ? { toggle: true, efforts: [] } : {})
  syncJSON()
}
async function save() {
  await action.run(async () => {
    if (!form.value) return
    const copy = clone(form.value)
    copy.model_id = copy.model_id.trim(); copy.name = copy.name.trim() || copy.model_id
    for (const [key, json] of [['defaults', defaultsJSON.value], ['reasoning', reasoningJSON.value]]) {
      const parsed: unknown = JSON.parse(json!)
      if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error(t('modelObjectRequired'))
      if (JSON.stringify(parsed) !== JSON.stringify(object(effective.value[key!]))) copy.overrides[key!] = parsed
    }
    const saved = await request(saveModel({ body: copy }))
    emit('saved', saved); emit('update:open', false)
  })
}
async function refresh() {
  await action.run(async () => {
    if (!form.value?.id) return
    form.value = editableProfile(await request(refreshModel({ path: { id: form.value.id } })))
    syncJSON(); emit('saved', form.value)
  })
}
const extraNumbers = [{ key: 'max_input_tokens', label: 'maxInput' }, { key: 'image_token_budget', label: 'imageBudget' }, { key: 'input_price', label: 'inputPrice' }, { key: 'output_price', label: 'outputPrice' }]
</script>
<template>
  <Modal
    :open="open"
    :title="profile ? t('editModel') : t('addModel')"
    class="model-editor"
    @update:open="emit('update:open', $event)"
  >
    <form
      v-if="form"
      class="model-form"
      @submit.prevent="save"
    >
      <div class="model-form-fields">
        <div class="model-field-grid">
          <label class="label">{{ t('modelID') }}<input
            v-model="form.model_id"
            :aria-label="t('modelID')"
            list="provider-model-catalog"
            placeholder="gpt-5.4"
            required
            @change="chooseModel(form.model_id)"
          ><datalist id="provider-model-catalog"><option
            v-for="model in catalog"
            :key="model.id"
            :value="model.id"
          >{{ model.name }}</option></datalist></label>
          <label class="label">{{ t('modelDisplayName') }}<input
            v-model="form.name"
            :aria-label="t('modelDisplayName')"
            :placeholder="form.model_id || t('optional')"
          ></label>
        </div>
        <label class="label">{{ t('modelDescription') }}<textarea
          v-model="form.description"
          :aria-label="t('modelDescription')"
          :placeholder="t('modelDescriptionHint')"
          rows="2"
        /></label>
        <fieldset class="model-compatibility">
          <legend>{{ t('modelCapabilities') }}</legend><div>
            <label class="check"><input
              type="checkbox"
              :checked="effective.images === 'supported'"
              @change="capability('images', $event)"
            >{{ t('vision') }}</label>
            <label class="check"><input
              type="checkbox"
              :checked="effective.tool_calling === 'supported'"
              @change="capability('tool_calling', $event)"
            >{{ t('toolCalling') }}</label>
            <label class="check"><input
              type="checkbox"
              :checked="reasoning"
              @change="toggleReasoning"
            >{{ t('reasoning') }}</label>
          </div>
        </fieldset>
        <div class="model-field-grid">
          <label class="label">{{ t('contextWindow') }}<input
            type="number"
            min="1"
            step="1"
            :aria-label="t('contextWindow')"
            :value="effective.context_window ?? ''"
            placeholder="128000"
            required
            @input="numeric('context_window', $event)"
          ><button
            v-if="'context_window' in form.overrides && 'context_window' in form.recommendations"
            class="text-button"
            type="button"
            @click="reset('context_window')"
          >{{ t('reset') }} · {{ form.recommendations.context_window }}</button></label>
          <label class="label">{{ t('maxOutput') }}<input
            type="number"
            min="1"
            step="1"
            :aria-label="t('maxOutput')"
            :value="effective.max_output_tokens ?? ''"
            required
            @input="numeric('max_output_tokens', $event)"
          ><button
            v-if="'max_output_tokens' in form.overrides && 'max_output_tokens' in form.recommendations"
            class="text-button"
            type="button"
            @click="reset('max_output_tokens')"
          >{{ t('reset') }} · {{ form.recommendations.max_output_tokens }}</button></label>
        </div>
        <details class="form-disclosure">
          <summary>{{ t('advancedOptions') }}</summary>
          <div class="model-advanced-fields">
            <div class="model-field-grid">
              <label
                v-for="field in extraNumbers"
                :key="field.key"
                class="label"
              >{{ t(field.label) }}<input
                type="number"
                min="0"
                step="any"
                :aria-label="t(field.label)"
                :value="effective[field.key] ?? ''"
                @input="numeric(field.key, $event)"
              ><button
                v-if="field.key in form.overrides && field.key in form.recommendations"
                class="text-button"
                type="button"
                @click="reset(field.key)"
              >{{ t('reset') }}</button></label>
              <label class="label">{{ t('protocol') }}<Select
                :model-value="form.protocol || ''"
                :label="t('protocol')"
                :options="[{ value: '', label: t('providerDefault') }, { value: 'openai-completions', label: 'Chat Completions' }, { value: 'openai-responses', label: 'Responses' }, { value: 'anthropic-messages', label: 'Anthropic Messages' }, { value: 'google-generative-ai', label: 'Google Gemini' }]"
                @update:model-value="form.protocol = $event; form.dialect = undefined"
              /></label>
              <label class="label">{{ t('streaming') }}<Select
                :model-value="effective.streaming === false ? 'false' : 'true'"
                :label="t('streaming')"
                :options="[{ value: 'true', label: t('on') }, { value: 'false', label: t('off') }]"
                @update:model-value="override('streaming', $event === 'true')"
              /></label>
            </div>
            <label class="label">{{ t('reasoningCapabilities') }}<textarea
              v-model="reasoningJSON"
              class="mono"
              rows="4"
            /></label>
            <label class="label">{{ t('generationDefaults') }}<textarea
              v-model="defaultsJSON"
              class="mono"
              rows="4"
            /></label>
            <p
              v-if="form.catalog_source"
              class="muted small"
            >
              {{ t('source') }}: {{ form.catalog_source }}
            </p>
            <Button
              v-if="form.id"
              size="small"
              :busy="action.busy.value"
              @click="refresh"
            >
              {{ t('refreshModelDefaults') }}
            </Button>
          </div>
        </details>
      </div>
      <p
        v-if="action.error.value"
        class="error-box form-error"
        role="alert"
      >
        {{ action.error.value }}
      </p>
      <div class="form-footer">
        <Button @click="emit('update:open', false)">
          {{ t('cancel') }}
        </Button><Button
          type="submit"
          variant="primary"
          :busy="action.busy.value"
        >
          {{ profile ? t('confirm') : t('addModel') }}
        </Button>
      </div>
    </form>
  </Modal>
</template>
