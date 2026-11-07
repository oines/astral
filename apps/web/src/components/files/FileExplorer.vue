<script setup lang="ts">
import { computed, nextTick, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Menu, MenuItem, useDelayedPending } from '@astral/ui'
import { ArrowUp, ChevronRight, Copy, Download, Ellipsis, ExternalLink, File, FolderOpen, RefreshCw } from '@astral/icons'
import type { FileRef } from '../../lib/types'
import { useClipboard } from '../../composables/useClipboard'
import { useFileExplorer, type FileExplorerSource, type FileTreeRow } from '../../composables/useFileExplorer'
import { filePathName, type DirectoryEntry } from '../../composables/useFileService'
import { useFileReveal } from '../../composables/useFileReveal'
import { revealOverflowLabel } from '../../lib/overflow-label'
import { errorText } from '../../lib/api'
import { downloadFileReference } from '../../lib/file-download'

const props = withDefaults(defineProps<{ source: FileExplorerSource; sessionID: string; computerName?: string }>(), { computerName: '' })
const emit = defineEmits<{ openFile: [file: FileRef] }>()
const { t } = useI18n()
const tree = useFileExplorer(() => props.source, () => props.sessionID)
const { root, rootPath, rows, locating, refreshing, error, openingPath } = tree
const waiting = useDelayedPending(() => locating.value ? `${props.sessionID}:${props.source.computer_id}:${props.source.path}` : false)
const opening = useDelayedPending(() => openingPath.value)
const host = ref<HTMLElement>()
const animateRows = ref(false)
watch([locating, refreshing], ([locating, refreshing]) => { if (locating || refreshing) animateRows.value = false }, { flush: 'sync' })
const focusPath = ref('')
const firstPath = computed(() => rows.value.find(row => row.kind === 'entry')?.key)
watch(rows, value => { if (focusPath.value && !value.some(row => row.kind === 'entry' && row.entry.path === focusPath.value)) focusPath.value = '' })
const menuOpen = ref(false)
const menuEntry = shallowRef<DirectoryEntry>()
const menuPosition = ref({ left: '0px', top: '0px' })
let menuInvoker: HTMLElement | undefined
const { copy } = useClipboard()
const native = useFileReveal(() => menuOpen.value && menuEntry.value ? { computerID: props.source.computer_id, sessionID: props.sessionID, path: menuEntry.value.path, directory: menuEntry.value.is_dir } : undefined)
watch(menuOpen, async open => {
  if (!open) { await nextTick(); if (!menuOpen.value) menuInvoker?.focus({ preventScroll: true }) }
})
watch([() => props.source.computer_id, () => props.sessionID], () => { menuOpen.value = false; focusPath.value = '' })

