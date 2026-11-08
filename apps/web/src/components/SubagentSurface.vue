<script setup lang="ts">
import { computed, onActivated, onDeactivated, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, useDelayedPending } from '@astral/ui'
import { useSession } from '../composables/useSession'
import type { SessionState } from '../lib/session-state'
import type { TranscriptPosition } from '../lib/transcript-position'
import Transcript from './Transcript.vue'
import { modelUnavailableLabel } from '../lib/model-availability'

const props = withDefaults(defineProps<{ sessionID: string; viewing?: boolean }>(), { viewing: true })
const { t } = useI18n()
const active = ref(true)
onActivated(() => { active.value = true })
onDeactivated(() => { active.value = false })
const id = computed(() => active.value && props.viewing ? props.sessionID : '')
const live = useSession(id)
const view = computed(() => live.state.value.view)
const error = computed(() => live.error.value || view.value?.error || (view.value?.model_available === false ? modelUnavailableLabel(view.value.model_unavailable_reason, t) : ''))
const waiting = useDelayedPending(() => !view.value && !live.error.value)
function rememberPosition(state: SessionState, position: TranscriptPosition) { state.scrollPosition = position }
</script>
<template>
  <div class="subagent-surface-body">
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
      :loading-history="live.loadingHistory.value"
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
