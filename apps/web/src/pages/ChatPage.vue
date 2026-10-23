<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { compactSession, forkSession, updateSession } from '@astral/sdk'
import { Button, Modal, Popover } from '@astral/ui'
import { ArrowRight, Ellipsis, PanelLeftClose, PanelLeftOpen, AstralLogo, GitBranch, Pencil, Pin, Download, FolderOpen } from '@astral/icons'
import { useAppStore } from '../stores/app'
import { useSurfaceStore } from '../stores/surface'
import { useSession } from '../composables/useSession'
import type { SessionView } from '../lib/types'
import { errorText, newMeta, request } from '../lib/api'
import Sidebar from '../components/Sidebar.vue'
import Composer from '../components/Composer.vue'
import Transcript from '../components/Transcript.vue'
import ContextSurface from '../components/ContextSurface.vue'
const route = useRoute()
const router = useRouter()
const app = useAppStore()
const surface = useSurfaceStore()
const { t } = useI18n()
const id = ref(typeof route.params.id === 'string' ? route.params.id : '')
watch(() => route.path, path => { if (!path.startsWith('/settings')) id.value = typeof route.params.id === 'string' ? route.params.id : '' })
const live = useSession(id)
const view = computed(() => live.state.value.view)
const error = ref('')
const busy = ref(false)
const activeAgent = computed(() => app.agents.find(agent => agent.id === app.activeAgentID))
const readyToStart = computed(() => activeAgent.value && app.availableModels.some(model => model.id === activeAgent.value?.default_model_id))
watch(() => view.value?.id === id.value ? view.value?.agent_id : app.sessions.find(session => session.id === id.value)?.agent_id, agentID => {
  if (agentID) app.activeAgentID = agentID
}, { immediate: true })
const menu = ref(false)
const rename = ref(false)
const title = ref('')
const compact = ref(false)
const instructions = ref('')
const sidebarToggle = ref<InstanceType<typeof Button>>()
let restoreSidebarFocus = false
function sidebarLeaving(element: Element) { restoreSidebarFocus = element.contains(document.activeElement) }
function sidebarClosed() {
  if (restoreSidebarFocus && !app.sidebar) sidebarToggle.value?.$el.focus()
}
function sidebarOpened(element: Element) {
  if (document.activeElement === document.body) element.querySelector<HTMLButtonElement>('.sidebar-agent')?.focus()
}
const initialView = computed<SessionView>(() => {
  const agent = app.agents.find(a => a.id === app.activeAgentID)
  return { ...newMeta(), agent_id: app.activeAgentID, title: t('newSession'), archived: false, pinned: false, source: 'web', options: {}, head_id: '', rollout_id: '', event_sequence: 0, status: 'idle', can_continue: false, approvals: [], pending: [], unresolved: [], profile_id: agent?.default_model_id || '', computer_id: agent?.default_computer_id || '' }
})
async function started(sessionID: string) {
  await router.push(`/sessions/${sessionID}`)
  try { await app.refresh() } catch (e) { error.value = errorText(e) }
}
async function presentation(patch: { title?: string; pinned?: boolean; archived?: boolean }) {
  try {
    await request(updateSession({ path: { id: id.value }, body: patch }))
    await app.refresh()
    rename.value = false
    menu.value = false
  } catch (e) { error.value = errorText(e) }
}
async function fork(entryID: string) {
  error.value = ''
  const retryKey = `astral:fork:${id.value}:${entryID}`
  const targetID = sessionStorage.getItem(retryKey) || `session_${crypto.randomUUID()}`
  sessionStorage.setItem(retryKey, targetID)
  try {
    const session = await request(forkSession({ path: { id: id.value }, body: { id: targetID, entry_id: entryID, title: '' } }))
    await app.refresh()
    await router.push(`/sessions/${session.id}`)
    sessionStorage.removeItem(retryKey)
  } catch (e) { error.value = errorText(e) }
}
async function doCompact() {
  busy.value = true
  try { await request(compactSession({ path: { id: id.value }, body: { instructions: instructions.value } })); compact.value = false }
  catch (e) { error.value = errorText(e) }
  finally { busy.value = false }
}
function shortcut(e: KeyboardEvent) {
  if (route.path.startsWith('/settings') || e.defaultPrevented) return
  if (e.key === 'Escape' && app.sidebar && window.innerWidth < 760) { e.preventDefault(); app.sidebar = false }
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'n') { e.preventDefault(); void router.push('/') }
}
window.addEventListener('keydown', shortcut)
onBeforeUnmount(() => { window.removeEventListener('keydown', shortcut); surface.close() })
watch(id, () => { surface.close(); error.value = ''; menu.value = false })
</script>
<template>
  <div
    class="chat-layout"
    :class="{ 'sidebar-hidden': !app.sidebar }"
  >
    <Button
      ref="sidebarToggle"
      class="sidebar-toggle"
      variant="ghost"
      size="icon"
      :aria-label="t(app.sidebar ? 'closeSidebar' : 'openSidebar')"
      :title="t(app.sidebar ? 'closeSidebar' : 'openSidebar')"
      aria-controls="chat-sidebar"
      :aria-expanded="app.sidebar"
      @click="app.sidebar = !app.sidebar"
    >
      <PanelLeftClose v-if="app.sidebar" />
      <PanelLeftOpen v-else />
    </Button>
    <Transition name="fade">
      <div
        v-if="app.sidebar"
        class="sidebar-backdrop"
        @click="app.sidebar = false"
      />
    </Transition>
    <Transition
      name="sidebar"
      @before-leave="sidebarLeaving"
      @after-leave="sidebarClosed"
      @after-enter="sidebarOpened"
    >
      <Sidebar
        v-if="app.sidebar"
        :inert="!app.sidebar"
      />
    </Transition>
    <main class="chat-main">
      <template v-if="!id">
        <div class="welcome">
          <AstralLogo class="welcome-logo" />
          <template v-if="readyToStart">
            <h1>{{ t('welcome') }}</h1>
            <Composer
              :key="app.activeAgentID"
              :view="initialView"
              :live="true"
              @started="started"
            />
          </template>
          <div
            v-else
            class="welcome-setup"
          >
            <h1>{{ t('setup') }}</h1><p class="muted">
              {{ activeAgent ? t('setupMissingModel', { name: activeAgent.name }) : t('setupNoBots') }}
            </p><div class="row wrap">
              <RouterLink
                v-if="!app.availableModels.length"
                to="/settings/models"
                class="button button-primary"
              >
                {{ t('setupModels') }}<ArrowRight />
              </RouterLink><RouterLink
                v-if="activeAgent"
                :to="{ path: '/settings/agents', query: { agent: activeAgent.id } }"
                class="button"
                :class="app.availableModels.length ? 'button-primary' : 'button-secondary'"
              >
                {{ t('setupBotSettings') }}
              </RouterLink><RouterLink
                v-else
                to="/settings/agents"
                class="button button-secondary"
              >
                {{ t('setupAgent') }}
              </RouterLink>
            </div>
          </div>
        </div>
      </template>
      <template v-else-if="view">
        <div class="session-utilities">
          <Popover
            v-model:open="menu"
            align="end"
            :label="t('sessionOptions')"
          >
            <template #trigger>
              <Button
                variant="ghost"
                size="icon"
                :aria-label="t('sessionOptions')"
              >
                <Ellipsis />
              </Button>
            </template>
            <div class="session-menu">
              <div class="popover-label">
                {{ view.title }}<p class="session-agent">
                  {{ app.agents.find(a => a.id === view?.agent_id)?.name || t('missing') }}<span v-if="view.channel_platform === 'telegram'"> · Telegram</span>
                </p>
              </div>
              <button
                v-if="!view.binding_id"
                class="menu-item"
                @click="title = view.title; rename = true; menu = false"
              >
                <Pencil />{{ t('rename') }}
              </button>
              <button
                class="menu-item"
                @click="presentation({ pinned: !view.pinned })"
              >
                <Pin />{{ t(view.pinned ? 'unpin' : 'pin') }}
              </button>
              <a
                class="menu-item"
                :href="`/api/v1/sessions/${id}/rollout`"
              ><Download />{{ t('export') }}</a>
              <RouterLink
                v-if="view.parent_session_id"
                class="menu-item"
                :to="`/sessions/${view.parent_session_id}`"
              >
                <GitBranch />{{ t('source') }}
              </RouterLink>
              <button
                class="menu-item"
                @click="presentation({ archived: !view.archived })"
              >
                <FolderOpen />{{ t(view.archived ? 'unarchive' : 'archive') }}
              </button>
            </div>
          </Popover>
        </div>
        <div
          v-if="error || live.error.value"
          class="chat-error error-box"
        >
          {{ error || live.error.value }}
        </div>
        <Transcript
          :state="live.state.value"
          :session-i-d="id"
          :load-older="live.older"
          @fork="fork"
        />
        <Composer
          :key="id"
          :view="view"
          :live="live.connection.value === 'connected'"
          @compact="compact = true"
        />
      </template>
      <div
        v-else
        class="empty"
      >
        <AstralLogo decorative /><p>{{ t(live.connection.value) }}</p><p
          v-if="live.error.value"
          class="error-box"
        >
          {{ live.error.value }}
        </p>
      </div>
    </main>
    <ContextSurface />
    <Modal
      v-model:open="rename"
      :title="t('rename')"
    >
      <label class="label">{{ t('name') }}<input
        v-model="title"
        @keydown.enter="presentation({ title })"
      ></label><Button
        variant="primary"
        :disabled="!title.trim()"
        @click="presentation({ title })"
      >
        {{ t('save') }}
      </Button>
    </Modal>
    <Modal
      v-model:open="compact"
      :title="t('compact')"
    >
      <div class="label">
        <label for="compact-instructions">{{ t('compactInstructions') }}</label>
        <textarea
          id="compact-instructions"
          v-model="instructions"
        />
      </div>
      <Button
        variant="primary"
        :busy="busy"
        @click="doCompact"
      >
        {{ t('compactAction') }}
      </Button>
    </Modal>
  </div>
</template>
