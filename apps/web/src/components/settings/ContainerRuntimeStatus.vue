<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@astral/ui'
import type { AstralInternalDomainHostInfo as HostInfo } from '@astral/sdk'

const props = defineProps<{ host?: HostInfo; checking: boolean; error?: string }>()
const emit = defineEmits<{ check: [] }>()
const { t, locale } = useI18n()
const diagnostic = computed(() => props.error || props.host?.container_error)
const unavailable = computed(() => Boolean(props.error) || props.host?.container_available !== true)
const explanation = computed(() => {
  if (props.error) return t('containerCheck.requestFailed')
  switch (props.host?.container_status) {
    case 'missing_cli': return t('containerCheck.missingCLI')
    case 'not_running': return t('containerCheck.notRunning')
    case 'permission_denied': return t('containerCheck.permissionDenied', { user: props.host.container_user })
    case 'error': return t('containerCheck.failed')
    default: return props.host?.container_error ? t('containerCheck.failed') : ''
  }
})
const guide = computed(() => locale.value.startsWith('zh')
  ? 'https://astral.oines.dev/zh-cn/docs/computers/containers/'
  : 'https://astral.oines.dev/docs/computers/containers/')
function openGuide(event: MouseEvent) {
  if (!window.astralDesktop) return
  event.preventDefault()
  void window.astralDesktop.openExternal(guide.value)
}
</script>
<template>
  <div
    v-if="unavailable"
    class="stack small"
  >
    <p
      v-if="explanation"
      role="status"
    >
      {{ explanation }}
    </p>
    <div class="row">
      <Button
        size="small"
        :busy="checking"
        @click="emit('check')"
      >
        {{ t('containerCheck.checkAgain') }}
      </Button>
      <a
        :href="guide"
        target="_blank"
        rel="noopener noreferrer"
        @click="openGuide"
      >{{ t('containerCheck.setupGuide') }}</a>
    </div>
    <details v-if="diagnostic">
      <summary>{{ t('containerCheck.errorDetails') }}</summary>
      <pre class="connection-command">{{ diagnostic }}</pre>
    </details>
  </div>
</template>
