import { ref, shallowRef } from 'vue'
import { defineStore } from 'pinia'
import type { ImageViewerItem } from '@astral/ui'

export const useImagePreviewStore = defineStore('image-preview', () => {
  const items = shallowRef<ImageViewerItem[]>([])
  const index = ref(0)
  const open = ref(false)
  function show(images: ImageViewerItem[], selected = 0) {
    if (!images.length) return
    items.value = images
    index.value = Math.max(0, Math.min(images.length - 1, selected))
    open.value = true
  }
  function close() {
    open.value = false
    items.value = []
    index.value = 0
  }
  return { items, index, open, show, close }
})
