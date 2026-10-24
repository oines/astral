import { mkdir, mkdtemp, rename, rm, writeFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { dirname, isAbsolute, join } from 'node:path'

// Linux desktop login is managed per user through the XDG autostart directory.
// AppImages must use APPIMAGE's persistent path, not their temporary mount path.
export async function setLinuxLoginStart(enabled: boolean, executable: string, configDirectory = process.env.XDG_CONFIG_HOME && isAbsolute(process.env.XDG_CONFIG_HOME) ? process.env.XDG_CONFIG_HOME : join(homedir(), '.config')) {
  if (!isAbsolute(executable) || /[\r\n\0=]/.test(executable)) throw new Error('Invalid application path for login startup.')
  const target = join(configDirectory, 'autostart', 'com.oines.astral.desktop')
  if (!enabled) {
    await rm(target, { force: true })
    return
  }
  // Desktop Entry Exec has its own quoting and field-code rules; it is not a shell.
  const command = executable.replace(/%/g, '%%').replace(/[\\"`$]/g, '\\$&').replace(/\\/g, '\\\\')
  const content = `[Desktop Entry]\nType=Application\nVersion=1.0\nName=Astral\nExec="${command}"\nTerminal=false\nX-GNOME-Autostart-enabled=true\n`
  await mkdir(dirname(target), { recursive: true, mode: 0o700 })
  const temporary = await mkdtemp(join(dirname(target), '.astral-'))
  try {
    const entry = join(temporary, 'entry.desktop')
    await writeFile(entry, content, { mode: 0o600, flag: 'wx' })
    await rename(entry, target)
  } finally {
    await rm(temporary, { recursive: true, force: true })
  }
}
