import { describe, expect, it } from 'vitest'
import type { Entry } from './types'
import { agentResultStatus, controlledAgentDetails, reasoningDetails, sentMessageDetails, toolPresentation, toolResultImages, toolResultText } from './tool-presentation'

function entry(name: string, args: unknown, data?: unknown): Entry {
  return {
    id: 'tool-result', sequence: 1, version: 1, session_id: 'session', time: '', kind: 'tool_result',
    tool: {
      invocation: { session_id: 'session', run_id: 'run', turn_id: 'turn', assistant_id: 'assistant', execution_id: 'execution', call: { id: 'call', name, arguments: args } },
      ...(data === undefined ? {} : { result: { outcome: 'succeeded', content: [{ type: 'text', text: JSON.stringify(data) }] } }),
    },
  }
}

describe('persisted tool presentation', () => {
  it('uses image ordinals and the frozen result Entry for read images', () => {
    const read = entry('read', { path: '/tmp/picture.png' })
    read.tool!.result = { outcome: 'succeeded', content: [{ type: 'text', text: 'Read image' }, { type: 'image', url: 'astral-file://computer/file', metadata: { width: 160, height: 100 } }, { type: 'text', text: 'Second image' }, { type: 'image' }] }
    expect(toolPresentation(read)).toMatchObject({ label: 'readFile', target: '/tmp/picture.png' })
    expect(toolResultImages(read, 'readonly child')).toMatchObject([
      { index: 0, src: '/api/v1/sessions/readonly%20child/entries/tool-result/images/0' },
      { index: 1, src: '/api/v1/sessions/readonly%20child/entries/tool-result/images/1' },
    ])
  })

  it('keeps JSON file contents and unknown tools available as ordinary result text', () => {
    const raw = '{"a":1}'
    const read = entry('read', { path: '/tmp/config.json' })
    read.tool!.result = { outcome: 'succeeded', content: [{ type: 'text', text: raw }] }
    expect(toolResultText(read)).toBe(raw)
    expect(toolPresentation(entry('custom_tool', { path: '/tmp/custom' }))).toMatchObject({ name: 'custom_tool', label: undefined, target: '/tmp/custom' })
  })
})

describe('agent_control details', () => {
  it('distinguishes list, query and combined model/task operations', () => {
    expect(controlledAgentDetails(entry('agent_control', {}, [])).actions).toEqual(['toolAgentList'])
    expect(controlledAgentDetails(entry('agent_control', {}, [])).emptyList).toBe(true)
    expect(controlledAgentDetails(entry('agent_control', { agent_id: 'child' })).actions).toEqual(['toolAgentQuery'])
    const detail = controlledAgentDetails(entry('agent_control', { agent_id: 'child', input: 'Keep the exact\nfollow-up text.', model: 'profile', reasoning: {} }, { session_id: 'child', name: 'Analysis', status: 'queued', selection: { id: 'profile', reasoning: {} } }))
    expect(detail).toMatchObject({ targetID: 'child', targetName: 'Analysis', input: 'Keep the exact\nfollow-up text.', model: 'profile', reasoning: { mode: 'api' } })
    expect(detail.actions).toEqual(['toolAgentInput', 'toolAgentModel'])
  })

  it('reads Details arrays once and retains all returned agents', () => {
    const result = entry('agent_control', {}, [{ session_id: 'stale' }])
    result.tool!.result!.details = [{ session_id: 'one', name: 'One', status: 'running' }, { session_id: 'two', title: 'Old title', status: 'failed', error: 'Provider unavailable' }]
    expect(controlledAgentDetails(result).agents.map(agent => agent.session_id)).toEqual(['one', 'two'])
  })

  it('does not confuse a stop request with a stopped run or a delete request with deletion', () => {
    const stopped = controlledAgentDetails(entry('agent_control', { agent_id: 'child', signal: 'stop' }, { session_id: 'child', status: 'stopping' }))
    expect(stopped.actions).toEqual(['toolAgentStop'])
    expect(agentResultStatus(stopped.agents[0]!)).toEqual({ key: 'stopping', value: 'stopping' })
    expect(agentResultStatus({ session_id: 'child' })).toEqual({ key: undefined, value: '' })
    expect(agentResultStatus({ session_id: 'child', deleted: true })).toEqual({ key: 'subagentDeleted', value: '' })
  })

  it('distinguishes omitted reasoning, API defaults, false and a zero token budget', () => {
    expect(reasoningDetails(undefined, false)).toEqual({ mode: 'profile' })
    expect(reasoningDetails(null, true)).toEqual({ mode: 'profile' })
    expect(reasoningDetails({}, true).mode).toBe('api')
    expect(reasoningDetails({ enabled: false, budget_tokens: 0 }, true)).toMatchObject({ mode: 'custom', enabled: false, budget: 0 })
  })

  it('retains tool errors without claiming a model/task operation had no side effects', () => {
    const failed = entry('agent_control', { agent_id: 'child', model: 'profile', input: 'New task' })
    failed.tool!.result = { outcome: 'not_executed', is_error: true, code: 'subagent_control', content: [{ type: 'text', text: 'Task admission failed after selection changed.' }] }
    expect(controlledAgentDetails(failed)).toMatchObject({ error: 'Task admission failed after selection changed.', actions: ['toolAgentInput', 'toolAgentModel'] })
  })
})

