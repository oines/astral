<script setup lang="ts">
import { computed, ref } from 'vue'
import { useNow } from '@vueuse/core'
import { useClipboard } from '../composables/useClipboard'
import { useI18n } from 'vue-i18n'
import { Button, Popover } from '@astral/ui'
import { ArrowDown, Check, Clock3, Copy, Ellipsis, File, GitBranch } from '@astral/icons'
import type { Content, Entry, FileRef } from '../lib/types'
import { errorText, fileURL, imageURL, object } from '../lib/api'
import { durableTranscriptGroups, liveTranscriptGroups, messageText, scheduledMessage, type SessionState, type TranscriptGroup } from '../lib/session-state'
import { useSurfaceStore } from '../stores/surface'
import Markdown from './Markdown.vue'
import ActivityLine from './ActivityLine.vue'
import { useTranscriptScroll } from '../composables/useTranscriptScroll'
import { createMessageTimeFormatter } from '../lib/message-time'
import { formatDateTime } from '../lib/timezone'
import { useAppStore } from '../stores/app'
import type { TranscriptPosition } from '../lib/transcript-position'
import { messageAttachments } from '../lib/message-attachments'
import { useImagePreviewStore } from '../stores/image-preview'
import SubagentBubble from './SubagentBubble.vue'
const props = defineProps<{ state: SessionState; sessionID: string; loadOlder: () => Promise<boolean>; readonly?: boolean }>()
const emit = defineEmits<{ fork: [entryID: string]; position: [state: SessionState, position: TranscriptPosition] }>()
const { t, locale } = useI18n()
const surface = useSurfaceStore()
const imagePreview = useImagePreviewStore()
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
const attachmentCache = new WeakMap<Entry, ReturnType<typeof messageAttachments>>()
const now = useNow({ interval: 60_000 })
const formatTimestamp = computed(() => createMessageTimeFormatter(locale.value, app.displayTimezone))
const timestamps = computed(() => Object.fromEntries(groups.value.filter(group => group.role === 'assistant' && !group.active).map(group => [group.key, formatTimestamp.value(group.entries.findLast(entry => messageText(entry))?.time, now.value)])))
const { copy, copied } = useClipboard()
const copiedGroup = ref('')
const copyError = ref('')
function groupText(group: TranscriptGroup) { return group.entries.map(messageText).filter(text => text.trim()).join('\n\n') }
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
function attachments(entry: Entry) {
  let result = attachmentCache.get(entry)
  if (!result) {
    result = messageAttachments(entry)
    attachmentCache.set(entry, result)
  }
  return result
}
function openFile(file: FileRef) {
  if (file.media_type.startsWith('image/')) {
    imagePreview.show([{ src: fileURL(file.id, props.sessionID), downloadUrl: fileURL(file.id, props.sessionID, true), name: file.display_name }])
  } else surface.open({ kind: 'file', id: file.id, sessionID: props.sessionID, ref: file })
}
function openImage(entry: Entry, index: number) {
  imagePreview.show(attachments(entry).images.map(image => ({
    src: imageURL(props.sessionID, entry.id, image.index),
    originalSrc: image.file ? fileURL(image.file.id, props.sessionID) : undefined,
    downloadUrl: image.file ? fileURL(image.file.id, props.sessionID, true) : imageURL(props.sessionID, entry.id, image.index),
    name: image.file?.display_name || t('imagePreviewTitle'),
  })), index)
}
function sender(entry: Entry) {
  const meta = object(entry.message?.metadata)
  return meta.source === 'im' ? String(object(meta.sender).name ?? '') : ''
}
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
                v-else-if="block.kind === 'text'"
                class="message message-assistant"
                :content="block.text"
                :streaming="block.streaming"
                :session-i-d="sessionID"
              />
              <SubagentBubble
                v-else-if="block.kind === 'subagent'"
                :entry="block.entry"
                :session-i-d="sessionID"
                @open="scroll.pause"
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
            v-for="entry in group.entries.filter(entry => group.role === 'user' || attachments(entry).images.length || attachments(entry).files.length)"
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
              v-if="attachments(entry).images.length"
              class="message-images"
              :class="{ 'message-images-multiple': attachments(entry).images.length > 1 }"
            >
              <div
                v-for="image in attachments(entry).images"
                :key="image.index"
                class="message-image"
              >
                <button
                  type="button"
                  class="message-image-trigger"
                  :aria-label="t('previewImage', { name: image.file?.display_name || t('imagePreviewTitle') })"
                  @click="openImage(entry, image.index)"
                >
                  <img
                    :src="imageURL(sessionID, entry.id, image.index)"
                    :alt="image.file?.display_name || t('historicalImage')"
                    :width="imageDimensions(image.content).width"
                    :height="imageDimensions(image.content).height"
                    decoding="async"
                    loading="lazy"
                  >
                </button>
              </div>
            </div>
            <div
              v-if="attachments(entry).files.length"
              class="message-files"
            >
              <button
                v-for="file in attachments(entry).files"
                :key="file.id"
                type="button"
                class="file-chip"
                :title="file.display_name"
                @click="openFile(file)"
              >
                <File /><span>{{ file.display_name }}</span>
              </button>
            </div>
            <div
              v-if="group.role === 'user' && messageText(entry).trim()"
              class="user-text"
            >
              {{ messageText(entry) }}
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
              v-if="group.role === 'assistant' && !readonly"
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
