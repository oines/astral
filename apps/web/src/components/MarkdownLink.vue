<script setup lang="ts">
import { inject, type ComputedRef } from 'vue'
import { LinkNode, type LinkNodeProps } from 'markstream-vue'
import { useSurfaceStore } from '../stores/surface'
import { fileReferenceID } from '../lib/api'
defineOptions({ inheritAttrs: false })
const props = defineProps<{ node: LinkNodeProps['node']; indexKey: LinkNodeProps['indexKey'] }>()
const sessionID = inject<ComputedRef<string>>('markdownSession')
const surface = useSurfaceStore()
function open(event: MouseEvent) {
  const href = props.node.href
  if (!href) return
  if (href.startsWith('astral-file:')) {
    event.preventDefault()
    event.stopPropagation()
    const id = fileReferenceID(href)
    if (id && sessionID?.value) surface.open({ kind: 'file', id, sessionID: sessionID.value })
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
