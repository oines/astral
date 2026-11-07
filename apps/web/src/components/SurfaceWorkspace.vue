<script setup lang="ts">
import { computed, nextTick, provide, ref, useTemplateRef, watch } from 'vue'
import { useResizeObserver } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { Bot, FolderTree, X } from '@astral/icons'
import { Button } from '@astral/ui'
import { useSurfaceStore, type SurfaceTab } from '../stores/surface'
import { useAppStore } from '../stores/app'
import type { FileRef } from '../lib/types'
import type { SubagentActivity } from '../lib/subagents'
import { revealOverflowLabel } from '../lib/overflow-label'
import { useSplitResize } from '../composables/useSplitResize'
import FileExplorer from './files/FileExplorer.vue'
import SurfaceFile from './files/SurfaceFile.vue'
import { workspaceTabKeys } from './files/workspace-context'
import SubagentSurface from './SubagentSurface.vue'
import SubagentList from './SubagentList.vue'

const props = defineProps<{ rootID: string; activities: Record<string, SubagentActivity> }>()
const surface = useSurfaceStore()
const app = useAppStore()
const { t } = useI18n()
const state = computed(() => surface.workspaceFor(props.rootID))
const tabs = computed(() => state.value?.tabs || [])
provide(workspaceTabKeys, computed(() => new Set(tabs.value.map(tab => tab.key))))
const active = computed(() => tabs.value.find(tab => tab.key === state.value?.activeKey))
const currentFile = computed(() => active.value?.kind === 'file' ? active.value : undefined)
const lastFileKey = ref('')
const latestFile = computed(() => tabs.value.find((tab): tab is Extract<SurfaceTab, { kind: 'file' }> => tab.key === lastFileKey.value && tab.kind === 'file' && !!tab.ref))
watch(currentFile, file => { if (file?.ref) lastFileKey.value = file.key }, { immediate: true, deep: true })
const rootSession = computed(() => app.sessionViews[props.rootID] || app.sessions.find(session => session.id === props.rootID))
const directoryTarget = computed(() => {
  const file = currentFile.value?.ref ? currentFile.value : latestFile.value
  if (file?.ref) return { source: file.ref, sessionID: file.sessionID }
  const session = rootSession.value
  const bot = app.agents.find(agent => agent.id === (session?.agent_id || app.activeAgentID))
  const computerID = session?.computer_id || bot?.default_computer_id
  return computerID ? { source: { computer_id: computerID, path: app.sessionViews[props.rootID]?.cwd || '' }, sessionID: props.rootID } : undefined
})
const element = useTemplateRef('workspace')
const nav = useTemplateRef('tabList')
const availableWidth = ref(620)
useResizeObserver(element, entries => { if (entries[0]) availableWidth.value = entries[0].contentRect.width })
const narrow = computed(() => availableWidth.value < 520)
const drawerOpen = ref(false)
const directoryRequested = ref(false)
const directoryShown = computed(() => !!directoryTarget.value && (!!currentFile.value?.ref || directoryRequested.value) && (narrow.value ? drawerOpen.value : state.value?.directoryOpen))
const maximumTreeWidth = computed(() => Math.min(280, Math.max(160, availableWidth.value - 321)))
const treeWidth = computed(() => Math.min(surface.directoryWidth, maximumTreeWidth.value))
function setTreeWidth(width: number) { surface.directoryWidth = Math.max(160, Math.min(maximumTreeWidth.value, width)) }
const { resizing: resizingTree, start: resizeTree } = useSplitResize({
  width: () => treeWidth.value,
  setWidth: setTreeWidth,
  enabled: () => surface.rootID === props.rootID && !!surface.current && !!directoryShown.value && !narrow.value,
})
const fileButton = useTemplateRef<InstanceType<typeof Button>>('fileButton')
function toggleDirectory() {
  const next = !directoryShown.value
  directoryRequested.value = true
  if (narrow.value) drawerOpen.value = next
  else if (state.value) state.value.directoryOpen = next
}
function resolved(key: string, file: FileRef) { surface.updateFile(key, file, props.rootID) }
function openFile(file: FileRef) {
  const sessionID = directoryTarget.value?.sessionID
  if (sessionID && !app.deletedSessions.has(sessionID) && surface.workspaceFor(props.rootID)) surface.open({ kind: 'file', id: file.id, sessionID, ref: file }, props.rootID)
}
function title(tab: SurfaceTab) {
  if (tab.kind !== 'file') return tab.kind === 'subagent' ? app.sessionViews[tab.id]?.title || tab.title : t('workspaceSubagents')
  const name = tab.ref?.display_name || t('workspaceFile')
  const ambiguous = tabs.value.some(other => other.key !== tab.key && other.kind === 'file' && other.ref?.display_name === name && other.ref.computer_id !== tab.ref?.computer_id)
  return ambiguous && tab.ref ? `${name} · ${computerName(tab.ref.computer_id)}` : name
}
function computerName(id: string) { return app.computers.find(computer => computer.id === id)?.name || id }
function tabID(key: string) { return `surface-tab-${encodeURIComponent(`${props.rootID}:${key}`)}` }
function closeTab(key: string) {
  surface.closeTab(key)
  void nextTick(() => {
    if (active.value) document.getElementById(tabID(active.value.key))?.focus({ preventScroll: true })
  })
}
function navigate(event: KeyboardEvent, index: number) {
  const key = event.key
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(key)) return
  event.preventDefault()
  const next = key === 'Home' ? 0 : key === 'End' ? tabs.value.length - 1 : (index + (key === 'ArrowRight' ? 1 : -1) + tabs.value.length) % tabs.value.length
  const tab = tabs.value[next]
  if (tab) { surface.select(tab.key); void nextTick(() => document.getElementById(tabID(tab.key))?.focus({ preventScroll: true })) }
}
function revealActiveTab() {
  const item = nav.value?.querySelector<HTMLElement>('[aria-selected="true"]')?.parentElement
  if (!item || !nav.value) return
  const itemRect = item.getBoundingClientRect(), bounds = nav.value.getBoundingClientRect()
  if (itemRect.right > bounds.right) nav.value.scrollLeft += itemRect.right - bounds.right
  else if (itemRect.left < bounds.left) nav.value.scrollLeft -= bounds.left - itemRect.left
}
function prepareTabLeave(element: Element) {
  const tab = element as HTMLElement
  tab.style.width = `${tab.getBoundingClientRect().width}px`
  tab.style.left = `${tab.offsetLeft}px`
  tab.inert = true
}
function resetTabMotion(element: Element) {
  const tab = element as HTMLElement
  tab.style.removeProperty('width')
  tab.style.removeProperty('left')
  tab.inert = false
}
function contentLeaving(element: Element) { (element as HTMLElement).inert = true }
function contentEntering(element: Element) { (element as HTMLElement).inert = false }
watch(() => active.value?.key, () => {
  drawerOpen.value = false
  void nextTick(revealActiveTab)
})
useResizeObserver(nav, () => { void nextTick(revealActiveTab) })
function escape(event: KeyboardEvent) {
  if (event.key === 'Escape' && !event.defaultPrevented && drawerOpen.value) {
    event.preventDefault(); event.stopPropagation(); drawerOpen.value = false
    fileButton.value?.$el.focus({ preventScroll: true })
  }
}
</script>
<template>
  <div
    ref="workspace"
    class="surface-workspace"
    :class="{ 'workspace-narrow': narrow }"
    @keydown="escape"
  >
    <header class="surface-heading workspace-heading">
      <div
        ref="tabList"
        class="workspace-tabs"
        role="tablist"
        :aria-label="t('workspaceTabs')"
      >
        <TransitionGroup
          name="workspace-tab"
          @before-leave="prepareTabLeave"
          @after-leave="resetTabMotion"
          @leave-cancelled="resetTabMotion"
          @before-enter="resetTabMotion"
        >
          <div
            v-for="(tab, index) in tabs"
            :key="tab.key"
            class="workspace-tab"
            :class="{ 'is-active': active?.key === tab.key }"
          >
            <button
              :id="tabID(tab.key)"
              type="button"
              role="tab"
              :aria-selected="active?.key === tab.key"
              :tabindex="active?.key === tab.key ? 0 : -1"
              :aria-controls="`workspace-content-${rootID}`"
              @click="surface.select(tab.key)"
              @keydown="navigate($event, index)"
              @pointerenter="revealOverflowLabel($event, 'span')"
              @focus="revealOverflowLabel($event, 'span')"
            >
              <Bot
                v-if="tab.kind !== 'file'"
                aria-hidden="true"
              /><span>{{ title(tab) }}</span>
            </button>
            <button
              type="button"
              class="workspace-tab-close"
              :aria-label="t('workspaceCloseTab', { name: title(tab) })"
              @click="closeTab(tab.key)"
            >
              <X />
            </button>
          </div>
        </TransitionGroup>
      </div>
      <div class="workspace-actions">
        <Button
          size="icon"
          variant="ghost"
          :aria-label="t('workspaceSubagents')"
          @click="surface.open({ kind: 'subagents', id: rootID })"
        >
          <Bot />
        </Button>
        <Button
          ref="fileButton"
          size="small"
          variant="ghost"
          class="workspace-files-toggle"
          :disabled="!directoryTarget"
          :aria-label="t(directoryShown ? 'workspaceHideFiles' : 'workspaceShowFiles')"
          :aria-pressed="directoryShown"
          :aria-expanded="directoryShown"
          :aria-controls="`workspace-files-${rootID}`"
          @click="toggleDirectory"
        >
          <FolderTree /><span>{{ t('workspaceFiles') }}</span>
        </Button>
        <Button
          size="icon"
          variant="ghost"
          :aria-label="t('workspaceClose')"
          @click="surface.close"
        >
          <X />
        </Button>
      </div>
    </header>
    <div
      :id="`workspace-content-${rootID}`"
      class="workspace-body"
      :class="{ 'is-resizing': resizingTree }"
      role="tabpanel"
      :aria-labelledby="active ? tabID(active.key) : undefined"
    >
      <div class="surface-tab-host">
        <Transition
          name="workspace-content"
          @before-leave="contentLeaving"
          @before-enter="contentEntering"
          @leave-cancelled="contentEntering"
        >
          <KeepAlive :max="6">
            <SurfaceFile
              v-if="active?.kind === 'file'"
              :key="active.key"
              :tab="active"
              :computer-name="active.ref ? computerName(active.ref.computer_id) : ''"
              @resolved="resolved"
            />
            <SubagentSurface
              v-else-if="active?.kind === 'subagent'"
              :key="active.key"
              :session-i-d="active.id"
              :viewing="surface.rootID === rootID && !!surface.current"
            />
            <SubagentList
              v-else-if="active?.kind === 'subagents'"
              :key="active.key"
              :root-i-d="rootID"
              :activities="activities"
            />
          </KeepAlive>
        </Transition>
      </div>
      <Transition name="workspace-backdrop">
        <button
          v-if="narrow && directoryShown"
          type="button"
          class="workspace-directory-backdrop"
          :aria-label="t('workspaceHideFiles')"
          @click="drawerOpen = false"
        />
      </Transition>
      <Transition :name="narrow ? 'workspace-drawer' : 'workspace-directory'">
        <div
          v-if="directoryTarget && (currentFile?.ref || directoryRequested)"
          v-show="directoryShown"
          :id="`workspace-files-${rootID}`"
          class="workspace-directory"
          :inert="!directoryShown"
          :aria-hidden="!directoryShown || undefined"
          :style="{ width: `${treeWidth + 1}px`, '--directory-width': `${treeWidth}px` }"
        >
          <div
            v-show="!narrow"
            class="workspace-directory-resize"
            :class="{ 'is-dragging': resizingTree }"
            role="separator"
            tabindex="0"
            aria-orientation="vertical"
            :aria-label="t('workspaceResizeFiles')"
            :aria-valuemin="160"
            :aria-valuemax="Math.floor(maximumTreeWidth)"
            :aria-valuenow="Math.round(treeWidth)"
            @pointerdown="resizeTree"
            @keydown.left.prevent="setTreeWidth(treeWidth + 20)"
            @keydown.right.prevent="setTreeWidth(treeWidth - 20)"
            @keydown.home.prevent="setTreeWidth(160)"
            @keydown.end.prevent="setTreeWidth(maximumTreeWidth)"
          />
          <div class="workspace-directory-clip">
            <div class="workspace-directory-content">
              <FileExplorer
                :source="directoryTarget.source"
                :session-i-d="directoryTarget.sessionID"
                :computer-name="computerName(directoryTarget.source.computer_id)"
                @open-file="openFile"
              />
            </div>
          </div>
        </div>
      </Transition>
    </div>
  </div>
