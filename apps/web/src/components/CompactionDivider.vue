<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight } from '@astral/icons'
import { Collapse } from '@astral/ui'
import type { Entry } from '../lib/types'
import { createMessageTimeFormatter } from '../lib/message-time'
import { useSharedNow } from '../composables/useSharedNow'
import { useAppStore } from '../stores/app'
import Markdown from './Markdown.vue'

const props = defineProps<{ entry: Entry; sessionID: string }>()
const emit = defineEmits<{ expand: [] }>()
const { t, locale } = useI18n()
const app = useAppStore()
const now = useSharedNow()
const open = ref(false)
const compaction = computed(() => props.entry.compaction)
const hasDetails = computed(() => Boolean(compaction.value?.summary?.trim() || compaction.value?.instructions?.trim()))
const timestamp = computed(() => createMessageTimeFormatter(locale.value, app.displayTimezone)(props.entry.time, now.value))

function toggle() {
  if (!hasDetails.value) return
  emit('expand')
  open.value = !open.value
}
</script>
<template>
  <div
    v-if="compaction"
    class="compaction-divider"
    :data-scroll-anchor="`compaction:${entry.id}`"
  >
    <div class="compaction-divider-row">
      <span
        class="compaction-line"
        aria-hidden="true"
      />
      <button
        v-if="hasDetails"
        type="button"
        class="compaction-trigger"
        :aria-expanded="open"
        @click="toggle"
      >
        <span>{{ t('compactedContext') }}</span>
        <time
          v-if="timestamp"
          :datetime="timestamp.dateTime"
          :title="timestamp.full"
        >· {{ timestamp.label }}</time>
        <ChevronRight
          class="disclosure-chevron"
          :class="{ expanded: open }"
        />
      </button>
      <span
        v-else
        class="compaction-label"
      >
        <span>{{ t('compactedContext') }}</span>
        <time
          v-if="timestamp"
          :datetime="timestamp.dateTime"
          :title="timestamp.full"
        >· {{ timestamp.label }}</time>
      </span>
      <span
        class="compaction-line"
        aria-hidden="true"
      />
    </div>
    <Collapse
      v-if="hasDetails"
      :open="open"
    >
      <div class="disclosure-body compaction-details">
        <button
          type="button"
          class="disclosure-rail"
          :aria-label="t('collapseDetails')"
          @click="toggle"
        />
        <div class="compaction-details-content">
          <div v-if="compaction.instructions?.trim()">
            <p class="tool-detail-label">
              {{ t('compactionInstructions') }}
            </p>
            <p class="tool-detail-text">
              {{ compaction.instructions }}
            </p>
          </div>
          <div v-if="compaction.summary?.trim()">
            <p
              v-if="compaction.instructions?.trim()"
              class="tool-detail-label"
            >
              {{ t('compactionSummary') }}
            </p>
            <Markdown
              :content="compaction.summary"
              :session-i-d="sessionID"
            />
          </div>
        </div>
      </div>
    </Collapse>
  </div>
</template>
