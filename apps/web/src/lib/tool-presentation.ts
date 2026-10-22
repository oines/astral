import type { Entry } from './types'
import { object } from './api'

export function toolPresentation(entry: Entry) {
  const name = entry.tool?.invocation.call.name ?? ''
  const args = object(entry.tool?.invocation.call.arguments)
  const labels: Record<string, string> = { exec_command: 'runCommand', read_file: 'readFile', write_file: 'writeFile', apply_patch: 'editFile', edit_file: 'editFile', web_search: 'webSearch', send_message: 'sendMessage' }
  const command = String(args.command || args.cmd || '')
  return { name, label: labels[name], command, target: command || String(args.path || args.query || args.url || '') }
}
