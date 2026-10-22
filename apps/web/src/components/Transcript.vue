<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useClipboard, useResizeObserver } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { Button, Popover } from '@astral/ui'
import { ArrowDown, Check, Copy, Ellipsis, File, GitBranch } from '@astral/icons'
import type { Entry, FileRef } from '../lib/types'
import { errorText, imageURL, object } from '../lib/api'
import { messageText, transcriptGroups, type SessionState, type TranscriptGroup } from '../lib/session-state'
import { useSurfaceStore } from '../stores/surface'
import Markdown from './Markdown.vue'
import ActivityLine from './ActivityLine.vue'
const props = defineProps<{ state: SessionState; sessionID: string; loadingHistory: boolean }>()
const emit = defineEmits<{ older: []; fork: [entryID: string] }>()
const { t, locale } = useI18n()
const surface = useSurfaceStore()
const scroller = ref<HTMLElement>()
const content = ref<HTMLElement>()
const stick = ref(true)
const groups = computed(() => transcriptGroups(props.state))
const { copy, copied } = useClipboard({ legacy: true })
const copiedGroup = ref('')
const copyError = ref('')
function groupText(group: TranscriptGroup) { return group.entries.map(messageText).filter(Boolean).join('\n\n') }
function statusText(group: TranscriptGroup) {
  const params = { ...group.statusParams }
  if (params.time) params.time = new Date(String(params.time)).toLocaleTimeString(locale.value)
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
function onScroll() {
  const el = scroller.value
  if (el) stick.value = el.scrollHeight - el.scrollTop - el.clientHeight < 120
}
async function bottom() { await nextTick(); if (scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight }
watch(() => [groups.value.length, Object.values(props.state.previews).map(p => Object.values(p.parts).map(part => part.text).join('')).join('')], () => { if (stick.value) void bottom() }, { flush: 'post' })
watch(() => props.sessionID, () => { stick.value = true; void bottom() }, { immediate: true, flush: 'post' })
useResizeObserver(content, () => { if (stick.value) void bottom() })
function older() { stick.value = false; emit('older') }
</script>
<template>
  <div
    ref="scroller"
    class="transcript-scroll"
    @scroll="onScroll"
  >
    <div
      ref="content"
      class="transcript"
    >
      <div
        v-if="state.historyCursor"
        class="history-loader"
      >
        <Button
          variant="ghost"
          size="small"
          :busy="loadingHistory"
          @click="older"
        >
          {{ t('older') }}
        </Button>
      </div>
      <TransitionGroup name="message">
        <section
          v-for="(group, groupIndex) in groups"
          :key="group.key"
          class="transcript-turn"
          :class="`turn-${group.role}`"
        >
          <template v-if="group.role === 'assistant'">
            <div
              v-for="block in group.blocks"
              :key="block.key"
              class="transcript-block"
            >
              <ActivityLine
                v-if="block.kind === 'process'"
                :items="block.items"
                :active="block.active"
                :status="block.active && group.status !== 'working' && group.status !== 'preparing' ? statusText(group) : undefined"
                :session-i-d="sessionID"
                :state="state"
                @expand="stick = false"
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
              :class="{ 'text-danger': group.status === 'needs_attention' }"
              role="status"
            >
              {{ statusText(group) }}
            </p>
          </template>
          <article
            v-for="entry in group.entries.filter(entry => group.role === 'user' || images(entry).length || files(entry).length)"
            :key="entry.id"
            :class="['message', group.role === 'user' ? 'message-user' : 'message-attachments']"
          >
            <div
              v-if="sender(entry)"
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
                v-for="(_, index) in images(entry)"
                :key="index"
                :href="imageURL(sessionID, entry.id, index)"
                target="_blank"
                rel="noopener"
              >
                <img
                  :src="imageURL(sessionID, entry.id, index)"
                  :alt="t('historicalImage')"
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
          </div>
          <p
            v-if="copyError && copiedGroup === group.key"
            class="copy-error text-danger"
            role="status"
          >
            {{ copyError }}
          </p>
        </section>
      </TransitionGroup>
    </div>
    <Transition name="float">
      <Button
        v-if="!stick"
        class="scroll-bottom"
        size="icon"
        :aria-label="t('bottom')"
        @click="stick = true; bottom()"
      >
        <ArrowDown />
      </Button>
    </Transition>
  </div>
</template>
