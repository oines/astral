<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { createSession, compactSession, forkSession, updateSession } from '@astral/sdk'
import { Button, Modal } from '@astral/ui'
import { ArrowRight, Ellipsis, PanelLeftOpen, Sparkle, GitBranch } from '@astral/icons'
import { useAppStore } from '../stores/app'
import { useSurfaceStore } from '../stores/surface'
import { useSession } from '../composables/useSession'
import { errorText, request } from '../lib/api'
import Sidebar from '../components/Sidebar.vue'
import Composer from '../components/Composer.vue'
import Transcript from '../components/Transcript.vue'
import ContextSurface from '../components/ContextSurface.vue'
const route = useRoute()
const router = useRouter()
const app = useAppStore()
const surface = useSurfaceStore()
const { t } = useI18n()
const id = computed(() => typeof route.params.id === 'string' ? route.params.id : '')
const live = useSession(id)
const view = computed(() => live.state.value.view)
const error = ref('')
const busy = ref(false)
const agentID = ref(app.agents[0]?.id || '')
const menu = ref(false)
const rename = ref(false)
const title = ref('')
const compact = ref(false)
const instructions = ref('')
const agent = computed(() => app.agents.find(a => a.id === view.value?.agent_id))
async function start() {
  busy.value = true
  error.value = ''
  try {
    const session = await request(createSession({ body: { agent_id: agentID.value, title: t('newSession') } }))
    await app.refresh()
    await router.push(`/sessions/${session.id}`)
  } catch (e) { error.value = errorText(e) }
  finally { busy.value = false }
}
async function presentation(patch: { title?: string; pinned?: boolean; archived?: boolean }) {
  try {
    await request(updateSession({ path: { id: id.value }, body: patch }))
    await app.refresh()
    rename.value = false
    menu.value = false
  } catch (e) { error.value = errorText(e) }
}
function sent(text: string) {
  if (view.value && ['New session', '新建会话', 'Untitled Session'].includes(view.value.title) && text.trim()) void presentation({ title: text.trim().slice(0, 64) })
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
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'n') { e.preventDefault(); void router.push('/') }
}
window.addEventListener('keydown', shortcut)
onBeforeUnmount(() => { window.removeEventListener('keydown', shortcut); surface.close() })
watch(id, () => { surface.close(); error.value = ''; menu.value = false })
</script>
<template>
  <div class="chat-layout">
    <div
      v-if="app.sidebar"
      class="sidebar-backdrop"
      @click="app.sidebar = false"
    />
    <Sidebar v-if="app.sidebar" />
    <main class="chat-main">
      <Button
        v-if="!app.sidebar"
        class="sidebar-open"
        variant="ghost"
        size="icon"
        :aria-label="t('recent')"
        @click="app.sidebar = true"
      >
        <PanelLeftOpen />
      </Button>
      <template v-if="!id">
        <div class="welcome">
          <span class="welcome-mark">✳</span>
          <h1>{{ t(app.models.length && app.agents.length ? 'welcome' : 'setup') }}</h1>
          <p class="muted">
            {{ t(app.models.length && app.agents.length ? 'welcomeText' : 'setupText') }}
          </p>
          <div
            v-if="app.agents.length && app.models.length"
            class="welcome-start"
          >
            <label class="label">{{ t('agent') }}<select v-model="agentID"><option
              v-for="a in app.agents"
              :key="a.id"
              :value="a.id"
            >{{ a.name }}</option></select></label>
            <Button
              variant="primary"
              :busy="busy"
              :disabled="!agentID"
              @click="start"
            >
              {{ t('newSession') }}<ArrowRight />
            </Button>
          </div>
          <div
            v-else
            class="row wrap"
          >
            <RouterLink
              to="/settings/models"
              class="button button-primary"
            >
              {{ t('setupModels') }}<ArrowRight />
            </RouterLink><RouterLink
              to="/settings/agents"
              class="button button-secondary"
            >
              {{ t('setupAgent') }}
            </RouterLink>
          </div>
          <p
            v-if="error"
            class="error-box"
          >
            {{ error }}
          </p>
          <span class="welcome-caption">{{ t('welcomeCaption') }}</span>
        </div>
      </template>
      <template v-else-if="view">
        <div class="session-heading">
          <div class="session-identity">
            <span class="agent-avatar">{{ agent?.avatar || agent?.name.slice(0, 1) || 'A' }}</span><div>
              <h1>{{ view.title }}</h1><p class="muted small">
                {{ agent?.name || t('missing') }}<span v-if="view.source === 'im'"> · Telegram</span><span v-if="live.connection.value !== 'connected'"> · {{ t(live.connection.value) }}</span>
              </p>
            </div>
          </div>
          <div class="session-menu">
            <Button
              variant="ghost"
              size="icon"
              :aria-label="t('settings')"
              @click="menu = !menu"
            >
              <Ellipsis />
            </Button>
            <div
              v-if="menu"
              class="dropdown stack"
            >
              <button @click="title = view.title; rename = true; menu = false">
                {{ t('rename') }}
              </button>
              <button @click="presentation({ pinned: !view.pinned })">
                {{ t(view.pinned ? 'unpin' : 'pin') }}
              </button>
              <a :href="`/api/v1/sessions/${id}/rollout`">{{ t('export') }}</a>
              <button @click="presentation({ archived: !view.archived })">
                {{ t(view.archived ? 'unarchive' : 'archive') }}
              </button>
            </div>
          </div>
        </div>
        <div
          v-if="view.parent_session_id"
          class="branch-notice"
        >
          <GitBranch /><RouterLink :to="`/sessions/${view.parent_session_id}`">
            {{ t('source') }}
          </RouterLink><span class="muted small"> · {{ t('sharedFiles') }}</span>
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
          :loading-history="live.loadingHistory.value"
          @older="live.older"
          @fork="fork"
        />
        <Composer
          :key="id"
          :view="view"
          :live="live.connection.value === 'connected'"
          @sent="sent"
          @compact="compact = true"
        />
      </template>
      <div
        v-else
        class="empty"
      >
        <Sparkle /><p>{{ t(live.connection.value) }}</p><p
          v-if="live.error.value"
          class="error-box"
        >
          {{ live.error.value }}
        </p>
      </div>
    </main>
    <ContextSurface
      :state="live.state.value"
      :more-trace="live.moreTrace"
    />
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
      <p class="muted">
        {{ t('compactHint') }}
      </p><label class="label">{{ t('instructions') }}<textarea v-model="instructions" /></label><Button
        variant="primary"
        :busy="busy"
        @click="doCompact"
      >
        {{ t('compact') }}
      </Button>
    </Modal>
  </div>
</template>
