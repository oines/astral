<script setup lang="ts">
import { computed, onBeforeUnmount, useTemplateRef, watch } from 'vue'
import { useElementSize } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '../stores/app'
import { useSurfaceStore } from '../stores/surface'
import type { SubagentActivity } from '../lib/subagents'
import { surfaceLayout, surfaceMinimumWidth } from '../lib/surface-layout'
import { useSplitResize } from '../composables/useSplitResize'
import SurfaceWorkspace from './SurfaceWorkspace.vue'

withDefaults(defineProps<{ activities?: Record<string, SubagentActivity> }>(), { activities: () => ({}) })
const surface = useSurfaceStore()
const app = useAppStore()
const { t } = useI18n()
const panel = useTemplateRef('panel')
const { width: availableWidth } = useElementSize(() => panel.value?.parentElement)
const layout = computed(() => surfaceLayout(availableWidth.value, surface.width))
const panelWidth = computed(() => layout.value.width)
function setWidth(width: number) { surface.width = Math.max(surfaceMinimumWidth, Math.min(layout.value.maximumWidth, width)) }
const { resizing, start: resize, stop: stopResize } = useSplitResize({
  width: () => panelWidth.value,
  setWidth,
  enabled: () => !!surface.current && !layout.value.overlay,
})
watch(() => surface.rootID, stopResize)
let returnFocus: HTMLElement | undefined
watch(() => surface.current, (current, previous) => {
  if (current && !previous) returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : undefined
})
function panelClosed() {
  if (returnFocus?.isConnected && document.activeElement === document.body) returnFocus.focus({ preventScroll: true })
  returnFocus = undefined
}
function dismiss(event: KeyboardEvent) {
  if (event.key !== 'Escape' || event.defaultPrevented || !surface.current) return
  if ((event.target as Element | null)?.closest?.('[role="dialog"], [role="menu"], [role="listbox"]')) return
  event.preventDefault()
  surface.close()
}
document.addEventListener('keydown', dismiss)
onBeforeUnmount(() => document.removeEventListener('keydown', dismiss))
watch(() => app.deletedSessions, removed => { if (removed) surface.forgetSessions(removed) }, { deep: true, flush: 'sync' })
</script>
<template>
  <Transition
    name="context-panel"
    @after-leave="panelClosed"
  >
    <aside
      v-show="surface.current"
      id="context-surface"
      ref="panel"
      class="context-surface"
      :class="{ 'surface-overlay': layout.overlay }"
      :style="{ width: `${panelWidth}px`, '--panel-width': `${panelWidth}px` }"
      :aria-label="t('workspaceTabs')"
      :inert="!surface.current"
    >
      <div
        v-if="!layout.overlay"
        class="surface-resize"
        :class="{ 'is-dragging': resizing }"
        role="separator"
        tabindex="0"
        aria-orientation="vertical"
        :aria-label="t('resizeDetails')"
        :aria-valuemin="surfaceMinimumWidth"
        :aria-valuemax="Math.floor(layout.maximumWidth)"
        :aria-valuenow="Math.round(panelWidth)"
        @pointerdown="resize"
        @keydown.left.prevent="setWidth(panelWidth + 30)"
        @keydown.right.prevent="setWidth(panelWidth - 30)"
        @keydown.home.prevent="setWidth(surfaceMinimumWidth)"
        @keydown.end.prevent="setWidth(layout.maximumWidth)"
      />
      <KeepAlive
        :key="surface.epoch"
        :max="3"
      >
        <SurfaceWorkspace
          v-if="surface.rootID"
          :key="surface.rootID"
          :root-i-d="surface.rootID"
          :activities="activities"
        />
      </KeepAlive>
    </aside>
  </Transition>
</template>
