<script setup lang="ts">
import { Skeleton } from '@astral/ui'
import ChatLoading from './ChatLoading.vue'
defineProps<{ sidebar: boolean; welcome: boolean }>()
</script>
<template>
  <div
    class="chat-layout"
    :class="{ 'sidebar-hidden': !sidebar }"
  >
    <Skeleton class="sidebar-toggle chat-skeleton-toggle" />
    <aside
      v-if="sidebar"
      class="sidebar chat-skeleton-sidebar"
      aria-hidden="true"
    >
      <div class="sidebar-heading">
        <Skeleton class="chat-skeleton-bot" />
      </div>
      <Skeleton class="chat-skeleton-nav" />
      <Skeleton class="chat-skeleton-search" />
      <div class="session-list">
        <Skeleton class="chat-skeleton-group" />
        <Skeleton
          v-for="row in 7"
          :key="row"
          class="chat-skeleton-session"
        />
      </div>
      <Skeleton class="chat-skeleton-settings" />
    </aside>
    <main class="chat-main">
      <ChatLoading :welcome="welcome" />
    </main>
  </div>
</template>
