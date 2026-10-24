<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { listConnectionsQuery } from '@astral/sdk/colada'
import { ArrowLeft, ChevronRight, Monitor, Plus, Settings2 } from '@astral/icons'
import { Modal } from '@astral/ui'
import type { Agent } from '../../lib/types'
import ChannelIcon from '../ChannelIcon.vue'
import { platformLabel } from '../../lib/im-platforms'

const props = defineProps<{ bot: Agent }>()
const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const connections = useQuery(listConnectionsQuery())
const platforms = computed(() => [...new Set((connections.data.value || []).filter(c => c.agent_id === props.bot.id).map(c => c.platform))])
const selectedPlatform = computed(() => typeof route.query.platform === 'string' ? route.query.platform : route.query.tab === 'channels' ? 'telegram' : '')
const connecting = ref(false)
function connect(platform: string) {
  connecting.value = false
  void router.push({ path: '/settings/agents', query: { agent: props.bot.id, platform, connect: '1' } })
}
</script>
<template>
  <aside class="settings-nav bot-settings-nav">
    <RouterLink
      to="/settings/agents"
      class="settings-back"
    >
      <ArrowLeft />{{ t('allAgents') }}
    </RouterLink>
    <div class="bot-nav-identity">
      <span class="agent-avatar">{{ bot.avatar || bot.name.slice(0, 1) }}</span>
      <strong>{{ bot.name }}</strong>
    </div>
    <nav :aria-label="bot.name">
      <RouterLink
        :to="{ path: '/settings/agents', query: { agent: bot.id } }"
        :class="{ selected: !selectedPlatform && route.query.tab !== 'computers' }"
      >
        <Settings2 />{{ t('botBasics') }}
      </RouterLink>
      <RouterLink
        :to="{ path: '/settings/agents', query: { agent: bot.id, tab: 'computers' } }"
        :class="{ selected: route.query.tab === 'computers' }"
      >
        <Monitor />{{ t('botComputerPermissions') }}
      </RouterLink>
      <p class="settings-nav-group">
        {{ t('botPlatforms') }}
      </p>
      <RouterLink
        v-for="platform in platforms"
        :key="platform"
        :to="{ path: '/settings/agents', query: { agent: bot.id, platform } }"
        :class="{ selected: selectedPlatform === platform }"
      >
        <ChannelIcon
          :platform="platform"
          aria-hidden="true"
        />{{ platformLabel(platform, locale) }}
      </RouterLink>
      <button
        type="button"
        class="settings-nav-action"
        @click="connecting = true"
      >
        <Plus />{{ t('connectPlatform') }}
      </button>
    </nav>
    <Modal
      v-model:open="connecting"
      class="platform-picker"
      :title="t('choosePlatform')"
    >
      <div class="platform-picker-list">
        <button
          v-for="platform in ['telegram', 'qq', 'weixin', 'feishu']"
          :key="platform"
          type="button"
          class="platform-picker-option"
          @click="connect(platform)"
        >
          <ChannelIcon
            :platform="platform"
            aria-hidden="true"
          />
          <span>{{ platformLabel(platform, locale) }}</span><ChevronRight aria-hidden="true" />
        </button>
      </div>
    </Modal>
  </aside>
</template>
<style scoped>
:global(.platform-picker.modal-content) { width: min(360px, calc(100vw - 32px)); padding: 16px; gap: 12px; }
:global(.platform-picker .modal-title) { font-size: 16px; font-weight: 550; }
.platform-picker-list { display: flex; flex-direction: column; gap: 2px; }
.platform-picker-option { display: flex; align-items: center; gap: 12px; min-height: 48px; padding: 10px 12px; border: 0; border-radius: 8px; background: transparent; text-align: left; }
.platform-picker-option:hover, .platform-picker-option:focus-visible { background: var(--hover); }
.platform-picker-option:focus-visible { outline: 1px solid var(--focus-ring); outline-offset: -1px; }
.platform-picker-option :deep(.platform-icon) { width: 24px; height: 24px; }
.platform-picker-option > span { flex: 1; }
.platform-picker-option > svg { width: 15px; height: 15px; color: var(--muted); }
</style>
