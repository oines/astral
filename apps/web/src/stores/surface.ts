import { ref } from 'vue'
import { defineStore } from 'pinia'
import type { FileRef } from '../lib/types'
export type Surface =
  | { kind: 'file'; id: string; sessionID: string; ref?: FileRef }
export const useSurfaceStore = defineStore('surface', () => {
  const current = ref<Surface>()
  const width = ref(520)
  function open(value: Surface) { current.value = value }
  function close() { current.value = undefined }
  return { current, width, open, close }
})
