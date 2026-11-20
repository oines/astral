<script setup lang="ts">
import { computed } from 'vue'

interface DiffRow {
  kind: 'separator' | 'add' | 'del' | 'context'
  line: string
  marker: string
  text: string
}

const props = defineProps<{ patch: string }>()

const rows = computed<DiffRow[]>(() => {
  const lines = props.patch.replace(/\r\n/g, '\n').split('\n')
  if (lines.at(-1) === '') lines.pop()
  const out: DiffRow[] = []
  let oldNum = 0
  let newNum = 0
  let inHunk = false
  let seenHunk = false

  for (const line of lines) {
    const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line)
    if (hunk) {
      if (seenHunk && out.length) {
        out.push({ kind: 'separator', line: '', marker: '', text: '' })
      }
      oldNum = Number(hunk[1])
      newNum = Number(hunk[2])
      inHunk = true
      seenHunk = true
      continue
    }
    if (!inHunk && /^(diff --git |index |--- |\+\+\+ |\*\*\* )/.test(line)) {
      continue
    }
    if (line.startsWith('+') && !line.startsWith('+++')) {
      out.push({ kind: 'add', line: inHunk ? String(newNum++) : '', marker: '+', text: line.slice(1) })
    } else if (line.startsWith('-') && !line.startsWith('---')) {
      out.push({ kind: 'del', line: inHunk ? String(oldNum++) : '', marker: '-', text: line.slice(1) })
    } else if (line.startsWith('\\')) {
      continue
    } else {
      const content = line.startsWith(' ') ? line.slice(1) : line
      const num = inHunk ? String(newNum++) : ''
      if (inHunk) oldNum++
      out.push({ kind: 'context', line: num, marker: ' ', text: content })
    }
  }
  return out
})

const hasLineNumbers = computed(() => rows.value.some(row => row.line))
</script>
<template>
  <div
    class="inline-diff"
    role="region"
    aria-label="Diff"
  >
    <div class="inline-diff-table">
      <template
        v-for="(row, index) in rows"
        :key="index"
      >
        <div
          v-if="row.kind === 'separator'"
          class="inline-diff-separator"
          aria-hidden="true"
        />
        <div
          v-else
          class="inline-diff-row"
          :class="`inline-diff-${row.kind}`"
        >
          <span
            v-if="hasLineNumbers"
            class="inline-diff-gutter"
            aria-hidden="true"
          >{{ row.line }}</span>
          <span
            class="inline-diff-marker"
            aria-hidden="true"
          >{{ row.marker }}</span>
          <span class="inline-diff-code">{{ row.text }}</span>
        </div>
      </template>
    </div>
  </div>
</template>
