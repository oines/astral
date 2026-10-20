<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useResizeObserver } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { Button } from '@astral/ui'
import { ArrowDown, File, GitBranch, Info } from '@astral/icons'
import type { Entry, FileRef } from '../lib/types'
import { imageURL, object } from '../lib/api'
import { messageText, transcriptRows, type SessionState } from '../lib/session-state'
import { useSurfaceStore } from '../stores/surface'
import Markdown from './Markdown.vue'
import ActivityLine from './ActivityLine.vue'
const props = defineProps<{ state: SessionState; sessionID: string; loadingHistory: boolean }>()
const emit = defineEmits<{ older: []; fork: [entryID: string] }>()
const { t } = useI18n()
const surface = useSurfaceStore()
const scroller = ref<HTMLElement>()
const content = ref<HTMLElement>()
const stick = ref(true)
const rows = computed(() => transcriptRows(props.state))
function files(entry: Entry): FileRef[] {
  const raw = object(entry.message?.metadata).files
  return Array.isArray(raw) ? raw as FileRef[] : []
}
function sender(entry: Entry) {
  const meta = object(entry.message?.metadata)
  return meta.source === 'im' ? String(object(meta.sender).name ?? '') : ''
}
function images(entry: Entry) { return (entry.message?.content ?? []).filter(p => p.type === 'image') }
const previewParts = computed(() => Object.values(props.state.previews).flatMap(p => Object.entries(p.parts).sort(([a], [b]) => Number(a) - Number(b)).map(([, part]) => part)))
function onScroll() {
  const el = scroller.value
  if (el) stick.value = el.scrollHeight - el.scrollTop - el.clientHeight < 120
}
async function bottom() { await nextTick(); if (scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight }
watch(() => [rows.value.length, previewParts.value.map(p => p.text).join('')], () => { if (stick.value) void bottom() }, { flush: 'post' })
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
      <template
        v-for="row in rows"
        :key="row.kind === 'message' ? row.entry.id : row.runID"
      >
        <ActivityLine
          v-if="row.kind === 'activity'"
          :run-i-d="row.runID"
          :session-i-d="sessionID"
          :state="state"
        />
        <article
          v-else
          :class="['message', row.entry.message?.role === 'user' ? 'message-user' : 'message-assistant']"
        >
          <div
            v-if="sender(row.entry)"
            class="message-sender"
          >
            {{ sender(row.entry) }} · Telegram
          </div>
          <div
            v-if="row.entry.message?.role === 'user'"
            class="user-text"
          >
            {{ messageText(row.entry) }}
          </div>
          <Markdown
            v-else
            :content="messageText(row.entry)"
            :session-i-d="sessionID"
          />
          <div
            v-if="images(row.entry).length"
            class="message-images"
          >
            <a
              v-for="(_, index) in images(row.entry)"
              :key="index"
              :href="imageURL(sessionID, row.entry.id, index)"
              target="_blank"
              rel="noopener"
            >
              <img
                :src="imageURL(sessionID, row.entry.id, index)"
                :alt="t('historicalImage')"
                loading="lazy"
              >
            </a>
          </div>
          <div
            v-if="files(row.entry).length"
            class="message-files"
          >
            <button
              v-for="file in files(row.entry)"
              :key="file.id"
              class="file-chip"
              @click="surface.open({ kind: 'file', id: file.id, sessionID, ref: file })"
            >
              <File />{{ file.display_name }}
            </button>
          </div>
          <div class="message-actions">
            <Button
              variant="ghost"
              size="icon"
              :title="t('fork')"
              :aria-label="t('fork')"
              @click="emit('fork', row.entry.id)"
            >
              <GitBranch />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              :title="t('details')"
              :aria-label="t('details')"
              @click="surface.open({ kind: 'entry', entry: row.entry, sessionID })"
            >
              <Info />
            </Button>
          </div>
        </article>
      </template>
      <div
        v-for="(part, index) in previewParts"
        :key="index"
        class="message-assistant stream-preview"
      >
        <Markdown
          v-if="part.type === 'text_delta'"
          :content="part.text"
          :session-i-d="sessionID"
          streaming
        />
        <details
          v-else-if="part.type === 'reasoning_delta'"
          class="reasoning"
        >
          <summary>{{ t('reasoning') }}</summary><Markdown
            :content="part.text"
            :session-i-d="sessionID"
            streaming
          />
        </details>
      </div>
      <div class="transcript-end" />
    </div>
    <Button
      v-if="!stick"
      class="scroll-bottom"
      variant="secondary"
      size="icon"
      :aria-label="t('recent')"
      @click="stick = true; bottom()"
    >
      <ArrowDown />
    </Button>
  </div>
</template>