</template>
<style scoped>
.surface-workspace { display: flex; flex-direction: column; min-height: 0; flex: 1; }
.workspace-heading { display: flex; align-items: center; gap: 8px; padding: 0 10px; height: 58px; border-bottom: 1px solid var(--line); flex-shrink: 0; }
.workspace-tabs { position: relative; min-width: 0; display: flex; flex: 1; gap: 4px; align-items: stretch; align-self: stretch; overflow-x: auto; scrollbar-width: none; }
.workspace-tab { display: flex; align-items: center; gap: 4px; position: relative; flex: 0 0 auto; min-width: 0; max-width: min(200px, 100%); padding: 0 10px; color: var(--muted); font-size: 13px; }
.workspace-tab::before { content: ''; position: absolute; top: 50%; left: 0; right: 0; height: var(--control-icon-size); transform: translateY(-50%); border-radius: var(--radius-control); pointer-events: none; transition: background-color var(--motion-fast) ease; }
.workspace-tab.is-active { color: var(--ink); }
.workspace-tab.is-active::before { background: var(--selected); }
.workspace-tab > button { display: flex; align-items: center; justify-content: center; position: relative; min-width: 0; gap: 6px; padding: 0; height: 34px; background: transparent; color: inherit; border: 0; border-radius: 4px; }
.workspace-tab > button:first-child { flex: 1; }
.workspace-tab > button:focus-visible { outline-offset: -2px; }
.workspace-tab > button:first-child > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.workspace-tab svg { width: 16px; height: 16px; flex-shrink: 0; }
.workspace-tab .workspace-tab-close { width: 24px; height: 28px; flex: 0 0 24px; color: var(--muted); }
.workspace-tab-close svg { width: 14px; height: 14px; }
.workspace-tab-move { transition: transform var(--motion-normal) var(--ease-out); }
.workspace-tab-enter-active, .workspace-tab-leave-active { transition: opacity var(--motion-fast) ease, transform var(--motion-fast) var(--ease-out); }
.workspace-tab-enter-from, .workspace-tab-leave-to { opacity: 0; transform: translateY(3px); }
.workspace-tab-leave-active { position: absolute; top: 0; height: 100%; pointer-events: none; }
@media (hover: hover) { .workspace-tab:not(.is-active):hover::before { background: var(--hover); } }
.workspace-actions { display: flex; gap: 2px; align-items: center; flex-shrink: 0; }
.workspace-files-toggle { width: 76px; height: 34px; flex-shrink: 0; }
.workspace-files-toggle[aria-pressed=true] { background: var(--selected); color: var(--ink); }
.workspace-body { display: flex; min-height: 0; flex: 1; position: relative; overflow: hidden; isolation: isolate; }
.surface-tab-host { position: relative; z-index: 0; display: flex; flex-direction: column; flex: 1; min-width: 0; min-height: 0; overflow: hidden; }
.workspace-content-enter-active, .workspace-content-leave-active { transition: opacity var(--motion-fast) ease; }
.workspace-content-enter-from, .workspace-content-leave-to { opacity: 0; }
.workspace-content-leave-active { position: absolute; inset: 0; pointer-events: none; }
.workspace-directory { min-width: 0; flex-shrink: 0; min-height: 0; display: flex; }
.workspace-directory-clip { display: flex; flex: 1; min-width: 0; overflow: hidden; }
.workspace-directory-content { display: flex; flex-direction: column; width: var(--directory-width); min-height: 0; flex-shrink: 0; }
.workspace-directory-enter-active, .workspace-directory-leave-active { transition: width var(--motion-panel) var(--ease-out), opacity var(--motion-normal) ease; }
.workspace-directory-enter-from, .workspace-directory-leave-to { width: 0 !important; opacity: 0; }
.workspace-directory-leave-active, .workspace-drawer-leave-active, .workspace-backdrop-leave-active { pointer-events: none; }
.workspace-drawer-enter-active, .workspace-drawer-leave-active { transition: transform var(--motion-panel) var(--ease-out), opacity var(--motion-normal) ease; }
.workspace-drawer-enter-from, .workspace-drawer-leave-to { transform: translateX(100%); opacity: 0; }
.workspace-backdrop-enter-active, .workspace-backdrop-leave-active { transition: opacity var(--motion-normal) ease; }
.workspace-backdrop-enter-from, .workspace-backdrop-leave-to { opacity: 0; }
.workspace-directory-resize { width: 1px; flex-shrink: 0; background: var(--line); cursor: col-resize; position: relative; z-index: 1; touch-action: none; }
.workspace-directory-resize::after { content: ''; position: absolute; inset: 0 -4px; }
.workspace-directory-resize:focus-visible { outline: 1px solid var(--focus-ring); outline-offset: 2px; }
.workspace-body.is-resizing, .workspace-body.is-resizing :deep(*) { user-select: none !important; cursor: col-resize !important; }
.workspace-body.is-resizing .workspace-directory { transition: none; }
.workspace-directory-backdrop { position: absolute; inset: 0; z-index: 2; background: var(--overlay); border: 0; }
.workspace-narrow .workspace-directory { position: absolute; z-index: 3; inset: 0 0 0 auto; width: min(260px, calc(100% - 48px)) !important; background: var(--bg); border-left: 1px solid var(--panel-line); box-shadow: var(--shadow); }
.workspace-narrow .workspace-directory-content { width: 100%; }
@media (max-width: 480px) { .workspace-heading { padding: 0 6px; gap: 4px; } .workspace-actions { gap: 0; } .workspace-tab { max-width: min(160px, 100%); } }
</style>
