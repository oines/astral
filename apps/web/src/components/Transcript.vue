<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, useDelayedPending } from '@astral/ui'
import { ArrowDown, Clock3, File, Zap } from '@astral/icons'
import type { Content, Entry, FileRef } from '../lib/types'
import { fileURL, imageURL, object } from '../lib/api'
import { durableTranscriptGroups, externalMessage, liveTranscriptGroups, messageText, scheduledMessage, type SessionState, type TranscriptGroup } from '../lib/session-state'
import { platformLabel } from '../lib/im-platforms'
import { useSurfaceStore } from '../stores/surface'
import Markdown from './Markdown.vue'
import ActivityLine from './ActivityLine.vue'
import TranscriptActions from './TranscriptActions.vue'
import CompactionDivider from './CompactionDivider.vue'
import UserMessageText from './UserMessageText.vue'
import { useTranscriptScroll } from '../composables/useTranscriptScroll'
import { createMessageTimeFormatter } from '../lib/message-time'
import { formatDateTime } from '../lib/timezone'
import { useAppStore } from '../stores/app'
import type { TranscriptPosition } from '../lib/transcript-position'
import { messageAttachments } from '../lib/message-attachments'
import { useImagePreviewStore } from '../stores/image-preview'
import SubagentBubble from './SubagentBubble.vue'

const props = defineProps<{ state: SessionState; sessionID: string; loadOlder: () => Promise<boolean>; loadingHistory?: boolean; readonly?: boolean }>()
const emit = defineEmits<{ fork: [entryID: string]; position: [state: SessionState, position: TranscriptPosition] }>()
const { t, locale } = useI18n()
const surface = useSurfaceStore()
const imagePreview = useImagePreviewStore()
const app = useAppStore()
const scroller = ref<HTMLElement>()
const content = ref<HTMLElement>()
const needsHistory = computed(() => !!props.state.historyCursor && props.state.historyStartTime === undefined)
const waitingHistory = useDelayedPending(needsHistory)
const scroll = useTranscriptScroll({ scroller, content, sessionID: computed(() => props.sessionID), hasMore: computed(() => !!props.state.historyCursor || !!props.state.subagentActivityNextCursor), needsHistory, loadOlder: () => props.loadOlder(), getPosition: () => props.state.scrollPosition, savePosition: position => emit('position', props.state, position) })

const durable = computed(() => {
  // Live deltas touch previews/tools, not this revision or the durable entries.
  void props.state.historyRevision
  return durableTranscriptGroups(props.state)
})
const groups = computed(() => liveTranscriptGroups(props.state, durable.value))
const attachmentCache = new WeakMap<Entry, ReturnType<typeof messageAttachments>>()
const formatTimestamp = computed(() => createMessageTimeFormatter(locale.value, app.displayTimezone))

// Track unseen activity at the tail while the reader is scrolled up.
const unseenNew = ref(false)
const tailSignature = computed(() => {
  const last = groups.value.at(-1)
  if (!last) return ''
  const lastBlock = last.blocks.at(-1)
  const blockLen = lastBlock?.kind === 'text' ? lastBlock.text.length : lastBlock?.kind === 'process' ? lastBlock.items.length : 0
  return `${groups.value.length}:${last.key}:${last.entries.length}:${last.blocks.length}:${blockLen}`
})
const tailStreaming = computed(() => Boolean(groups.value.at(-1)?.active))
const hasNewActivity = computed(() => scroll.showJump.value && (unseenNew.value || tailStreaming.value))

watch(tailSignature, (next, prev) => {
  if (prev && next !== prev && scroll.showJump.value) unseenNew.value = true
})
watch(() => scroll.showJump.value, shown => {
  if (!shown) unseenNew.value = false
})
watch(() => props.sessionID, () => { unseenNew.value = false })

