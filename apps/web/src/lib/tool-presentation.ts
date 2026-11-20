import type { Content, Entry, ProcessStatus } from './types'
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

const fileToolNames = new Set(['read', 'read_file', 'write', 'write_file', 'edit', 'edit_file', 'apply_patch'])
const directFilePreviewNames = new Set(['read', 'read_file', 'write', 'write_file'])
const semanticDetailNames = new Set(['agent_control', 'send_message', 'spawn_agent', 'web_search'])

function isAbsolutePath(path: string) {
  return path.startsWith('/') || path.startsWith('\\\\') || /^[a-zA-Z]:[\\/]/.test(path)
}

function joinPath(cwd: string, rel: string) {
  if (!cwd) return rel
  const sep = cwd.includes('\\') && !cwd.includes('/') ? '\\' : '/'
  const base = cwd.replace(/[\\/]+$/, '')
  const tail = rel.replace(/^\.[\\/]/, '')
  return tail ? `${base}${sep}${tail}` : base
}

export function fileToolTarget(entry: Entry) {
  const name = entry.tool?.invocation.call.name ?? ''
  if (!fileToolNames.has(name)) return undefined
  const args = object(entry.tool?.invocation.call.arguments)
  const rawPath = text(args.path) || text(args.file_path)
  if (!rawPath) return undefined
  const binding = object(entry.tool?.invocation.binding)
  const computerID = text(binding.computer_id)
  if (!computerID) return undefined
  const cwd = text(binding.cwd)
  const resolved = isAbsolutePath(rawPath) ? rawPath : joinPath(cwd, rawPath)
  return { computerID, path: resolved, displayPath: rawPath }
}

export function sourceHost(url: string) {
  try {
    return new URL(url).hostname.replace(/^www\./i, '')
  } catch {
    return ''
  }
}

export function spawnAgentDetails(entry: Entry) {
  const args = object(entry.tool?.invocation.call.arguments)
  const data = object(toolResultData(entry))
  const selection = object(data.selection)
  const model = text(selection.id) || text(args.model)
  const reasoningSupplied = Object.hasOwn(selection, 'reasoning') || Object.hasOwn(args, 'reasoning')
  const reasoningValue = Object.hasOwn(selection, 'reasoning') ? selection.reasoning : args.reasoning
  return {
    sessionID: text(data.session_id),
    name: text(data.name) || text(args.name),
    task: text(args.task),
    inheritContext: args.inherit_context === true,
    model,
    reasoning: reasoningDetails(reasoningValue, reasoningSupplied),
    fallback: toolResultFallbackText(entry),
    error: toolResultError(entry),
  }
}

export function toolHasOutputOrPatch(entry: Entry, liveText = '', process?: ProcessStatus) {
  const details = object(entry.tool?.result?.details)
  const output = typeof details.output === 'string' ? details.output : liveText
  const patch = typeof (details.patch || details.diff) === 'string' ? String(details.patch || details.diff) : ''
  return {
    output,
    patch,
    hasOutput: Boolean(output.trim()) || Boolean(process?.has_output),
    hasPatch: Boolean(patch.trim()),
  }
}

export type ToolInteractionMode = 'static' | 'file' | 'expand'

export function toolInteractionMode(
  entry: Entry,
  options: { active?: boolean; process?: ProcessStatus; liveText?: string; targetTruncated?: boolean } = {},
): ToolInteractionMode {
  const name = entry.tool?.invocation.call.name ?? ''
  const result = entry.tool?.result
  const details = object(result?.details)
  const { hasOutput, hasPatch } = toolHasOutputOrPatch(entry, options.liveText, options.process)
  const hasExitError = (details.exit_code !== undefined && details.exit_code !== 0)
    || (options.process?.exit_code !== undefined && options.process.exit_code !== 0)
    || Boolean(options.process?.error)

  if (options.active || options.process?.running) return 'expand'
  if (toolResultError(entry) || hasExitError) return 'expand'
  if ((result?.content || []).some(part => part.type === 'image')) return 'expand'
  if (semanticDetailNames.has(name)) return 'expand'
  if (hasPatch) return 'expand'

  if (directFilePreviewNames.has(name)) {
    if (fileToolTarget(entry)) return 'file'
    return hasOutput || Boolean(toolResultText(entry).trim()) ? 'expand' : 'static'
  }
  if (name === 'switch_computer') return 'static'
  if (name === 'exec_command' || name === 'write_stdin') {
    const args = object(entry.tool?.invocation.call.arguments)
    const command = text(args.command) || text(args.cmd)
    if (hasOutput || options.process || command.includes('\n') || options.targetTruncated) return 'expand'
    return 'static'
  }
  if (hasOutput || Boolean(toolResultText(entry).trim())) return 'expand'
  return 'static'
}

export function toolPresentation(entry: Entry, computers: Array<{ id: string; name: string }> = []) {
  const name = entry.tool?.invocation.call.name ?? ''
  const args = object(entry.tool?.invocation.call.arguments)
  const data = object(toolResultData(entry))
  const labels: Record<string, string> = {
    exec_command: 'runCommand',
    write_stdin: text(args.chars) ? 'writeStdin' : 'readStdin',
    read: 'readFile',
    read_file: 'readFile',
    write: 'writeFile',
    write_file: 'writeFile',
    apply_patch: 'editFile',
    edit: 'editFile',
    edit_file: 'editFile',
    web_search: 'webSearch',
    send_message: 'sendMessage',
    spawn_agent: 'spawnSubagent',
    agent_control: 'controlSubagent',
    switch_computer: 'switchComputer',
  }
  const command = String(args.command || args.cmd || '')
  let target = command || String(args.path || args.file_path || args.query || args.url || '')
  if (name === 'switch_computer') {
    const computerID = text(args.computer_id) || text(data.computer_id)
    const computerName = computers.find(item => item.id === computerID)?.name || text(data.computer_name) || computerID
    const cwd = text(args.cwd) || text(data.cwd)
    target = [computerName, cwd].filter(Boolean).join(' · ')
  } else if (name === 'spawn_agent') {
    const firstLine = text(args.task).trim().split('\n').find(line => line.trim()) || ''
    target = text(data.name) || text(args.name) || firstLine
  } else if (name === 'write_stdin') {
    target = text(args.chars).replace(/\s+/g, ' ').trim()
  }
  return { name, label: labels[name], command, target }
}
