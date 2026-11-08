<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Clock3, Zap } from '@astral/icons'
import type { SessionAutomations } from '../lib/sidebar-groups'

defineProps<{ items: SessionAutomations }>()
const { t } = useI18n()
</script>
<template>
  <span class="session-automations">
    <Clock3
      v-if="items.timed.length"
      :aria-label="t('scheduleTimedTasks')"
    />
    <Zap
      v-if="items.events.length"
      :aria-label="t('wakeEndpoints')"
    />
  </span>
</template>
