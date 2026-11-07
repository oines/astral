<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, useDelayedPending } from '@astral/ui'
import type { FileRef } from '../../lib/types'
import type { SurfaceTab } from '../../stores/surface'
import { resolveFile } from '../../composables/useFileService'
import { errorText } from '../../lib/api'
import FilePreview from './FilePreview.vue'
import { workspaceTabKeys } from './workspace-context'

const props = defineProps<{ tab: Extract<SurfaceTab, { kind: 'file' }>; computerName: string }>()
const emit = defineEmits<{ resolved: [key: string, file: FileRef] }>()
const keys = inject(workspaceTabKeys, undefined)
const valid = computed(() => !keys || keys.value.has(props.tab.key))
const { t } = useI18n()
const file = ref<FileRef>()
const error = ref('')
const busy = ref(false)
const attempt = ref(0)
const waiting = useDelayedPending(() => busy.value)
watch(() => [props.tab.id, props.tab.sessionID, props.tab.ref, attempt.value, valid.value] as const, async ([id, sessionID, provided, , allowed], _previous, cleanup) => {
  const controller = new AbortController()
  cleanup(() => controller.abort())
  file.value = allowed ? provided : undefined
  error.value = ''
  busy.value = allowed && !provided
  if (!allowed) return
  if (provided) return
  try {
    const resolved = await resolveFile(id, sessionID, controller.signal)
    if (!controller.signal.aborted) { file.value = resolved; emit('resolved', props.tab.key, resolved) }
  } catch (e) { if (!controller.signal.aborted) error.value = errorText(e) }
  finally { if (!controller.signal.aborted) busy.value = false }
}, { immediate: true })
</script>
<template>
  <FilePreview
    v-if="file"
    :file="file"
    :session-i-d="tab.sessionID"
    :computer-name="computerName"
  />
  <div
    v-else
    class="surface-file-status"
  >
    <div
      v-if="error"
      class="stack"
      role="alert"
    >
      <p class="text-danger">
        {{ error }}
      </p>
      <Button
        size="small"
        @click="attempt++"
      >
        {{ t('retry') }}
      </Button>
    </div>
    <span
      v-else-if="waiting"
      class="spinner"
      role="status"
      :aria-label="t('loading')"
    />
  </div>
</template>
<style scoped>
.surface-file-status { padding: 20px; }
</style>
