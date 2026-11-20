<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Entry } from '../lib/types'
import { spawnAgentDetails } from '../lib/tool-presentation'
import { useAppStore } from '../stores/app'
import { useSurfaceStore } from '../stores/surface'

const props = defineProps<{ entry: Entry; sessionID: string }>()
const { t } = useI18n()
const app = useAppStore()
const surface = useSurfaceStore()
const detail = computed(() => spawnAgentDetails(props.entry))
const removed = computed(() => Boolean(detail.value.sessionID) && Boolean(app.deletedSessions?.has(detail.value.sessionID)))
const reasoningLabel = computed(() => {
  const options = detail.value.reasoning
  if (options.mode === 'profile') return t('toolReasoningProfile')
  if (options.mode === 'api') return t('interfaceDefault')
  return [
    options.enabled === undefined ? '' : t(options.enabled ? 'on' : 'off'),
    options.effort,
    options.budget === undefined ? '' : `${t('reasoningBudget')} ${options.budget.toLocaleString()}`,
  ].filter(Boolean).join(' · ')
})

function openChild() {
  if (!detail.value.sessionID || removed.value) return
  const parent = app.sessionViews[props.sessionID] || app.sessions.find(session => session.id === props.sessionID)
  surface.openSubagent(
    { sessionID: detail.value.sessionID, parentSessionID: props.sessionID, title: detail.value.name || t('subagent'), status: 'idle' },
    { id: props.sessionID, title: parent?.title || t('conversation') },
  )
}
</script>
<template>
  <div class="tool-semantic-detail">
    <dl class="tool-facts">
      <template v-if="detail.name">
        <dt>{{ t('toolTargetAgent') }}</dt>
        <dd>
          <button
            v-if="detail.sessionID && !removed"
            type="button"
            class="text-button tool-inline-link"
            @click="openChild"
          >
            {{ detail.name }}
          </button>
          <span v-else>{{ detail.name }}</span>
        </dd>
      </template>
      <dt>{{ t('toolSpawnContext') }}</dt>
      <dd>{{ t(detail.inheritContext ? 'toolSpawnInherit' : 'toolSpawnFresh') }}</dd>
      <template v-if="detail.model">
        <dt>{{ t('model') }}</dt><dd>{{ detail.model }}</dd>
        <dt>{{ t('reasoning') }}</dt><dd>{{ reasoningLabel }}</dd>
      </template>
    </dl>
    <div v-if="detail.task">
      <p class="tool-detail-label">
        {{ t('toolSpawnTask') }}
      </p>
      <p class="tool-detail-text">
        {{ detail.task }}
      </p>
    </div>
    <div v-if="detail.fallback && !detail.error">
      <p class="tool-detail-label">
        {{ t('result') }}
      </p>
      <p class="tool-detail-text">
        {{ detail.fallback }}
      </p>
    </div>
    <p
      v-if="detail.error"
      class="tool-detail-text text-danger"
    >
      {{ detail.error }}
    </p>
  </div>
</template>
