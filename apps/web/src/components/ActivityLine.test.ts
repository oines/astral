import { describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { emptyState, type ProcessItem } from '../lib/session-state'
import ActivityLine from './ActivityLine.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('../stores/app', () => ({ useAppStore: () => ({ computers: [] }) }))
vi.mock('@astral/ui', () => ({ Collapse: { render: () => null } }))
vi.mock('./Markdown.vue', () => ({ default: { render: () => null } }))
vi.mock('./ProcessStep.vue', () => ({ default: { render: () => null } }))
vi.mock('./ToolCall.vue', async () => {
  const { h } = await import('vue')
  return { default: { render: () => h('div', { class: 'direct-tool' }) } }
})

function render(active: boolean, items: ProcessItem[], runningTool = false) {
  const state = emptyState()
  state.activityStatus = { run_id: 'run', segment: 1, state: 'working', text: 'Checking files', source: 'summarizer', seq: 1, core_sequence: 1 }
  if (runningTool) state.tools['exec'] = { name: 'read_file', text: '', started: true, ended: false }
  return renderToString(createSSRApp(ActivityLine, { sessionID: 'session', active, items, state }))
}

describe('ActivityLine summaries', () => {
  it('shows the live summary and restores the process overview on an inactive line', async () => {
    const items: ProcessItem[] = [{ kind: 'reasoning', key: 'thought', text: 'Original thought' }]
    expect(await render(true, items)).toContain('>Checking files<')
    const completed = await render(false, items)
    expect(completed).toContain('>thinkingProcess<')
    expect(completed).not.toContain('Checking files')
  })

  it('preserves the expanded control for a single directly rendered tool', async () => {
    const tool: ProcessItem = { kind: 'tool', key: 'exec', entry: {
      id: 'tool', kind: 'tool_started', sequence: 1, session_id: 'session', time: '', version: 7,
      tool: { invocation: { assistant_id: 'assistant', execution_id: 'exec', run_id: 'run', session_id: 'session', turn_id: 'turn', call: { id: 'call', name: 'read_file', arguments: {} } } },
    } }
    const html = await render(true, [tool], true)
    expect(html).toContain('class="direct-tool"')
    expect(html).not.toContain('Checking files')
  })
})