function groupText(group: TranscriptGroup) { return group.entries.map(messageText).filter(text => text.trim()).join('\n\n') }
function groupTime(group: TranscriptGroup) {
  if (group.role === 'assistant') return group.entries.findLast(entry => messageText(entry))?.time
  if (group.role === 'user') {
    const entry = group.entries.at(-1)
    if (!entry || scheduledMessage(entry) || externalMessage(entry)) return undefined
    return entry.time
  }
  return undefined
}
function groupProcessKey(group: TranscriptGroup) {
  const processes = props.state.processes
  if (!processes || group.role !== 'assistant') return ''
  let sig = ''
  for (const block of group.blocks) {
    if (block.kind !== 'process') continue
    for (const item of block.items) {
      if (item.kind !== 'tool') continue
      const execID = item.entry.tool?.invocation.execution_id
      const proc = execID ? processes[execID] : undefined
      if (proc) sig += `${execID}:${proc.running ? 1 : 0}:${proc.exit_code ?? ''};`
    }
  }
  return sig
}
function statusText(group: TranscriptGroup) {
  const params = { ...group.statusParams }
  if (params.time) params.time = formatDateTime(String(params.time), locale.value, app.displayTimezone, { timeStyle: 'medium' })
  return t(group.status ?? '', params)
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
function senderInfo(entry: Entry) {
  const meta = object(entry.message?.metadata)
  if (meta.source !== 'im') return undefined
  const name = String(object(meta.sender).name ?? '').trim()
  if (!name) return undefined
  const platform = typeof meta.platform === 'string' && meta.platform.trim() ? platformLabel(meta.platform.trim(), locale.value) : ''
  return { name, platform }
}
function imageDimensions(image: Content) {
  const metadata = object(image.metadata)
  const width = Number(metadata.width)
  const height = Number(metadata.height)
  return { width: width > 0 ? width : undefined, height: height > 0 ? height : undefined }
}
function scheduledTime(entry: Entry) { return formatTimestamp.value(scheduledMessage(entry)?.deliveredAt, new Date()) }
function externalTime(entry: Entry) { return formatTimestamp.value(externalMessage(entry)?.deliveredAt, new Date()) }
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
        <div
          v-if="waitingHistory && loadingHistory"
          class="activity-line"
          role="status"
        >
          <span
            class="spinner"
            aria-hidden="true"
          />{{ t('loading') }}
        </div>
        <section
          v-for="(group, groupIndex) in groups"
          :key="group.key"
          v-memo="[group, groupIndex === groups.length - 1, locale, app.displayTimezone, groupProcessKey(group)]"
          class="transcript-turn"
          :class="`turn-${group.role}`"
        >
          <CompactionDivider
            v-if="group.role === 'compaction' && group.entries[0]"
            :entry="group.entries[0]"
            :session-i-d="sessionID"
            @expand="scroll.pause"
          />
          <template v-else-if="group.role === 'assistant'">
            <div
              v-for="block in group.blocks"
              :key="block.key"
              :data-scroll-anchor="block.key"
              class="transcript-block"
              :class="{ 'transcript-block-status': block.kind === 'subagent-status' }"
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
                v-else-if="block.kind === 'subagent-status'"
                :activity="block.activity"
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
            v-for="entry in group.role === 'compaction' ? [] : group.entries.filter(entry => group.role === 'user' || attachments(entry).images.length || attachments(entry).files.length)"
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
              v-else-if="externalMessage(entry)"
              class="message-sender scheduled-message-source"
            >
              <Zap aria-hidden="true" />
              <span>{{ t('wakeTask') }}<template v-if="externalMessage(entry)?.title"> · {{ externalMessage(entry)?.title }}</template><template v-if="externalMessage(entry)?.computers.length"> · {{ externalMessage(entry)?.computers.join('、') }}</template></span>
              <time
                v-if="externalTime(entry)"
                :datetime="externalTime(entry)?.dateTime"
                :title="externalTime(entry)?.full"
              >{{ externalTime(entry)?.label }}</time>
            </div>
            <div
              v-else-if="senderInfo(entry)"
              class="message-sender"
            >
              {{ senderInfo(entry)?.name }}<template v-if="senderInfo(entry)?.platform">
                · {{ senderInfo(entry)?.platform }}
              </template>
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
            <UserMessageText
              v-if="group.role === 'user' && messageText(entry).trim()"
              :text="messageText(entry)"
              @expand="scroll.pause"
            />
          </article>
          <TranscriptActions
            v-if="group.role !== 'compaction' && groupText(group) && !group.active"
            :role="group.role"
            :text="groupText(group)"
            :time="groupTime(group)"
            :persistent="group.role === 'assistant' && groupIndex === groups.length - 1"
            :can-fork="group.role === 'assistant' && !readonly"
            @fork="fork(group)"
          />
        </section>
      </div>
    </div>
    <Transition name="float">
      <Button
        v-if="scroll.showJump.value"
        class="scroll-bottom"
        size="icon"
        :aria-label="t(hasNewActivity ? 'jumpToLatestNew' : 'bottom')"
        @click="unseenNew = false; scroll.bottom()"
      >
        <ArrowDown />
        <span
          v-if="hasNewActivity"
          class="scroll-bottom-dot"
          aria-hidden="true"
        />
      </Button>
    </Transition>
  </div>
</template>