describe('send_message facts', () => {
  it.each([
    [true, 'toolMessageSent'], [false, 'toolMessageFailed'], [null, 'toolMessageUnknown'],
  ])('uses sent=%s independently of IDs and the tool outcome', (sent, status) => {
    const message = entry('send_message', { text: 'Actual body' }, { sent, message_ids: ['platform-returned-id'], ...(sent === true ? {} : { error: 'A platform error' }) })
    if (sent !== true) { message.tool!.result!.outcome = 'failed'; message.tool!.result!.is_error = true }
    expect(sentMessageDetails(message)).toMatchObject({ status, messageIDs: ['platform-returned-id'], text: 'Actual body' })
  })

  it('keeps the explicit sent boolean authoritative over a generic unknown outcome', () => {
    const message = entry('send_message', { text: 'Actual body' }, { sent: false })
    message.tool!.result!.outcome = 'unknown'
    expect(sentMessageDetails(message).status).toBe('toolMessageFailed')
  })

  it.each([
    [{ status: 'queued' }, 'toolMessageQueued'], [{ status: 'accepted' }, 'toolMessageAccepted'], [{ accepted: true }, 'toolMessageAccepted'], [{ message_ids: ['id'] }, 'toolMessageUnknown'],
  ])('never treats acceptance, queuing, or a message ID as sent', (data, status) => {
    expect(sentMessageDetails(entry('send_message', { text: '' }, data)).status).toBe(status)
  })

  it('preserves the actual bound target and ordered native content without inventing separators', () => {
    const message = entry('send_message', { parts: [{ type: 'text', text: 'one ' }, { type: 'mention', user_id: '42', name: 'Name' }, { type: 'text', text: '\nnext' }, { type: 'image', file_uri: 'astral-file://computer/image' }, { type: 'file', path: '/tmp/report.pdf' }], reply_to: 'prior-message', format: 'markdown' }, { sent: true })
    message.tool!.invocation.binding = { conversation_id: 'original-chat', binding_id: 'original-binding' }
    expect(sentMessageDetails(message)).toMatchObject({ target: { conversation_id: 'original-chat', binding_id: 'original-binding' }, replyTo: 'prior-message', parts: [{ type: 'text', text: 'one ' }, { type: 'mention', user_id: '42' }, { type: 'text', text: '\nnext' }, { type: 'image' }, { type: 'file' }] })
  })

  it('preserves attachment paths and errors without guessing file types', () => {
    const message = entry('send_message', { text: '', attachments: ['/tmp/photo.png', 'astral-file://computer/report'] }, { sent: false, error: 'Upload rejected' })
    expect(sentMessageDetails(message)).toMatchObject({ attachments: ['/tmp/photo.png', 'astral-file://computer/report'], status: 'toolMessageFailed', error: 'Upload rejected' })
  })
})
