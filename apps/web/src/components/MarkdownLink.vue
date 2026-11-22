<script setup lang="ts">
import { inject } from 'vue'
import { LinkNode, type LinkNodeProps } from 'markstream-vue'
import { useSurfaceStore } from '../stores/surface'
import { markdownSource } from '../lib/markdown'
import { parseComputerFile } from '../lib/computer-file'
import { fileReferenceID } from '../lib/api'
defineOptions({ inheritAttrs: false })
const props = defineProps<{ node: LinkNodeProps['node']; indexKey: LinkNodeProps['indexKey'] }>()
const source = inject(markdownSource, undefined)
const surface = useSurfaceStore()
function open(event: MouseEvent) {
  const href = props.node.href
  if (!href) return
  if (href.startsWith('astral-file:')) {
    event.preventDefault()
    event.stopPropagation()
    const path = parseComputerFile(href)
    if (path) {
      if (!props.node.loading && source?.value.assistantReply) surface.openPath(path, source.value.sessionID)
      return
    }
    const id = fileReferenceID(href)
    if (id && source?.value.sessionID) surface.open({ kind: 'file', id, sessionID: source.value.sessionID })
  } else if (!/^(https?:|mailto:|#)/i.test(href)) {
    event.preventDefault()
    event.stopPropagation()
  } else if (window.astralDesktop && /^https?:/i.test(href)) {
    event.preventDefault()
    void window.astralDesktop.openExternal(href)
  }
}
</script>
<template>
  <span @click.capture="open"><LinkNode
    :node="node"
    :index-key="indexKey"
    :aria-label="node.text || node.href"
    v-bind="$attrs"
  /></span>
</template>
