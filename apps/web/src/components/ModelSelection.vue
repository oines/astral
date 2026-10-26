<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AstralCoreReasoningOptions as Reasoning } from '@astral/sdk'
import { ModelPicker } from '@astral/ui'
import { ChevronDown } from '@astral/icons'
import { useAppStore } from '../stores/app'
import { modelReasoning, reasoningSelection, type ModelSelection } from '../lib/model-reasoning'
import { modelUnavailableLabel } from '../lib/model-availability'

const props = defineProps<{ modelValue: string; reasoning?: Reasoning; label?: string; emptyLabel?: string; busy?: boolean; composer?: boolean; unavailableReason?: string }>()
const emit = defineEmits<{ select: [selection: ModelSelection] }>()
const app = useAppStore()
const { t } = useI18n()
const profile = computed(() => app.models.find(model => model.id === props.modelValue))
const unavailableReason = computed(() => props.modelValue && (props.unavailableReason || !profile.value?.available) ? props.unavailableReason || profile.value?.unavailable_reason || 'model_missing' : '')
const modelName = computed(() => profile.value?.name || (props.modelValue ? t('model') : props.emptyLabel || t('model')))
const modelLabel = computed(() => unavailableReason.value ? `${modelName.value} · ${modelUnavailableLabel(unavailableReason.value, t)}` : modelName.value)
const reasoningState = computed(() => modelReasoning(unavailableReason.value ? undefined : profile.value, props.reasoning, t))
const models = computed(() => {
  const result = app.availableModels.map(model => ({ value: model.id, label: model.name, group: model.provider_id, groupLabel: model.provider_name || '', keywords: model.model_id, disabled: false }))
  if (props.emptyLabel) result.unshift({ value: '', label: props.emptyLabel, group: '', groupLabel: '', keywords: '', disabled: false })
  if (props.modelValue && !result.some(model => model.value === props.modelValue)) result.push({ value: props.modelValue, label: modelLabel.value, group: '', groupLabel: '', keywords: '', disabled: true })
  return result
})
function chooseModel(profile_id: string) {
  if (!props.busy && profile_id !== props.modelValue) emit('select', { profile_id, reasoning: undefined })
}
function chooseReasoning(value: string) {
  if (!props.busy && value !== reasoningState.value.value) emit('select', { profile_id: props.modelValue, reasoning: reasoningSelection(value, profile.value) })
}
function chooseBudget(value: number) {
  if (!props.busy && value !== props.reasoning?.budget_tokens) emit('select', { profile_id: props.modelValue, reasoning: { budget_tokens: value } })
}
</script>
<template>
  <ModelPicker
    :model-value="modelValue"
    :models="models"
    :reasoning-value="reasoningState.value"
    :reasoning-label="reasoningState.label"
    :reasoning-options="reasoningState.options"
    :reasoning-budget="reasoningState.budget"
    :reasoning-budget-value="reasoning?.budget_tokens"
    :labels="{ model: label || t('model'), reasoning: t('reasoning'), search: t('searchModels'), empty: t('noModelMatch'), budget: t('reasoningBudget'), useBudget: t('useReasoningBudget') }"
    :busy="busy"
    :side="composer ? 'top' : 'bottom'"
    :align="composer ? 'end' : 'start'"
    @update:model-value="chooseModel"
    @update:reasoning-value="chooseReasoning"
    @update:reasoning-budget-value="chooseBudget"
  >
    <button
      type="button"
      :class="composer ? 'composer-picker model-picker' : 'select-trigger model-selection-trigger'"
      :aria-label="label || t('model')"
      :title="[modelLabel, ...(reasoningState.options.length ? [reasoningState.label] : []), ...(profile?.reasoning_error ? [profile.reasoning_error] : [])].join(' · ')"
    >
      <span>{{ modelLabel }}</span><small v-if="reasoningState.options.length">· {{ reasoningState.label }}</small><ChevronDown />
    </button>
  </ModelPicker>
</template>
