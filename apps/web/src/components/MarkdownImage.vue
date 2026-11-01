<script setup lang="ts">
import { computed, inject, type ComputedRef } from 'vue'
import { ImageNode, type ImageNodeProps } from 'markstream-vue'
import { useI18n } from 'vue-i18n'
import { fileReferenceID, fileURL } from '../lib/api'
import { useImagePreviewStore } from '../stores/image-preview'

defineOptions({ inheritAttrs: false })
const props = defineProps<ImageNodeProps>()
const sessionID = inject<ComputedRef<string>>('markdownSession')
const images = useImagePreviewStore()
const { t } = useI18n()
const fileID = computed(() => fileReferenceID(props.node.src))
const node = computed(() => ({ ...props.node, src: fileID.value && sessionID?.value ? fileURL(fileID.value, sessionID.value) : props.node.src }))
function open(event: Event) {
  if (!node.value.src) return
  event.preventDefault()
  event.stopPropagation()
  images.show([{
    src: node.value.src,
    downloadUrl: fileID.value && sessionID?.value ? fileURL(fileID.value, sessionID.value, true) : node.value.src,
    name: props.node.alt || t('imagePreviewTitle'),
  }])
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
