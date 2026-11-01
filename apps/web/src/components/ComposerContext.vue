<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@astral/ui'
import type { SessionView } from '../lib/types'

const props = defineProps<{ usage?: SessionView['context_usage']; ratio?: number; disabled: boolean; autofocus?: boolean }>()
const emit = defineEmits<{ compact: [] }>()
const { t } = useI18n()
const panel = ref<HTMLElement>()
onMounted(() => { if (props.autofocus) panel.value?.focus() })
</script>
<template>
  <div
    ref="panel"
    class="context-menu"
    tabindex="-1"
  >
    <strong>{{ t('context') }} <span>{{ ratio === undefined ? '—' : `${ratio}%` }}</span></strong>
    <div class="context-meter">
      <span :style="{ width: `${ratio ?? 0}%` }" />
    </div>
    <p class="muted">
      {{ usage?.input_tokens.toLocaleString() ?? '—' }} / {{ usage?.context_window?.toLocaleString() ?? '—' }} tokens
    </p>
    <p
      v-if="!usage"
      class="muted small"
    >
      {{ t('contextNotReported') }}
    </p>
    <Button
      variant="secondary"
      size="small"
      :disabled="disabled"
      @click="emit('compact')"
    >
      {{ t('compact') }}
    </Button>
  </div>
</template>
