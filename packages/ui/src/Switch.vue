<script setup lang="ts">
import { SwitchRoot, SwitchThumb } from 'reka-ui'
defineProps<{ modelValue?: boolean; disabled?: boolean; label?: string }>()
defineEmits<{ 'update:modelValue': [value: boolean] }>()
</script>
<template>
  <SwitchRoot
    class="switch"
    :model-value="modelValue"
    :disabled="disabled"
    :aria-label="label"
    @update:model-value="$emit('update:modelValue', $event)"
  >
    <SwitchThumb class="switch-thumb" />
  </SwitchRoot>
</template>
