<script setup lang="ts">
import { computed, inject, type ComputedRef } from 'vue'
import { ImageNode, type ImageNodeProps } from 'markstream-vue'
import { fileReferenceID, fileURL } from '../lib/api'
import { useSurfaceStore } from '../stores/surface'

defineOptions({ inheritAttrs: false })
const props = defineProps<ImageNodeProps>()
const sessionID = inject<ComputedRef<string>>('markdownSession')
const surface = useSurfaceStore()
const fileID = computed(() => fileReferenceID(props.node.src))
const node = computed(() => ({ ...props.node, src: fileID.value && sessionID?.value ? fileURL(fileID.value, sessionID.value) : props.node.src }))
function open(event: Event) {
  if (!fileID.value || !sessionID?.value) return
  event.preventDefault()
  event.stopPropagation()
  surface.open({ kind: 'file', id: fileID.value, sessionID: sessionID.value })
}
</script>
<template>
  <span
    @click.capture="open"
    @keydown.enter.capture="open"
    @keydown.space.capture="open"
  ><ImageNode
    :node="node"
    :fallback-src="fallbackSrc"
    :lazy="lazy"
    :use-placeholder="usePlaceholder"
    v-bind="$attrs"
  /></span>
</template>
