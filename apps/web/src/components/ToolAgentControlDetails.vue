<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Entry } from '../lib/types'
import { object } from '../lib/api'
import { agentResultStatus, controlledAgentDetails, reasoningDetails } from '../lib/tool-presentation'

const props = defineProps<{ entry: Entry }>()
const { t } = useI18n()
const detail = computed(() => controlledAgentDetails(props.entry))
function reasoning(value: unknown, supplied: boolean) {
  const options = reasoningDetails(value, supplied)
  if (options.mode === 'profile') return t('toolReasoningProfile')
  if (options.mode === 'api') return t('interfaceDefault')
  return [options.enabled === undefined ? '' : t(options.enabled ? 'on' : 'off'), options.effort, options.budget === undefined ? '' : `${t('reasoningBudget')} ${options.budget.toLocaleString()}`].filter(Boolean).join(' · ')
}
function status(agent: Record<string, unknown>) {
  const value = agentResultStatus(agent)
  return value.key ? t(value.key) : value.value
}
</script>
<template>
  <div class="tool-semantic-detail">
    <dl class="tool-facts">
      <dt>{{ t('toolAction') }}</dt><dd>{{ detail.actions.map(action => t(action)).join(' · ') }}</dd>
      <template v-if="detail.targetID">
        <dt>{{ t('toolTargetAgent') }}</dt><dd>{{ detail.targetName || detail.targetID }}</dd>
      </template>
      <template v-if="detail.model">
        <dt>{{ t('model') }}</dt><dd>{{ detail.model }}</dd>
        <dt>{{ t('reasoning') }}</dt><dd>{{ reasoning(object(entry.tool?.invocation.call.arguments).reasoning, Object.hasOwn(object(entry.tool?.invocation.call.arguments), 'reasoning')) }}</dd>
      </template>
    </dl>
    <div v-if="detail.input">
      <p class="tool-detail-label">
        {{ t('toolAgentTask') }}
      </p>
      <p class="tool-detail-text">
        {{ detail.input }}
      </p>
    </div>
    <div v-if="detail.agents.length || detail.emptyList">
      <p class="tool-detail-label">
        {{ t('result') }}
      </p>
      <p v-if="detail.emptyList">
        {{ t('toolNoAgents') }}
      </p>
      <div
        v-for="agent in detail.agents"
        :key="String(agent.session_id)"
        class="tool-agent-result"
      >
        <span v-if="!detail.targetID || detail.agents.length > 1">{{ agent.name || agent.title || agent.session_id }}</span>
        <span
          v-if="status(agent)"
          class="tool-detail-secondary"
        >{{ status(agent) }}</span>
        <dl
          v-if="object(agent.selection).id"
          class="tool-facts"
        >
          <dt>{{ t('model') }}</dt><dd>{{ object(agent.selection).id }}</dd>
          <dt>{{ t('reasoning') }}</dt><dd>{{ reasoning(object(agent.selection).reasoning, Object.hasOwn(object(agent.selection), 'reasoning')) }}</dd>
        </dl>
        <p
          v-if="agent.error"
          class="tool-detail-text text-danger"
        >
          {{ agent.error }}
        </p>
      </div>
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
    <details
      v-if="detail.targetName || (!detail.targetID && detail.agents.some(agent => agent.name || agent.title))"
      class="tool-identifiers"
    >
      <summary>{{ t('toolIdentifiers') }}</summary>
      <dl class="tool-facts">
        <template v-if="detail.targetID && !detail.agents.length">
          <dt>{{ t('toolTargetAgent') }}</dt><dd>{{ detail.targetID }}</dd>
        </template>
        <template
          v-for="agent in detail.agents"
          :key="String(agent.session_id)"
        >
          <dt>{{ agent.name || agent.title || t('subagent') }}</dt><dd>{{ agent.session_id }}</dd>
        </template>
      </dl>
    </details>
  </div>
</template>
