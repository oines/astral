import type { Computer } from './types'
import { operatingSystem } from './api'

export type ComputerFact = { key: string; value?: string; copy?: boolean }
type Translate = (key: string) => string

export function computerKind(computer: Computer) {
  return computer.kind === 'container' ? 'container' : computer.local || computer.kind === 'local' ? 'local' : 'remote'
}
export function computerSystem(computer: Computer) {
  const arch = ({ arm64: 'ARM64', aarch64: 'ARM64', amd64: 'x86_64', x86_64: 'x86_64' })[computer.arch || ''] || computer.arch
  return [computer.os ? operatingSystem(computer.os) : '', arch].filter(Boolean).join(' · ')
}
export function computerSummary(computer: Computer, t: Translate) {
  return [t(`computerInfo.kind.${computerKind(computer)}`), computerSystem(computer), computer.container?.name !== computer.name ? computer.container?.name : ''].filter(Boolean).join(' · ')
}
export function computerEnvironment(computer: Computer): ComputerFact[] {
  return [
    { key: 'computerInfo.hostname', value: computer.hostname },
    { key: 'operatingSystem', value: computerSystem(computer) },
    { key: 'shell', value: computer.shell },
  ]
}
export function containerIdentity(computer: Computer): ComputerFact[] {
  if (computer.kind !== 'container') return []
  return [
    { key: 'computerInfo.containerName', value: computer.container?.name, copy: true },
    { key: 'computerInfo.host', value: computer.container?.host, copy: true },
    { key: 'computerInfo.image', value: computer.container?.image, copy: true },
  ]
}
export function computerStorage(computer: Computer): ComputerFact[] {
  const rows: ComputerFact[] = [
    { key: 'computerInfo.dataDirectory', value: computer.root, copy: true },
    { key: 'computerInfo.userDirectory', value: computer.home, copy: true },
  ]
  if (computer.kind === 'container') rows.push(
    { key: 'computerInfo.volume', value: computer.container?.volume, copy: true },
    { key: 'computerInfo.mountPath', value: computer.container?.mount_path, copy: true },
  )
  return rows
}
