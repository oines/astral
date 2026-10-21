<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight } from '@astral/icons'
import { Collapse } from '@astral/ui'
import type { ProcessItem, SessionState } from '../lib/session-state'
import ToolCall from './ToolCall.vue'
import Markdown from './Markdown.vue'
defineProps<{ item: ProcessItem; sessionID: string; state: SessionState }>()
const emit = defineEmits<{ expand: [] }>()
const { t } = useI18n()
const open = ref(false)
function toggle() { emit('expand'); open.value = !open.value }
</script>
<template>
  <ToolCall
    v-if="item.kind === 'tool'"
    :entry="item.entry"
    :session-i-d="sessionID"
    :live="state.tools[item.entry.tool!.invocation.execution_id]"
    @expand="emit('expand')"
  />
  <div
    v-else
    class="reasoning"
  >
    <button
      class="reasoning-heading"
      :aria-expanded="open"
      @click="toggle"
    >
      {{ t('thinkingProcess') }}<ChevronRight
        class="disclosure-chevron"
        :class="{ expanded: open }"
      />
    </button>
    <Collapse :open="open">
      <Markdown
        :content="item.text"
        :session-i-d="sessionID"
        :streaming="item.streaming"
      />
    </Collapse>
  </div>
</template>