async function open(entry: DirectoryEntry) {
  if (entry.is_dir) animateRows.value = true
  const file = await tree.open(entry)
  if (file) emit('openFile', file)
}
function toggle(path: string) { animateRows.value = true; return tree.toggle(path) }
function beforeRowLeave(element: Element) {
  const row = element as HTMLElement
  row.style.width = `${row.getBoundingClientRect().width}px`
  row.style.left = `${row.offsetLeft}px`
  row.style.top = `${row.offsetTop}px`
  row.inert = true
  row.setAttribute('aria-hidden', 'true')
}
function restoreRow(element: Element) {
  const row = element as HTMLElement
  row.style.removeProperty('width'); row.style.removeProperty('left'); row.style.removeProperty('top')
  row.inert = false
  row.removeAttribute('aria-hidden')
}
async function download(entry: DirectoryEntry) {
  if (entry.is_dir) return
  const sessionID = props.sessionID
  const file = await tree.open(entry)
  if (!file) return
  try { downloadFileReference(file, sessionID) }
  catch (cause) { error.value = errorText(cause) }
}
function actions(event: MouseEvent | KeyboardEvent, entry: DirectoryEntry) {
  event.preventDefault()
  menuInvoker = event.currentTarget instanceof HTMLElement ? event.currentTarget : undefined
  const rect = menuInvoker?.getBoundingClientRect()
  menuPosition.value = { left: `${rect?.right || 0}px`, top: `${rect?.top || 0}px` }
  menuEntry.value = entry
  menuOpen.value = true
}
function preventSecondaryMenuRelease(event: PointerEvent) {
  // Reka selects an item on pointerup when the press started outside it. A
  // context menu can cover its opening pointer after collision positioning;
  // releasing that secondary button must not synthesize an item click.
  if (event.button !== 0 || event.ctrlKey) event.preventDefault()
}
function items() { return [...(host.value?.querySelectorAll<HTMLButtonElement>('[role="treeitem"]') || [])].filter(item => !item.closest('[inert]')) }
function focus(path: string) { items().find(item => item.dataset.path === path)?.focus({ preventScroll: false }) }
async function navigate(event: KeyboardEvent, row: Extract<FileTreeRow, { kind: 'entry' }>) {
  const list = items(), index = list.indexOf(event.currentTarget as HTMLButtonElement)
  const next = { ArrowDown: index + 1, ArrowUp: index - 1, Home: 0, End: list.length - 1 }[event.key]
  if (next !== undefined) {
    event.preventDefault(); list[Math.max(0, Math.min(list.length - 1, next))]?.focus(); return
  }
  if (event.key === 'ArrowLeft') {
    event.preventDefault()
    if (row.entry.is_dir && row.directory.expanded) await toggle(row.entry.path)
    else focus(row.parent)
  } else if (event.key === 'ArrowRight' && row.entry.is_dir) {
    event.preventDefault()
    if (!row.directory.expanded) await toggle(row.entry.path)
    else {
      await nextTick()
      const child = rows.value.find(value => value.kind === 'entry' && value.parent === row.entry.path)
      if (child?.kind === 'entry') focus(child.entry.path)
    }
  } else if (event.key === 'F10' && event.shiftKey || event.key === 'ContextMenu') actions(event, row.entry)
}
</script>
<template>
  <section
    class="file-explorer"
    :aria-label="t('filesDirectory')"
    :aria-busy="locating || refreshing || undefined"
  >
    <p class="file-explorer-computer small muted">
      {{ computerName || source.computer_id }}
    </p>
    <div class="file-explorer-toolbar">
      <Button
        variant="ghost"
        size="icon"
        :disabled="!root?.page?.parent || locating"
        :aria-label="t('filesParent')"
        @click="tree.parent"
      >
        <ArrowUp />
      </Button>
      <span
        class="file-explorer-path mono"
        :title="rootPath || source.path"
      >{{ rootPath || filePathName(source.path) }}</span>
      <Button
        variant="ghost"
        size="icon"
        :busy="refreshing"
        :disabled="locating"
        :aria-label="t('filesRefresh')"
        @click="tree.refresh"
      >
        <RefreshCw />
      </Button>
    </div>
    <div
      v-if="error || native.error.value"
      class="file-explorer-error"
      role="alert"
    >
      <p class="error-box">
        {{ error || native.error.value }}
      </p>
      <Button
        v-if="error"
        size="small"
        @click="tree.refresh"
      >
        {{ t('retry') }}
      </Button>
    </div>
    <div
      v-if="waiting && !root?.page"
      class="file-explorer-loading"
      role="status"
      :aria-label="t('loading')"
    >
      <span
        class="spinner"
        aria-hidden="true"
      />
    </div>
    <div
      ref="host"
      class="file-explorer-tree"
      role="tree"
      :aria-label="t('filesDirectory')"
    >
      <TransitionGroup
        name="file-tree"
        :css="animateRows"
        @before-enter="restoreRow"
        @before-leave="beforeRowLeave"
        @after-leave="restoreRow"
        @leave-cancelled="restoreRow"
      >
        <template
          v-for="row in rows"
          :key="row.key"
        >
          <div
            v-if="row.kind === 'entry'"
            class="file-explorer-row"
            :class="{ selected: row.entry.path === source.path }"
            :style="{ '--file-depth': row.depth }"
          >
            <button
              type="button"
              role="treeitem"
              class="file-explorer-entry"
              :data-path="row.entry.path"
              :tabindex="focusPath ? (focusPath === row.entry.path ? 0 : -1) : (row.key === firstPath ? 0 : -1)"
              :aria-level="row.depth + 1"
              :aria-expanded="row.entry.is_dir ? row.directory.expanded : undefined"
              :aria-selected="row.entry.path === source.path"
              :aria-busy="row.directory.busy || openingPath === row.entry.path || undefined"
              @focus="focusPath = row.entry.path; revealOverflowLabel($event, '.file-explorer-name')"
              @pointerenter="revealOverflowLabel($event, '.file-explorer-name')"
              @click="open(row.entry)"
              @keydown="navigate($event, row)"
              @contextmenu="actions($event, row.entry)"
            >
              <span
                class="file-explorer-disclosure"
                aria-hidden="true"
              >
                <span
                  v-if="row.directory.waiting || opening && openingPath === row.entry.path"
                  class="spinner"
                />
                <ChevronRight
                  v-else-if="row.entry.is_dir"
                  class="file-explorer-chevron"
                />
              </span>
              <FolderOpen
                v-if="row.entry.is_dir"
                class="file-explorer-kind"
                aria-hidden="true"
              /><File
                v-else
                class="file-explorer-kind"
                aria-hidden="true"
              />
              <span class="file-explorer-name">{{ row.entry.name }}</span>
            </button>
            <Button
              class="file-explorer-actions"
              :tabindex="focusPath === row.entry.path ? 0 : -1"
              variant="ghost"
              size="icon"
              :aria-label="t('filesActions', { name: row.entry.name })"
              @click="actions($event, row.entry)"
            >
              <Ellipsis />
            </Button>
          </div>
          <div
            v-else
            class="file-explorer-detail"
            :style="{ '--file-depth': row.depth }"
          >
            <p
              v-if="row.kind === 'empty'"
              class="muted small"
            >
              {{ t('filesEmptyDirectory') }}
            </p>
            <template v-else-if="row.kind === 'error'">
              <p
                class="error-box"
                role="alert"
              >
                {{ row.directory.error }}
              </p><Button
                size="small"
                @click="tree.retry(row.path)"
              >
                {{ t('retry') }}
              </Button>
            </template>
            <Button
              v-else
              variant="ghost"
              size="small"
              :busy="row.directory.busy"
              @click="tree.more(row.path)"
            >
              {{ t('filesLoadMore') }}
            </Button>
          </div>
        </template>
      </TransitionGroup>
    </div>
    <Menu
      v-model:open="menuOpen"
      :label="t('filesActions', { name: menuEntry?.name || '' })"
      side="right"
      :side-offset="4"
      class="file-explorer-menu"
      @pointerup.capture="preventSecondaryMenuRelease"
      @close-auto-focus.prevent
    >
      <template #trigger>
        <button
          type="button"
          class="file-explorer-menu-anchor"
          :style="menuPosition"
          tabindex="-1"
          aria-hidden="true"
        />
      </template>
      <MenuItem
        v-if="menuEntry && !menuEntry.is_dir"
        class="menu-item"
        @select="download(menuEntry)"
      >
        <Download />{{ t('download') }}
      </MenuItem>
      <MenuItem
        v-if="native.available.value"
        class="menu-item"
        @select="native.reveal"
      >
        <ExternalLink />{{ t(native.labelKey.value) }}
      </MenuItem>
      <MenuItem
        v-if="menuEntry"
        class="menu-item"
        @select="copy(menuEntry.path)"
      >
        <Copy />{{ t('filesCopyPath') }}
      </MenuItem>
    </Menu>
  </section>
