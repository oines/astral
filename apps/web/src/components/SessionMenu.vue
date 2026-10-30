<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Button, Menu, MenuItem } from '@astral/ui'
import { Download, Ellipsis, FolderOpen, GitBranch, Pencil, Pin } from '@astral/icons'
import type { Session } from '../lib/types'

defineProps<{ session: Session; busy: boolean }>()
const emit = defineEmits<{ rename: []; presentation: [patch: { pinned?: boolean; archived?: boolean }] }>()
const { t } = useI18n()
</script>
<template>
  <Menu
    class="session-menu"
    side="bottom"
    align="start"
    :side-offset="4"
    :label="t('sessionOptionsFor', { name: session.title })"
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
      <FolderOpen />{{ t(session.archived ? 'unarchive' : 'archive') }}
    </MenuItem>
  </Menu>
</template>
