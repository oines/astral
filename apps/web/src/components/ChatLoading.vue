<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Skeleton } from '@astral/ui'
defineProps<{ welcome?: boolean; reconnecting?: boolean }>()
const { t } = useI18n()
</script>
<template>
  <div
    class="chat-loading"
    :class="{ 'chat-loading-welcome': welcome }"
    role="status"
    :aria-label="t(reconnecting ? 'reconnecting' : 'loading')"
    aria-busy="true"
  >
    <div
      v-if="welcome"
      class="chat-skeleton-welcome"
      aria-hidden="true"
    >
      <Skeleton class="chat-skeleton-logo" />
      <Skeleton class="chat-skeleton-title" />
    </div>
    <div
      v-else
      class="transcript-viewport"
      aria-hidden="true"
    >
      <div class="transcript-scroll">
        <div class="transcript chat-skeleton-transcript">
          <template
            v-for="turn in 2"
            :key="turn"
          >
            <Skeleton class="chat-skeleton-user" />
            <div class="chat-skeleton-answer">
              <Skeleton
                v-for="line in (turn === 1 ? 3 : 4)"
                :key="line"
                class="chat-skeleton-line"
              />
              <div class="row chat-skeleton-actions">
                <Skeleton /><Skeleton /><Skeleton />
              </div>
            </div>
          </template>
        </div>
      </div>
    </div>
    <div class="composer-area">
      <div
        class="composer"
        aria-hidden="true"
      >
        <div class="composer-input-row">
          <Skeleton class="chat-skeleton-add" />
          <Skeleton class="chat-skeleton-prompt" />
          <Skeleton class="chat-skeleton-send" />
        </div>
      </div>
      <div
        class="composer-environment"
        aria-hidden="true"
      >
        <Skeleton class="chat-skeleton-computer" />
        <Skeleton class="chat-skeleton-model" />
      </div>
      <p
        v-if="reconnecting"
        class="composer-connection"
      >
        {{ t('reconnecting') }}
      </p>
    </div>
  </div>
</template>
