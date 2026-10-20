<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as monaco from 'monaco-editor/esm/vs/editor/editor.api'
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'
import 'monaco-editor/esm/vs/basic-languages/javascript/javascript.contribution'
import 'monaco-editor/esm/vs/basic-languages/python/python.contribution'
import 'monaco-editor/esm/vs/basic-languages/shell/shell.contribution'
import 'monaco-editor/esm/vs/basic-languages/markdown/markdown.contribution'
import { useAppStore } from '../stores/app'
const props = withDefaults(defineProps<{ content: string; language?: string; original?: string }>(), { language: 'plaintext', original: undefined })
const app = useAppStore()
const host = ref<HTMLElement>()
let editor: monaco.editor.IStandaloneCodeEditor | monaco.editor.IStandaloneDiffEditor | undefined
let models: monaco.editor.ITextModel[] = []
self.MonacoEnvironment = { getWorker: () => new EditorWorker() }
function update() {
  if (!editor) return
  const previous = models
  const current = monaco.editor.createModel(props.content, props.language)
  models = [current]
  if (props.original !== undefined && 'getOriginalEditor' in editor) {
    const original = monaco.editor.createModel(props.original, props.language)
    models.push(original)
    editor.setModel({ original, modified: current })
  } else if (!('getOriginalEditor' in editor)) editor.setModel(current)
  previous.forEach(model => model.dispose())
}
onMounted(() => {
  if (!host.value) return
  const options = { readOnly: true, automaticLayout: true, minimap: { enabled: false }, fontSize: 12, scrollBeyondLastLine: false, theme: app.dark ? 'vs-dark' : 'vs', wordWrap: 'on' as const, padding: { top: 12 } }
  editor = props.original !== undefined ? monaco.editor.createDiffEditor(host.value, { ...options, renderSideBySide: false }) : monaco.editor.create(host.value, options)
  update()
})
watch(() => [props.content, props.original, props.language], update)
watch(() => app.dark, dark => monaco.editor.setTheme(dark ? 'vs-dark' : 'vs'))
onBeforeUnmount(() => { editor?.dispose(); models.forEach(model => model.dispose()) })
</script>
<template>
  <div
    ref="host"
    class="code-viewer"
  />
</template>
