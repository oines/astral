<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { Button, Menu, MenuItem, MenuSeparator } from '@astral/ui'
import { Clock3, Download, Ellipsis, FolderOpen, GitBranch, Lock, Pencil, Pin, Zap } from '@astral/icons'
import type { Session } from '../lib/types'
import { previewSessionAutomations, type SessionAutomations } from '../lib/sidebar-groups'
import { automationLocation } from '../lib/automation-links'

const props = defineProps<{ session: Session; busy: boolean; running?: boolean; automations?: SessionAutomations }>()
const emit = defineEmits<{ rename: []; navigate: []; presentation: [patch: { pinned?: boolean; archived?: boolean; private?: boolean }] }>()
const { t } = useI18n()
const automated = computed(() => props.automations ? previewSessionAutomations(props.automations) : [])
const remaining = computed(() => props.automations ? props.automations.timed.length + props.automations.events.length - automated.value.length : 0)
const navigating = ref(false)
function navigateResource(event: MouseEvent) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  navigating.value = true
  emit('navigate')
}
function closeAutoFocus(event: Event) {
  if (!navigating.value) return
  event.preventDefault()
  navigating.value = false
}
</script>
<template>
  <Menu
    class="session-menu"
    side="bottom"
    align="start"
    :side-offset="4"
    :label="t('sessionOptionsFor', { name: session.title })"
    @close-auto-focus="closeAutoFocus"
  >
    <template #trigger>
      <Button
        class="session-menu-trigger"
        variant="ghost"
        size="icon"
        :data-session-menu="session.id"
        :aria-label="t('sessionOptionsFor', { name: session.title })"
        :title="t('sessionOptions')"
      >
        <Ellipsis />
      </Button>
    </template>
    <MenuItem
      v-for="entry in automated"
      :key="`${entry.kind}:${entry.id}`"
      as-child
    >
      <RouterLink
        class="menu-item"
        :to="automationLocation(session.agent_id, entry)"
        @click="navigateResource"
      >
        <Clock3
          v-if="entry.kind === 'schedule'"
          :aria-label="t('scheduleTimedTasks')"
        />
        <Zap
          v-else
          :aria-label="t('wakeEndpoints')"
        />
        <span class="session-automation-title">{{ entry.title }}</span>
      </RouterLink>
    </MenuItem>
    <MenuItem
      v-if="remaining"
      as-child
    >
      <RouterLink
        class="menu-item muted"
        :to="{ path: '/schedules', query: { agent: session.agent_id } }"
        @click="emit('navigate')"
      >
        {{ t('sessionAutomationsMore', { count: remaining }) }}
      </RouterLink>
    </MenuItem>
    <MenuSeparator
      v-if="automated.length"
      class="separator session-automation-separator"
    />
    <MenuItem
      v-if="!session.binding_id"
      class="menu-item"
      :disabled="busy"
      @select="emit('rename')"
    >
      <Pencil />{{ t('rename') }}
    </MenuItem>
    <MenuItem
      class="menu-item"
      :disabled="busy"
      @select="emit('presentation', { private: !session.private })"
    >
      <Lock />{{ t(session.private ? 'makeSessionNonPrivate' : 'makeSessionPrivate') }}
    </MenuItem>
    <MenuItem
      class="menu-item"
      :disabled="busy"
      @select="emit('presentation', { pinned: !session.pinned })"
    >
      <Pin />{{ t(session.pinned ? 'unpin' : 'pin') }}
    </MenuItem>
    <MenuItem as-child>
      <a
        class="menu-item"
        :href="`/api/v1/sessions/${session.id}/rollout`"
      ><Download />{{ t('export') }}</a>
    </MenuItem>
    <MenuItem
      v-if="session.parent_session_id"
      as-child
    >
      <RouterLink
        class="menu-item"
        :to="`/sessions/${session.parent_session_id}`"
      >
        <GitBranch />{{ t('source') }}
      </RouterLink>
    </MenuItem>
    <MenuItem
      v-if="!session.binding_id"
      class="menu-item"
      :disabled="busy"
      @select="emit('presentation', { archived: !session.archived })"
    >
      <FolderOpen />{{ t(session.archived ? 'unarchive' : running ? 'stopAndArchive' : 'archive') }}
    </MenuItem>
  </Menu>
</template>
