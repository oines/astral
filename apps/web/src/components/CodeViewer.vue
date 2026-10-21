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
function applyTheme() {
  const style = getComputedStyle(document.documentElement)
  const color = (token: string) => style.getPropertyValue(token).trim()
  monaco.editor.defineTheme('astral-preview', {
    base: app.dark ? 'vs-dark' : 'vs',
    inherit: true,
    rules: [],
    colors: {
      focusBorder: color('--focus-ring'),
      'editor.background': color('--bg'),
      'editor.foreground': color('--ink'),
      'editorLineNumber.foreground': color('--subtle'),
      'editor.lineHighlightBackground': color('--surface'),
      'editor.lineHighlightBorder': '#00000000',
      'editorWidget.background': color('--raised'),
      'editorWidget.border': color('--field-line'),
      'input.background': color('--raised'),
      'input.foreground': color('--ink'),
      'input.border': color('--field-line'),
      'inputOption.activeBorder': color('--focus-ring'),
      'inputOption.activeBackground': color('--selected'),
      'inputOption.activeForeground': color('--ink'),
      'list.focusOutline': color('--focus-ring'),
    },
  })
  monaco.editor.setTheme('astral-preview')
}
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
  applyTheme()
  const options = { readOnly: true, automaticLayout: true, minimap: { enabled: false }, fontSize: 12, scrollBeyondLastLine: false, theme: 'astral-preview', wordWrap: 'on' as const, padding: { top: 12 } }
  editor = props.original !== undefined ? monaco.editor.createDiffEditor(host.value, { ...options, renderSideBySide: false }) : monaco.editor.create(host.value, options)
  update()
})
watch(() => [props.content, props.original, props.language], update)
watch(() => app.dark, applyTheme, { flush: 'post' })
onBeforeUnmount(() => { editor?.dispose(); models.forEach(model => model.dispose()) })
</script>
<template>
  <div
    ref="host"
    class="code-viewer"
  />
</template>
