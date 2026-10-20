import { ref } from 'vue'
import { defineStore } from 'pinia'
import type { Entry, FileRef } from '../lib/types'
export type Surface =
  | { kind: 'file'; id: string; sessionID: string; ref?: FileRef }
  | { kind: 'trace'; runID: string; sessionID: string }
  | { kind: 'entry'; entry: Entry; sessionID: string }
  | { kind: 'json'; title: string; data: unknown }
export const useSurfaceStore = defineStore('surface', () => {
  const current = ref<Surface>()
  const width = ref(520)
  function open(value: Surface) { current.value = value }
  function close() { current.value = undefined }
  return { current, width, open, close }
})
