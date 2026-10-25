<script setup lang="ts">
import { ref } from 'vue'
import { useClipboard } from '../composables/useClipboard'
import { useI18n } from 'vue-i18n'
import { Button } from '@astral/ui'
import { Check, Copy } from '@astral/icons'
import { errorText } from '../lib/api'
const props = defineProps<{ value?: string; label: string }>()
const { t } = useI18n()
const { copy, copied } = useClipboard()
const error = ref('')
async function copyValue() {
  if (!props.value) return
  error.value = ''
  try { await copy(props.value) } catch (e) { error.value = errorText(e) }
}
</script>
<template>
  <span class="copy-value">
    <span class="copy-value-content">
      <span class="mono">{{ value || '—' }}</span>
      <span
        v-if="error"
        class="text-danger small"
        role="status"
      >{{ error }}</span>
    </span>
    <Button
      v-if="value"
      variant="ghost"
      size="icon"
      :aria-label="`${t(copied ? 'copied' : 'copy')} ${label}`"
      :title="t(copied ? 'copied' : 'copy')"
      @click="copyValue"
    >
      <Check v-if="copied" /><Copy v-else />
    </Button>
  </span>
</template>
