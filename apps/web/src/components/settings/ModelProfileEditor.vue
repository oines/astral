<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { saveModel, describeModel, modelRecommendation, type AstralCoreReasoningOptions as Reasoning, type AstralCoreModelsIntegrationDiscovery as Discovery } from '@astral/sdk'
import { Button, Modal, Select } from '@astral/ui'
import { useI18n } from 'vue-i18n'
import type { Profile, Provider } from '../../lib/types'
import { clone, newMeta, object, request, jsonEqual } from '../../lib/api'
import { useAction } from '../../composables/useAction'
import { modelReasoning, reasoningSelection } from '../../lib/model-reasoning'
import ReasoningFormatSelect from './ReasoningFormatSelect.vue'
const props = defineProps<{ open: boolean; provider?: Provider; profile?: Profile; suggestion?: Discovery; catalog: Discovery[] }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; saved: [profile: Profile] }>()
const { t } = useI18n()
const action = useAction()
const form = ref<Profile>()
const defaultsJSON = ref('{}')
const reasoningJSON = ref('{}')
const mappingJSON = ref('{}')
const lookupBusy = ref(false)
const lookupError = ref('')
const preview = ref<Profile>()
const previewError = ref('')
let previewEpoch = 0
let previewTimer: ReturnType<typeof setTimeout> | undefined
let previewSignature = ''
let lookupEpoch = 0
let lastSuggestedName = ''
let routeExplicit = false
let nameExplicit = false
let pendingLookup: Promise<void> | undefined
const effective = computed(() => ({ ...form.value?.recommendations, ...form.value?.overrides }))
function editableProfile(profile: Profile): Profile {
  return { ...profile, recommendations: object(profile.recommendations), overrides: object(profile.overrides) }
}
function initialize() {
  lookupEpoch++
  lookupBusy.value = false; lookupError.value = ''; lastSuggestedName = ''
  if (!props.open || !props.provider) return
  pendingLookup = undefined
  form.value = editableProfile(props.profile ? clone(props.profile) : { ...newMeta(), name: '', model_id: '', provider_id: props.provider.id, recommendations: {}, overrides: {} })
  routeExplicit = !!(props.profile?.protocol || props.profile?.dialect)
  nameExplicit = !!props.profile?.name
  if (props.suggestion) chooseModel(props.suggestion.id)
  syncJSON()
  action.error.value = ''; action.success.value = ''
}
watch(() => props.open, initialize, { immediate: true })
function syncJSON() { defaultsJSON.value = JSON.stringify(effective.value.defaults || {}, null, 2); reasoningJSON.value = JSON.stringify(effective.value.reasoning || {}, null, 2); mappingJSON.value = JSON.stringify(effective.value.reasoning_mapping || {}, null, 2) }
function chooseModel(id: string) {
  if (!form.value || !props.provider || !id.trim()) return
  const modelID = id.trim()
  const oldValues = effective.value
  const cleanJSON = { defaults: jsonMatches(defaultsJSON.value, oldValues.defaults), reasoning: jsonMatches(reasoningJSON.value, oldValues.reasoning), mapping: jsonMatches(mappingJSON.value, oldValues.reasoning_mapping) }
  form.value.model_id = modelID
  // Until the new ID resolves, no recommendation from the previous identity
  // may be saved under it. Explicit user overrides and edited JSON survive.
  form.value.recommendations = {}; form.value.recommendation_sources = {}; form.value.recommendation_target = undefined
  form.value.catalog_source = ''; form.value.catalog_at = undefined
  if (!routeExplicit) { form.value.protocol = undefined; form.value.dialect = undefined }
  if (cleanJSON.defaults) defaultsJSON.value = JSON.stringify(effective.value.defaults || {}, null, 2)
  if (cleanJSON.reasoning) reasoningJSON.value = JSON.stringify(effective.value.reasoning || {}, null, 2)
  if (cleanJSON.mapping) mappingJSON.value = JSON.stringify(effective.value.reasoning_mapping || {}, null, 2)
  const providerID = props.provider.id
  const previousRoute = { protocol: form.value.protocol, dialect: form.value.dialect }
  const previousJSON = { defaults: defaultsJSON.value, reasoning: reasoningJSON.value, mapping: mappingJSON.value }
  const epoch = ++lookupEpoch
  lookupBusy.value = true; lookupError.value = ''
  pendingLookup = (async () => {
    try {
      const original = props.profile?.model_id === modelID ? { id: modelID, name: props.profile.name, recommendations: props.profile.recommendations, recommendation_target: props.profile.recommendation_target, protocol: props.profile.protocol, dialect: props.profile.dialect, source: props.profile.catalog_source, recommendation_sources: props.profile.recommendation_sources } : undefined
      const match = original || props.catalog.find(m => m.id === modelID) || (props.suggestion?.id === modelID ? props.suggestion : undefined) || await request(modelRecommendation({ path: { id: providerID }, query: { model_id: modelID } }))
      if (epoch !== lookupEpoch || !props.open || !form.value || form.value.model_id.trim() !== modelID || props.provider?.id !== providerID) return
      applyRecommendation(match, modelID, previousJSON, previousRoute)
    } catch (error) {
      if (epoch === lookupEpoch) lookupError.value = error instanceof Error ? error.message : t('recommendationUnavailable')
    } finally {
      if (epoch === lookupEpoch) lookupBusy.value = false
    }
  })()
}
function jsonMatches(text: string, value: unknown) {
  try { return jsonEqual(JSON.parse(text), value || {}) } catch { return false }
}
function applyRecommendation(match: Discovery, id: string, previousJSON: { defaults: string; reasoning: string; mapping: string }, previousRoute: { protocol?: string; dialect?: string }) {
  if (!form.value) return
  form.value.model_id = id
  if (!nameExplicit && (!form.value.name || form.value.name === lastSuggestedName)) form.value.name = match.name
  lastSuggestedName = match.name
  form.value.recommendations = clone(match.recommendations)
  form.value.recommendation_target = match.recommendation_target ? clone(match.recommendation_target) : undefined
  if (!routeExplicit && form.value.protocol === previousRoute.protocol && form.value.dialect === previousRoute.dialect) {
    form.value.protocol = match.protocol; form.value.dialect = match.dialect
  }
  form.value.catalog_source = match.source; form.value.catalog_at = new Date().toISOString()
  form.value.recommendation_sources = clone(match.recommendation_sources || {})
  if (defaultsJSON.value === previousJSON.defaults) defaultsJSON.value = JSON.stringify(effective.value.defaults || {}, null, 2)
  if (reasoningJSON.value === previousJSON.reasoning) reasoningJSON.value = JSON.stringify(effective.value.reasoning || {}, null, 2)
  if (mappingJSON.value === previousJSON.mapping) mappingJSON.value = JSON.stringify(effective.value.reasoning_mapping || {}, null, 2)
}
function override(key: string, value: unknown) { if (form.value) form.value.overrides[key] = value }
function selectProtocol(value: string) {
  if (!form.value) return
  routeExplicit = true
  form.value.protocol = value; form.value.dialect = undefined
}
function reset(key: string) { if (form.value) delete form.value.overrides[key] }
function selectReasoningFormat(value: string) {
  if (value) override('reasoning_format', value)
  else reset('reasoning_format')
  if (value !== 'custom') reset('reasoning_mapping')
}
function numeric(key: string, event: Event) { const value = (event.target as HTMLInputElement).value; if (value === '') reset(key); else override(key, Number(value)) }
function capability(key: string, value: string) {
  if (value === 'auto') reset(key)
  else override(key, value)
}
function capabilityValue(key: string) {
  const value = form.value?.overrides[key]
  return value === 'supported' || value === 'unsupported' || value === 'unknown' ? value : 'auto'
}
function capabilityOptions(key: string) {
  const value = form.value?.recommendations[key]
  const label = value === 'supported' ? t('supported') : value === 'unsupported' ? t('unsupported') : t('unverified')
  return [{ value: 'auto', label: `${t('capabilityAutomatic')} · ${label}` }, { value: 'supported', label: t('supported') }, { value: 'unsupported', label: t('unsupported') }, ...(capabilityValue(key) === 'unknown' ? [{ value: 'unknown', label: t('unverified') }] : [])]
}
function parseObjectJSON(text: string): Record<string, unknown> {
  const parsed: unknown = JSON.parse(text)
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error(t('modelObjectRequired'))
  return parsed as Record<string, unknown>
}
function draftProfile(): Profile {
  if (!form.value) throw new Error(t('modelObjectRequired'))
  const copy = clone(form.value)
  copy.model_id = copy.model_id.trim(); copy.name = copy.name.trim() || copy.model_id
  for (const [key, json] of [['defaults', defaultsJSON.value], ['reasoning', reasoningJSON.value]] as const) {
    const parsed = parseObjectJSON(json)
    if (!jsonEqual(parsed, object(effective.value[key]))) copy.overrides[key] = parsed
  }
  if (effective.value.reasoning_format === 'custom') copy.overrides.reasoning_mapping = parseObjectJSON(mappingJSON.value)
  return copy
}
watch(() => [props.open, JSON.stringify(form.value), defaultsJSON.value, reasoningJSON.value, mappingJSON.value], () => {
  const epoch = ++previewEpoch
  clearTimeout(previewTimer)
  const signature = JSON.stringify([props.provider?.id, form.value?.model_id, form.value?.protocol, form.value?.dialect, effective.value.context_window, effective.value.max_output_tokens, effective.value.reasoning_format, reasoningJSON.value, mappingJSON.value])
  if (signature !== previewSignature) preview.value = undefined
  previewSignature = signature; previewError.value = ''
  if (!props.open || !form.value?.model_id.trim()) return
  previewTimer = setTimeout(async () => {
    try {
      const described = await request(describeModel({ body: draftProfile() }))
      if (epoch === previewEpoch && props.open && form.value) {
        if (!jsonEqual(described.recommendations, form.value.recommendations)) {
          const oldValues = effective.value
          const untouched = { defaults: jsonMatches(defaultsJSON.value, oldValues.defaults), reasoning: jsonMatches(reasoningJSON.value, oldValues.reasoning), mapping: jsonMatches(mappingJSON.value, oldValues.reasoning_mapping) }
          form.value.recommendations = clone(described.recommendations)
          if (untouched.defaults) defaultsJSON.value = JSON.stringify(effective.value.defaults || {}, null, 2)
          if (untouched.reasoning) reasoningJSON.value = JSON.stringify(effective.value.reasoning || {}, null, 2)
          if (untouched.mapping) mappingJSON.value = JSON.stringify(effective.value.reasoning_mapping || {}, null, 2)
        }
        if (!jsonEqual(described.recommendation_target, form.value.recommendation_target)) {
          form.value.recommendation_target = described.recommendation_target ? clone(described.recommendation_target) : undefined
        }
        if (!jsonEqual(described.recommendation_sources || {}, form.value.recommendation_sources || {})) {
          form.value.recommendation_sources = clone(described.recommendation_sources || {})
        }
        preview.value = described
      }
    } catch (error) {
      if (epoch === previewEpoch) previewError.value = error instanceof Error ? error.message : t('recommendationUnavailable')
    }
  }, 250)
}, { immediate: true })
onBeforeUnmount(() => { previewEpoch++; clearTimeout(previewTimer) })
const defaultReasoning = computed<Reasoning>(() => {
  try { return object(parseObjectJSON(defaultsJSON.value).reasoning) as Reasoning } catch { return {} }
})
const defaultReasoningState = computed(() => modelReasoning(preview.value, defaultReasoning.value, t, { inherit: false }))
const defaultReasoningOptions = computed(() => {
  const state = defaultReasoningState.value
  return [...state.options, ...(state.budget ? [{ value: 'budget', label: t('reasoningBudget') }] : []), ...(state.value === 'unavailable' ? [{ value: 'unavailable', label: state.label, disabled: true }] : [])]
})
function setDefaultReasoning(value: Reasoning) {
  try { defaultsJSON.value = JSON.stringify({ ...parseObjectJSON(defaultsJSON.value), reasoning: value }, null, 2) }
  catch (error) { action.error.value = error instanceof Error ? error.message : t('modelObjectRequired') }
}
function chooseDefaultReasoning(value: string) {
  if (value === 'budget') setDefaultReasoning({ budget_tokens: defaultReasoningState.value.budget?.min ?? 0 })
  else {
    const selection = reasoningSelection(value, preview.value)
    if (selection) setDefaultReasoning(selection)
  }
}
function setDefaultBudget(event: Event) {
  const raw = (event.target as HTMLInputElement).value
  if (raw !== '') setDefaultReasoning({ budget_tokens: Number(raw) })
}
async function save() {
  await action.run(async () => {
    await pendingLookup
    if (!form.value) return
    const copy = draftProfile()
    const saved = await request(saveModel({ body: copy }))
    emit('saved', saved); emit('update:open', false)
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
            :placeholder="t('modelID')"
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
            @input="nameExplicit = true"
          ></label>
        </div>
        <p
          v-if="lookupBusy"
          class="muted small"
          role="status"
        >
          {{ t('loadingRecommendations') }}
        </p>
        <p
          v-if="lookupError"
          class="error-box"
          role="alert"
        >
          {{ lookupError }}
        </p>
        <label class="label">{{ t('modelDescription') }}<textarea
          v-model="form.description"
          :aria-label="t('modelDescription')"
          :placeholder="t('modelDescriptionHint')"
          rows="2"
        /></label>
        <fieldset class="model-compatibility">
          <legend>{{ t('modelCapabilities') }}</legend>
          <div class="model-field-grid">
            <label class="label">{{ t('vision') }}<Select
              :model-value="capabilityValue('images')"
              :label="t('vision')"
              :options="capabilityOptions('images')"
              @update:model-value="capability('images', $event)"
            /></label>
            <label class="label">{{ t('toolCalling') }}<Select
              :model-value="capabilityValue('tool_calling')"
              :label="t('toolCalling')"
              :options="capabilityOptions('tool_calling')"
              @update:model-value="capability('tool_calling', $event)"
            /></label>
          </div>
        </fieldset>
        <label class="label">{{ t('defaultReasoning') }}<Select
          :model-value="defaultReasoningState.value"
          :label="t('defaultReasoning')"
          :options="defaultReasoningOptions"
          :placeholder="defaultReasoningState.label"
          :disabled="!defaultReasoningOptions.length"
          @update:model-value="chooseDefaultReasoning"
        /></label>
        <label
          v-if="defaultReasoning.budget_tokens !== undefined"
          class="label"
        >{{ t('reasoningBudget') }}<input
          type="number"
          step="1"
          required
          :aria-label="t('reasoningBudget')"
          :min="defaultReasoningState.budget?.min ?? 0"
          :max="defaultReasoningState.budget?.max"
          :value="defaultReasoning.budget_tokens"
          @change="setDefaultBudget"
        ></label>
        <p
          v-if="previewError || preview?.reasoning_error"
          class="error-box"
          role="alert"
        >
          {{ previewError || preview?.reasoning_error }}
        </p>
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
                @update:model-value="selectProtocol"
              /></label>
              <label
                v-if="!provider?.preset_id"
                class="label"
              >{{ t('reasoningRequestFormat') }}<ReasoningFormatSelect
                :model-value="typeof effective.reasoning_format === 'string' ? effective.reasoning_format : ''"
                :protocol="form.protocol || provider?.protocol || 'openai-completions'"
                :inherit-label="t('providerDefault')"
                @update:model-value="selectReasoningFormat"
              /></label>
              <label class="label">{{ t('streaming') }}<Select
                :model-value="effective.streaming === false ? 'false' : 'true'"
                :label="t('streaming')"
                :options="[{ value: 'true', label: t('on') }, { value: 'false', label: t('off') }]"
                @update:model-value="override('streaming', $event === 'true')"
              /></label>
            </div>
            <label
              v-if="!provider?.preset_id && effective.reasoning_format === 'custom'"
              class="label"
            >{{ t('reasoningParameterMapping') }}<textarea
              v-model="mappingJSON"
              class="mono"
              :aria-label="t('reasoningParameterMapping')"
              rows="7"
              :placeholder="t('reasoningMappingExample')"
            /><span class="muted small">{{ t('reasoningMappingHint') }}</span></label>
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
          :busy="action.busy.value || lookupBusy"
        >
          {{ profile ? t('confirm') : t('addModel') }}
        </Button>
      </div>
    </form>
  </Modal>
</template>

<style scoped>
.model-compatibility > .model-field-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
@media (max-width: 480px) { .model-compatibility > .model-field-grid { grid-template-columns: minmax(0, 1fr); } }
</style>
