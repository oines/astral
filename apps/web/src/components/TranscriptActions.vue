<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Popover } from '@astral/ui'
import { Check, Copy, Ellipsis, GitBranch } from '@astral/icons'
import { useClipboard } from '../composables/useClipboard'
import { useSharedNow } from '../composables/useSharedNow'
import { errorText } from '../lib/api'
import { createMessageTimeFormatter } from '../lib/message-time'
import { useAppStore } from '../stores/app'

const props = defineProps<{
  role: 'user' | 'assistant'
  text: string
  time?: string
  persistent?: boolean
  canFork?: boolean
}>()
const emit = defineEmits<{ fork: [] }>()
const { t, locale } = useI18n()
const app = useAppStore()
const now = useSharedNow()
const { copy, copied } = useClipboard()
const copyError = ref('')

const timestamp = computed(() => createMessageTimeFormatter(locale.value, app.displayTimezone)(props.time, now.value))

async function copyText() {
  copyError.value = ''
  try { await copy(props.text) } catch (error) { copyError.value = errorText(error) }
}
</script>
<template>
  <div
    class="message-actions"
    :class="{ persistent }"
  >
    <time
      v-if="role === 'user' && timestamp"
      class="message-time message-time-user"
      :datetime="timestamp.dateTime"
      :title="timestamp.full"
      :aria-label="timestamp.full"
    >{{ timestamp.label }}</time>
    <Button
      variant="ghost"
      size="icon"
      :title="t(copied ? 'copied' : 'copy')"
      :aria-label="t(copied ? 'copied' : 'copy')"
      @click="copyText"
    >
      <Check v-if="copied" /><Copy v-else />
    </Button>
    <Popover
      v-if="canFork"
      :label="t('common.more')"
    >
      <template #trigger>
        <Button
          variant="ghost"
          size="icon"
          :title="t('common.more')"
          :aria-label="t('common.more')"
        >
          <Ellipsis />
        </Button>
      </template>
      <template #default="{ close }">
        <button
          class="menu-item"
          @click="emit('fork'); close()"
        >
          <GitBranch />{{ t('fork') }}
        </button>
      </template>
    </Popover>
    <time
      v-if="role === 'assistant' && timestamp"
      class="message-time"
      :datetime="timestamp.dateTime"
      :title="timestamp.full"
      :aria-label="timestamp.full"
    >{{ timestamp.label }}</time>
  </div>
  <p
    v-if="copyError"
    class="copy-error text-danger"
    role="status"
  >
    {{ copyError }}
  </p>
</template>
