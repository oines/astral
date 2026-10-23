<script setup lang="ts">
import { computed } from 'vue'
import { useQuery } from '@pinia/colada'
import { useI18n } from 'vue-i18n'
import { reasoningFormats } from '@astral/sdk'
import { Select } from '@astral/ui'
import { request, errorText } from '../../lib/api'

const props = defineProps<{ modelValue: string; protocol: string; inheritLabel: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useI18n()
const formats = useQuery({ key: () => ['reasoning-formats', props.protocol], query: () => request(reasoningFormats({ query: { protocol: props.protocol } })) })
const options = computed(() => {
  const values = [...(formats.data.value || [])]
  if (props.modelValue && !values.includes(props.modelValue)) values.push(props.modelValue)
  return [{ value: '', label: props.inheritLabel }, ...values.map(value => ({ value, label: t(`reasoningFormat_${value}`) }))]
})
</script>
<template>
  <div class="stack">
    <Select
      :model-value="modelValue"
      :label="t('reasoningRequestFormat')"
      :options="options"
      @update:model-value="emit('update:modelValue', $event)"
    />
    <p
      v-if="formats.error.value"
      class="error-box"
      role="alert"
    >
      {{ errorText(formats.error.value) }}
    </p>
  </div>
</template>
