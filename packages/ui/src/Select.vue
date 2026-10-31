<script setup lang="ts">
import { computed, onDeactivated, ref } from 'vue'
import { Check, ChevronDown } from '@astral/icons'
import { SelectRoot, SelectTrigger, SelectPortal, SelectContent, SelectViewport, SelectItem, SelectItemText, SelectItemIndicator } from 'reka-ui'
defineOptions({ inheritAttrs: false })
const props = withDefaults(defineProps<{
  modelValue?: string
  options: Array<{ value: string; label: string; description?: string; disabled?: boolean }>
  placeholder?: string
  disabled?: boolean
  required?: boolean
  name?: string
  label?: string
}>(), { modelValue: '', placeholder: '', disabled: false, required: false, name: undefined, label: undefined })
const emit = defineEmits<{ 'update:modelValue': [value: string]; closeAutoFocus: [event: Event] }>()
const emptyValue = '__astral_empty_option__'
const selected = computed(() => props.options.find(option => option.value === props.modelValue))
const open = ref(false)
onDeactivated(() => { open.value = false })
</script>
<template>
  <SelectRoot
    v-model:open="open"
    :model-value="modelValue || (options.some(option => option.value === '') ? emptyValue : undefined)"
    :disabled="disabled"
    :required="required"
    :name="name"
    @update:model-value="value => emit('update:modelValue', value === emptyValue ? '' : String(value))"
  >
    <SelectTrigger
      v-bind="$attrs"
      class="select-trigger"
      :aria-label="label"
    >
      <span>{{ selected?.label || placeholder }}</span>
      <ChevronDown aria-hidden="true" />
    </SelectTrigger>
    <SelectPortal>
      <SelectContent
        class="select-content"
        position="popper"
        :side-offset="6"
        :collision-padding="12"
        @close-auto-focus="emit('closeAutoFocus', $event)"
      >
        <SelectViewport>
          <SelectItem
            v-for="option in options"
            :key="option.value"
            :value="option.value || emptyValue"
            :disabled="option.disabled"
            class="select-option"
          >
            <span class="select-option-copy"><SelectItemText>{{ option.label }}</SelectItemText><small v-if="option.description">{{ option.description }}</small></span>
            <SelectItemIndicator class="select-check">
              <Check aria-hidden="true" />
            </SelectItemIndicator>
          </SelectItem>
        </SelectViewport>
      </SelectContent>
    </SelectPortal>
  </SelectRoot>
</template>