</template>
<style scoped>
.file-explorer { display: flex; flex-direction: column; min-width: 0; min-height: 0; height: 100%; }
.file-explorer-computer { margin: 0; padding: 16px 12px 0; overflow-wrap: anywhere; flex-shrink: 0; }
.file-explorer-toolbar { display: flex; align-items: center; gap: 2px; padding: 8px 8px; flex-shrink: 0; }
.file-explorer-path { color: var(--muted); font-size: 11px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex: 1; min-width: 0; }
.file-explorer-tree { position: relative; overflow: auto; scrollbar-width: thin; flex: 1; min-height: 0; padding: 0 8px 16px; }
.file-explorer-row { display: flex; align-items: center; min-height: 36px; margin: 2px 0; border-radius: 6px; min-width: 0; transition: background-color var(--motion-fast) ease; }
.file-explorer-row:hover, .file-explorer-row:focus-within { background: var(--hover); }
.file-explorer-row.selected { background: var(--selected); }
.file-explorer-entry { display: flex; align-items: center; gap: 6px; padding: 6px 4px 6px calc(4px + var(--file-depth) * 16px); border: 0; background: transparent; color: var(--ink); text-align: left; min-height: 36px; flex: 1; min-width: 0; font-size: 13px; line-height: 20px; border-radius: 6px; }
.file-explorer-entry:focus-visible { outline: 1px solid var(--focus-ring); outline-offset: -1px; }
.file-explorer-disclosure { width: 12px; height: 14px; display: grid; place-items: center; flex-shrink: 0; color: var(--muted); }
.file-explorer-disclosure > svg { width: 12px; height: 12px; }
.file-explorer-chevron { transition: transform var(--motion-fast) var(--ease-out); }
.file-explorer-entry[aria-expanded=true] .file-explorer-chevron { transform: rotate(90deg); }
.file-explorer-disclosure .spinner { width: 12px; height: 12px; }
.file-explorer-kind { width: 16px; height: 16px; flex-shrink: 0; color: var(--muted); }
.file-explorer-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-explorer-actions { opacity: 0; flex-shrink: 0; width: 28px; height: 28px; min-height: 28px; padding: 6px; margin-right: 4px; border-radius: 4px; }
.file-explorer-row:hover .file-explorer-actions, .file-explorer-row:focus-within .file-explorer-actions, .file-explorer-actions:focus-visible { opacity: 1; }
.file-explorer-detail { padding: 6px 8px 6px calc(46px + var(--file-depth) * 16px); display: flex; flex-direction: column; gap: 6px; }
.file-explorer-detail p { margin: 0; overflow-wrap: anywhere; }
.file-explorer-detail .button { align-self: flex-start; }
.file-explorer-error { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; padding: 4px 12px 12px; }
.file-explorer-error p { margin: 0; overflow-wrap: anywhere; }
.file-explorer-loading { display: grid; place-items: center; min-height: 80px; }
.file-explorer-menu-anchor { position: fixed; width: 1px; height: 1px; padding: 0; border: 0; opacity: 0; pointer-events: none; }
.file-tree-enter-active, .file-tree-leave-active, .file-tree-move { transition: opacity var(--motion-fast) ease, transform var(--motion-fast) var(--ease-out); }
.file-tree-enter-from, .file-tree-leave-to { opacity: 0; transform: translateY(-3px); }
.file-tree-leave-active { position: absolute; margin: 0; pointer-events: none; }
@media (prefers-reduced-motion: reduce) { .file-explorer-row, .file-explorer-chevron, .file-tree-enter-active, .file-tree-leave-active, .file-tree-move { transition: none; } }
@media (hover: none), (pointer: coarse) { .file-explorer-actions { opacity: 1; } }
@media (any-pointer: coarse) { .file-explorer-actions { width: 32px; height: 32px; min-height: 32px; padding: 8px; } }
</style>
