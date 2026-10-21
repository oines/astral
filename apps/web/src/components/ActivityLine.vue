<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight } from '@astral/icons'
import { Collapse } from '@astral/ui'
import type { ProcessItem, SessionState } from '../lib/session-state'
import { toolPresentation } from '../lib/tool-presentation'
import ProcessStep from './ProcessStep.vue'
const props = defineProps<{ items: ProcessItem[]; sessionID: string; state: SessionState }>()
const emit = defineEmits<{ expand: [] }>()
const { t } = useI18n()
const open = ref(false)
const toolItems = computed(() => props.items.filter(item => item.kind === 'tool'))
const presentation = computed(() => toolItems.value.length === 1 ? toolPresentation(toolItems.value[0]!.entry) : undefined)
const label = computed(() => {
  if (presentation.value) return presentation.value.label ? t(presentation.value.label) : presentation.value.name
  if (!toolItems.value.length) return t('thinkingProcess')
  const names = toolItems.value.map(item => item.entry.tool!.invocation.call.name)
  return t(names.every(name => name === 'exec_command') ? 'commandsSummary' : names.every(name => name === 'read_file') ? 'filesReadSummary' : 'executionSummary', { count: names.length })
})
function toggle() { emit('expand'); open.value = !open.value }
</script>
<template>
  <div class="activity">
    <ProcessStep
      v-if="items.length === 1"
      :item="items[0]!"
      :session-i-d="sessionID"
      :state="state"
      @expand="emit('expand')"
    />
    <template v-else>
      <button
        class="activity-line"
        :aria-expanded="open"
        @click="toggle"
      >
        <span>{{ label }}</span><span
          v-if="presentation?.target"
          class="tool-target"
        >{{ presentation.target }}</span><ChevronRight
          class="disclosure-chevron"
          :class="{ expanded: open }"
        />
      </button>
      <Collapse :open="open">
        <div class="activity-details">
          <ProcessStep
            v-for="item in items"
            :key="item.key"
            :item="item"
            :session-i-d="sessionID"
            :state="state"
            @expand="emit('expand')"
          />
        </div>
      </Collapse>
    </template>
  </div>
</template>
