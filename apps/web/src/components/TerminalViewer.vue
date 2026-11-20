<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useAppStore } from '../stores/app'
const props = defineProps<{ content: string; inline?: boolean; follow?: boolean }>()
const app = useAppStore()
const host = ref<HTMLElement>()
const height = ref(72)
const terminal = new Terminal({ disableStdin: true, convertEol: true, fontSize: 12, lineHeight: 1.35, fontFamily: 'SFMono-Regular, Consolas, monospace', cursorBlink: false, scrollback: 10000, theme: { background: '#212121', foreground: '#ececec', cursor: '#212121' } })
const fit = new FitAddon()
let observer: ResizeObserver | undefined
let shown = ''
function theme() {
  const style = getComputedStyle(document.documentElement)
  const background = props.inline ? style.getPropertyValue('--bg').trim() : '#212121'
  const muted = style.getPropertyValue('--muted').trim() || '#888888'
  terminal.options.theme = {
    background,
    foreground: props.inline ? style.getPropertyValue('--ink').trim() : '#ececec',
    cursor: background,
    scrollbarSliderBackground: props.inline ? `${muted}47` : '#ffffff38',
    scrollbarSliderHoverBackground: props.inline ? `${muted}7a` : '#ffffff5c',
    scrollbarSliderActiveBackground: props.inline ? `${muted}a3` : '#ffffff80',
  }
}
function fitContent() {
  fit.fit()
  if (props.inline) {
    const lines = props.content.split('\n').reduce((rows, line) => rows + Math.max(1, Math.ceil(line.length / Math.max(terminal.cols, 1))), 0)
    height.value = Math.max(52, Math.min(300, lines * 17 + 8))
    void nextTick(() => { fit.fit(); if (!props.follow) terminal.scrollToTop() })
  }
}
function update() {
  if (props.content.startsWith(shown)) terminal.write(props.content.slice(shown.length), fitContent)
  else { terminal.reset(); terminal.write(props.content, fitContent) }
  shown = props.content
  if (host.value) fitContent()
}
onMounted(() => {
  if (!host.value) return
  theme()
  terminal.loadAddon(fit)
  terminal.open(host.value)
  fit.fit()
  update()
  observer = new ResizeObserver(fitContent)
  observer.observe(host.value)
})
watch(() => props.content, update)
watch(() => app.dark, theme)
onBeforeUnmount(() => { observer?.disconnect(); terminal.dispose() })
</script>
<template>
  <div
    ref="host"
    class="terminal-viewer"
    :class="{ 'terminal-inline': inline }"
    :style="inline ? { height: `${height}px` } : undefined"
  />
</template>
<style scoped>
.terminal-viewer :deep(.xterm-scrollable-element > .shadow) { display: none !important; box-shadow: none !important; }
.terminal-viewer :deep(.xterm-scrollable-element > .scrollbar.vertical) { width: 8px !important; }
.terminal-viewer :deep(.xterm-scrollable-element > .scrollbar.vertical > .slider) { width: 4px !important; left: 2px !important; border-radius: 999px; }
.terminal-viewer :deep(.xterm-scrollable-element > .scrollbar.horizontal) { height: 8px !important; }
.terminal-viewer :deep(.xterm-scrollable-element > .scrollbar.horizontal > .slider) { height: 4px !important; top: 2px !important; border-radius: 999px; }
</style>
