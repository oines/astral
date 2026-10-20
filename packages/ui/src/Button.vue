<script setup lang="ts">
import { cva } from 'class-variance-authority'
withDefaults(defineProps<{
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger'
  size?: 'normal' | 'small' | 'icon'
  busy?: boolean
  disabled?: boolean
  type?: 'button' | 'submit' | 'reset'
}>(), { variant: 'secondary', size: 'normal', busy: false, disabled: false, type: 'button' })
const styles = cva('button', {
  variants: {
    variant: { primary: 'button-primary', secondary: 'button-secondary', ghost: 'button-ghost', danger: 'button-danger' },
    size: { normal: '', small: 'button-small', icon: 'button-icon' },
  },
})
</script>
<template>
  <button
    :class="styles({ variant, size })"
    :aria-busy="busy || undefined"
    :disabled="busy || disabled"
    :type="type"
  >
    <span
      v-if="busy"
      class="spinner"
      aria-hidden="true"
    />
    <slot />
  </button>
</template>
