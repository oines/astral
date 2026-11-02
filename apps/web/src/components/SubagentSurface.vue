<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowLeft, ChevronRight } from '@astral/icons'
import { Button, useDelayedPending } from '@astral/ui'
import { useSession } from '../composables/useSession'
import { useSurfaceStore } from '../stores/surface'
import { useAppStore } from '../stores/app'
import type { SessionState } from '../lib/session-state'
import type { TranscriptPosition } from '../lib/transcript-position'
import { subagentStatus } from '../lib/subagents'
import Transcript from './Transcript.vue'
import { modelUnavailableLabel } from '../lib/model-availability'

const props = defineProps<{ sessionID: string }>()
const { t } = useI18n()
const surface = useSurfaceStore()
const app = useAppStore()
const id = computed(() => props.sessionID)
const live = useSession(id)
const view = computed(() => live.state.value.view)
const error = computed(() => live.error.value || view.value?.error || (view.value?.model_available === false ? modelUnavailableLabel(view.value.model_unavailable_reason, t) : ''))
const path = computed(() => surface.current?.kind === 'subagent' ? surface.current.path : [])
const waiting = useDelayedPending(() => !view.value && !live.error.value)
const status = computed(() => subagentStatus(view.value?.error ? 'failed' : view.value?.status))
function title(session: { id: string; title: string }) {
  return app.sessionViews[session.id]?.title || app.sessions.find(value => value.id === session.id)?.title || session.title || t('subagent')
}
function rememberPosition(state: SessionState, position: TranscriptPosition) { state.scrollPosition = position }
</script>
<template>
  <div class="subagent-surface-body">
    <nav
      class="subagent-navigation"
      :aria-label="t('subagentNavigation')"
    >
      <Button
        variant="ghost"
        size="icon"
        :aria-label="t('subagentBack')"
        @click="surface.backToSession(Math.max(0, path.length - 2))"
      >
        <ArrowLeft />
      </Button>
      <ol>
        <li
          v-for="(session, index) in path"
          :key="session.id"
        >
          <ChevronRight
            v-if="index"
            aria-hidden="true"
          />
          <span
            v-if="index === path.length - 1"
            aria-current="page"
          >{{ title(session) }}</span>
          <button
            v-else
            type="button"
            @click="surface.backToSession(index)"
          >
            {{ title(session) }}
          </button>
        </li>
      </ol>
    </nav>
    <p
      v-if="view"
      class="subagent-surface-status muted small"
      role="status"
    >
      {{ t(status) }}
    </p>
    <div
      v-if="error"
      class="subagent-surface-error"
      role="alert"
    >
      <p class="text-danger">
        {{ error }}
      </p>
      <Button
        v-if="live.error.value"
        size="small"
        @click="live.reconnect"
      >
        {{ t('retry') }}
      </Button>
    </div>
    <Transcript
      v-if="view"
      :key="sessionID"
      :state="live.state.value"
      :session-i-d="sessionID"
      :load-older="live.older"
      readonly
      @position="rememberPosition"
    />
    <div
      v-else-if="waiting"
      class="subagent-surface-loading"
      role="status"
      :aria-label="t('loading')"
    >
      <span class="spinner" />
    </div>
  </div>
</template>
