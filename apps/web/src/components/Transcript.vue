<script setup lang="ts">
import { computed, ref } from 'vue'
import { useNow } from '@vueuse/core'
import { useClipboard } from '../composables/useClipboard'
import { useI18n } from 'vue-i18n'
import { Button, Popover } from '@astral/ui'
import { ArrowDown, Check, Clock3, Copy, Ellipsis, File, GitBranch } from '@astral/icons'
import type { Content, Entry, FileRef } from '../lib/types'
import { errorText, imageURL, object } from '../lib/api'
import { durableTranscriptGroups, liveTranscriptGroups, messageText, scheduledMessage, type SessionState, type TranscriptGroup } from '../lib/session-state'
import { useSurfaceStore } from '../stores/surface'
import Markdown from './Markdown.vue'
import ActivityLine from './ActivityLine.vue'
import { useTranscriptScroll } from '../composables/useTranscriptScroll'
import { createMessageTimeFormatter } from '../lib/message-time'
import { formatDateTime } from '../lib/timezone'
import { useAppStore } from '../stores/app'
import type { TranscriptPosition } from '../lib/transcript-position'
const props = defineProps<{ state: SessionState; sessionID: string; loadOlder: () => Promise<boolean> }>()
const emit = defineEmits<{ fork: [entryID: string]; position: [state: SessionState, position: TranscriptPosition] }>()
const { t, locale } = useI18n()
const surface = useSurfaceStore()
const app = useAppStore()
const scroller = ref<HTMLElement>()
const content = ref<HTMLElement>()
const scroll = useTranscriptScroll({ scroller, content, sessionID: computed(() => props.sessionID), hasMore: computed(() => !!props.state.historyCursor), loadOlder: () => props.loadOlder(), getPosition: () => props.state.scrollPosition, savePosition: position => emit('position', props.state, position) })
const durable = computed(() => {
  // Live deltas touch previews/tools, not this revision or the durable entries.
  void props.state.historyRevision
  return durableTranscriptGroups(props.state)
})
const groups = computed(() => liveTranscriptGroups(props.state, durable.value))
const now = useNow({ interval: 60_000 })
const formatTimestamp = computed(() => createMessageTimeFormatter(locale.value, app.displayTimezone))
const timestamps = computed(() => Object.fromEntries(groups.value.filter(group => group.role === 'assistant' && !group.active).map(group => [group.key, formatTimestamp.value(group.entries.findLast(entry => messageText(entry))?.time, now.value)])))
const { copy, copied } = useClipboard()
const copiedGroup = ref('')
const copyError = ref('')
function groupText(group: TranscriptGroup) { return group.entries.map(messageText).filter(Boolean).join('\n\n') }
function statusText(group: TranscriptGroup) {
  const params = { ...group.statusParams }
  if (params.time) params.time = formatDateTime(String(params.time), locale.value, app.displayTimezone, { timeStyle: 'medium' })
  return t(group.status ?? '', params)
}
async function copyGroup(group: TranscriptGroup) {
  copiedGroup.value = group.key
  copyError.value = ''
  try { await copy(groupText(group)) } catch (error) { copyError.value = errorText(error) }
}
function fork(group: TranscriptGroup) {
  const entry = group.entries.at(-1)
  if (entry) emit('fork', entry.id)
}
function files(entry: Entry): FileRef[] {
  const raw = object(entry.message?.metadata).files
  return Array.isArray(raw) ? raw as FileRef[] : []
}
function sender(entry: Entry) {
  const meta = object(entry.message?.metadata)
  return meta.source === 'im' ? String(object(meta.sender).name ?? '') : ''
}
function images(entry: Entry) { return (entry.message?.content ?? []).filter(p => p.type === 'image') }
function imageDimensions(image: Content) {
  const metadata = object(image.metadata)
  const width = Number(metadata.width)
  const height = Number(metadata.height)
  return { width: width > 0 ? width : undefined, height: height > 0 ? height : undefined }
}
function scheduledTime(entry: Entry) { return formatTimestamp.value(scheduledMessage(entry)?.deliveredAt, now.value) }
</script>
<template>
  <div class="transcript-viewport">
    <div
      ref="scroller"
      class="transcript-scroll"
      role="region"
      :aria-label="t('conversationMessages')"
      tabindex="0"
      @scroll.passive="scroll.onScroll"
      @scrollend="scroll.onScrollEnd"
      @wheel.passive="scroll.onWheel"
      @touchstart.passive="scroll.onTouchStart"
      @touchmove.passive="scroll.onTouchMove"
      @pointerdown="scroll.onPointerdown"
      @keydown="scroll.onKeydown"
    >
      <div
        ref="content"
        class="transcript"
      >
        <section
          v-for="(group, groupIndex) in groups"
          :key="group.key"
          v-memo="[group, now, locale, app.displayTimezone, copiedGroup, copied, copyError]"
          class="transcript-turn"
          :class="`turn-${group.role}`"
        >
          <template v-if="group.role === 'assistant'">
            <div
              v-for="block in group.blocks"
              :key="block.key"
              :data-scroll-anchor="block.key"
              class="transcript-block"
            >
              <ActivityLine
                v-if="block.kind === 'process'"
                :items="block.items"
                :active="block.active"
                :status="block.active && group.status !== 'working' && group.status !== 'preparing' ? statusText(group) : undefined"
                :session-i-d="sessionID"
                :state="state"
                @expand="scroll.pause"
              />
              <Markdown
                v-else
                class="message message-assistant"
                :content="block.text"
                :streaming="block.streaming"
                :session-i-d="sessionID"
              />
            </div>
            <p
              v-if="group.status && !group.active"
              class="run-status"
              role="status"
            >
              {{ statusText(group) }}
            </p>
          </template>
          <article
            v-for="entry in group.entries.filter(entry => group.role === 'user' || images(entry).length || files(entry).length)"
            :key="entry.id"
            :data-scroll-anchor="entry.id"
            :class="['message', group.role === 'user' ? 'message-user' : 'message-attachments']"
          >
            <div
              v-if="scheduledMessage(entry)"
              class="message-sender scheduled-message-source"
            >
              <Clock3 aria-hidden="true" />
              <span>{{ t('scheduleTask') }}<template v-if="scheduledMessage(entry)?.title"> · {{ scheduledMessage(entry)?.title }}</template></span>
              <time
                v-if="scheduledTime(entry)"
                :datetime="scheduledTime(entry)?.dateTime"
                :title="scheduledTime(entry)?.full"
              >{{ scheduledTime(entry)?.label }}</time>
            </div>
            <div
              v-else-if="sender(entry)"
              class="message-sender"
            >
              {{ sender(entry) }} · Telegram
            </div>
            <div
              v-if="group.role === 'user'"
              class="user-text"
            >
              {{ messageText(entry) }}
            </div>
            <div
              v-if="images(entry).length"
              class="message-images"
            >
              <a
                v-for="(image, index) in images(entry)"
                :key="index"
                :href="imageURL(sessionID, entry.id, index)"
                target="_blank"
                rel="noopener"
              >
                <img
                  :src="imageURL(sessionID, entry.id, index)"
                  :alt="t('historicalImage')"
                  :width="imageDimensions(image).width"
                  :height="imageDimensions(image).height"
                  decoding="async"
                  loading="lazy"
                >
              </a>
            </div>
            <div
              v-if="files(entry).length"
              class="message-files"
            >
              <button
                v-for="file in files(entry)"
                :key="file.id"
                class="file-chip"
                @click="surface.open({ kind: 'file', id: file.id, sessionID, ref: file })"
              >
                <File />{{ file.display_name }}
              </button>
            </div>
          </article>
          <div
            v-if="groupText(group) && !group.active"
            class="message-actions"
            :class="{ persistent: group.role === 'assistant' && groupIndex === groups.length - 1 }"
          >
            <Button
              variant="ghost"
              size="icon"
              :title="t(copied && copiedGroup === group.key ? 'copied' : 'copy')"
              :aria-label="t(copied && copiedGroup === group.key ? 'copied' : 'copy')"
              @click="copyGroup(group)"
            >
              <Check v-if="copied && copiedGroup === group.key" /><Copy v-else />
            </Button>
            <Popover
              v-if="group.role === 'assistant'"
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
                  @click="fork(group); close()"
                >
                  <GitBranch />{{ t('fork') }}
                </button>
              </template>
            </Popover>
            <time
              v-if="group.role === 'assistant' && timestamps[group.key]"
              class="message-time"
              :datetime="timestamps[group.key]?.dateTime"
              :title="timestamps[group.key]?.full"
              :aria-label="timestamps[group.key]?.full"
            >{{ timestamps[group.key]?.label }}</time>
          </div>
          <p
            v-if="copyError && copiedGroup === group.key"
            class="copy-error text-danger"
            role="status"
          >
            {{ copyError }}
          </p>
        </section>
      </div>
    </div>
    <Transition name="float">
      <Button
        v-if="scroll.showJump.value"
        class="scroll-bottom"
        size="icon"
        :aria-label="t('bottom')"
        @click="scroll.bottom"
      >
        <ArrowDown />
      </Button>
    </Transition>
  </div>
</template>
