<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
const props = defineProps<{ content: string }>()
const host = ref<HTMLElement>()
const terminal = new Terminal({ disableStdin: true, convertEol: true, fontSize: 12, fontFamily: 'SFMono-Regular, Consolas, monospace', cursorBlink: false, scrollback: 10000, theme: { background: '#20251f', foreground: '#e1e9dc', cursor: '#20251f' } })
const fit = new FitAddon()
let observer: ResizeObserver | undefined
let shown = ''
function update() {
  if (props.content.startsWith(shown)) terminal.write(props.content.slice(shown.length))
  else { terminal.reset(); terminal.write(props.content) }
  shown = props.content
}
onMounted(() => {
  if (!host.value) return
  terminal.loadAddon(fit)
  terminal.open(host.value)
  fit.fit()
  update()
  observer = new ResizeObserver(() => fit.fit())
  observer.observe(host.value)
})
watch(() => props.content, update)
onBeforeUnmount(() => { observer?.disconnect(); terminal.dispose() })
</script>
<template>
  <div
    ref="host"
    class="terminal-viewer"
  />
</template>
