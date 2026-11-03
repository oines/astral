import type { Content, Entry } from './types'
import { imageURL, object } from './api'

function text(value: unknown) { return typeof value === 'string' ? value : '' }

function json(value: string): unknown {
  try { return JSON.parse(value) }
  catch { return undefined }
}

export function toolResultText(entry: Entry) {
  return entry.tool?.result?.content?.filter(part => part.type === 'text').map(part => part.text || '').join('\n') || ''
}

// Host-owned tools can persist the same response in Details and text. Read it
// once; printing both would duplicate the result and expose JSON as Markdown.
export function toolResultData(entry: Entry): unknown {
  const details = entry.tool?.result?.details
  if (details !== undefined && details !== null) return details
  return json(toolResultText(entry))
}

// Older versions of a known tool may have returned plain text. Keep that
// evidence readable when a specialized view cannot interpret it as data.
export function toolResultFallbackText(entry: Entry) {
  const raw = toolResultText(entry)
  const parsed = json(raw)
  return parsed !== null && typeof parsed === 'object' ? '' : raw
}

export function toolResultError(entry: Entry) {
  const result = entry.tool?.result
  const data = object(toolResultData(entry))
  if (typeof data.error === 'string' && data.error) return data.error
  if (!result?.is_error && !['failed', 'not_executed', 'unknown'].includes(result?.outcome || '')) return ''
  return toolResultText(entry) || result?.code || ''
}

export function toolResultImages(entry: Entry, sessionID: string) {
  // The media endpoint numbers image parts, rather than all content parts.
  return (entry.tool?.result?.content || []).filter(part => part.type === 'image').map((content, index) => ({
    content,
    index,
    src: imageURL(sessionID, entry.id, index),
  }))
}

export function imageDimensions(image: Content) {
  const metadata = object(image.metadata)
  const width = Number(metadata.width)
  const height = Number(metadata.height)
  return { width: width > 0 ? width : undefined, height: height > 0 ? height : undefined }
}

export function reasoningDetails(value: unknown, supplied: boolean) {
  if (!supplied || value == null) return { mode: 'profile' as const }
  const options = object(value)
  return {
    mode: Object.keys(options).length ? 'custom' as const : 'api' as const,
    enabled: typeof options.enabled === 'boolean' ? options.enabled : undefined,
    effort: text(options.effort),
    budget: typeof options.budget_tokens === 'number' ? options.budget_tokens : undefined,
  }
}

export function controlledAgentDetails(entry: Entry) {
  const args = object(entry.tool?.invocation.call.arguments)
  const data = toolResultData(entry)
  const actions = args.signal === 'stop' ? ['toolAgentStop'] : args.signal === 'delete' ? ['toolAgentDelete'] : [
    ...(args.input ? ['toolAgentInput'] : []),
    ...(args.model ? ['toolAgentModel'] : []),
  ]
  if (!actions.length) actions.push(args.agent_id ? 'toolAgentQuery' : 'toolAgentList')
  const agents = (Array.isArray(data) ? data : [data]).map(object).filter(item => typeof item.session_id === 'string' && item.session_id)
  const targetID = text(args.agent_id)
  const target = agents.find(agent => agent.session_id === targetID)
  return {
    actions,
    targetID,
    targetName: text(target?.name) || text(target?.title),
    input: text(args.input),
    model: text(args.model),
    reasoning: reasoningDetails(args.reasoning, Object.hasOwn(args, 'reasoning')),
    agents,
    emptyList: Array.isArray(data) && !data.length,
    fallback: toolResultFallbackText(entry),
    error: toolResultError(entry),
  }
}

export function agentResultStatus(agent: Record<string, unknown>) {
  if (agent.deleted === true) return { key: 'subagentDeleted', value: '' }
  const value = text(agent.status)
  const keys: Record<string, string> = { running: 'running', stopping: 'stopping', compacting: 'compacting', idle: 'subagentIdle', queued: 'subagentQueued', waiting: 'subagentQueued', failed: 'subagentFailed', error: 'subagentFailed', needs_attention: 'subagentNeedsAttention' }
  return { key: keys[value], value }
}

export function sentMessageDetails(entry: Entry) {
  const args = object(entry.tool?.invocation.call.arguments)
  const binding = object(entry.tool?.invocation.binding)
  const data = object(toolResultData(entry))
  const result = entry.tool?.result
  const messageIDs = Array.isArray(data.message_ids) ? data.message_ids.filter((id): id is string => typeof id === 'string') : []
  let status = ''
  if (result) {
    if (data.sent === true) status = 'toolMessageSent'
    else if (data.sent === false) status = 'toolMessageFailed'
    else if (data.sent === null || result.outcome === 'unknown') status = 'toolMessageUnknown'
    else if (data.status === 'queued' || data.status === 'pending') status = 'toolMessageQueued'
    else if (data.status === 'accepted' || data.accepted === true) status = 'toolMessageAccepted'
    else if (result.outcome === 'not_executed') status = 'toolMessageNotSent'
    else if (result.is_error || result.outcome === 'failed') status = 'toolMessageFailed'
    else status = 'toolMessageUnknown'
  }
  return {
    target: binding,
    text: text(args.text),
    title: text(args.title),
    format: text(args.format),
    replyTo: text(args.reply_to),
    attachments: Array.isArray(args.attachments) ? args.attachments.filter((item): item is string => typeof item === 'string') : [],
    // Keep native ordered parts intact: joining them would lose image/file
    // positions or introduce spaces that were never part of the sent message.
    parts: Array.isArray(args.parts) ? args.parts.map(object) : [],
    template: object(args.markdown_template),
    silent: args.disable_notification === true,
    asDocument: args.as_document === true,
    status,
    messageIDs,
    fallback: toolResultFallbackText(entry),
    error: toolResultError(entry),
  }
}

export function toolPresentation(entry: Entry) {
  const name = entry.tool?.invocation.call.name ?? ''
  const args = object(entry.tool?.invocation.call.arguments)
  const labels: Record<string, string> = { exec_command: 'runCommand', read: 'readFile', read_file: 'readFile', write_file: 'writeFile', apply_patch: 'editFile', edit_file: 'editFile', web_search: 'webSearch', send_message: 'sendMessage', spawn_agent: 'spawnSubagent', agent_control: 'controlSubagent' }
  const command = String(args.command || args.cmd || '')
  return { name, label: labels[name], command, target: command || String(args.path || args.query || args.url || '') }
}
