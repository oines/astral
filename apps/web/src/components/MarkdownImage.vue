<script setup lang="ts">
import { computed, inject } from 'vue'
import { ImageNode, type ImageNodeProps } from 'markstream-vue'
import { useI18n } from 'vue-i18n'
import { markdownSource } from '../lib/markdown'
import { parseComputerFile, previewFileURL } from '../lib/computer-file'
import { fileReferenceID, fileURL } from '../lib/api'
import { useImagePreviewStore } from '../stores/image-preview'

defineOptions({ inheritAttrs: false })
const props = defineProps<ImageNodeProps>()
const source = inject(markdownSource, undefined)
const images = useImagePreviewStore()
const { t } = useI18n()
function imageURL(download = false) {
  const src = props.node.src
  if (!src || props.node.loading) return ''
  if (src.startsWith('astral-file:')) {
    if (!source?.value.assistantReply) return ''
    const file = parseComputerFile(src)
    if (file) return previewFileURL(file, source.value.sessionID, download)
    const id = fileReferenceID(src)
    return id ? fileURL(id, source.value.sessionID, download) : ''
  }
  // Raw relative/same-origin API URLs in untrusted Markdown must not initiate
  // authenticated file reads. Uploaded attachments render outside Markdown.
  if (!source?.value.assistantReply) {
    const base = typeof window === 'undefined' ? 'http://localhost' : window.location.href
    try {
      const url = new URL(src, base)
      if (url.origin === new URL(base).origin && /^\/+api(?:\/|$)/.test(decodeURIComponent(url.pathname))) return ''
    } catch { return '' }
  }
  return src
}
const node = computed(() => ({ ...props.node, src: imageURL() }))
function open(event: Event) {
  if (!node.value.src) return
  event.preventDefault()
  event.stopPropagation()
  images.show([{
    src: node.value.src,
    downloadUrl: imageURL(true),
    name: props.node.alt || t('imagePreviewTitle'),
  }])
}
</script>
<template>
  <span v-if="!node.src">{{ node.alt }}</span>
  <span
    v-else
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
