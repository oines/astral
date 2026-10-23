import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { afterEach, expect, it } from 'vitest'
import { setLinuxLoginStart } from './login'

const directories: string[] = []
afterEach(async () => { await Promise.all(directories.splice(0).map(path => rm(path, { recursive: true, force: true }))) })

it('registers a per-user desktop entry and removes only Astral when disabled', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'astral-autostart-'))
  directories.push(directory)
  const executable = resolve(directory, 'Astral 中文 $tools 100%.AppImage')
  await setLinuxLoginStart(true, executable, directory)
  const target = join(directory, 'autostart', 'com.oines.astral.desktop')
  const entry = await readFile(target, 'utf8')
  expect(entry).toContain('Type=Application\n')
  expect(entry).toContain('Terminal=false\n')
  expect(entry).toContain('100%%.AppImage')
  expect(entry).toContain('\\\\$tools')
  expect(entry).not.toContain('/tmp/.mount_')
  const other = join(directory, 'autostart', 'other.desktop')
  await writeFile(other, 'other application')
  await setLinuxLoginStart(false, executable, directory)
  await expect(readFile(target)).rejects.toMatchObject({ code: 'ENOENT' })
  expect(await readFile(other, 'utf8')).toBe('other application')
})

it('rejects control characters rather than injecting a desktop entry', async () => {
  await expect(setLinuxLoginStart(true, '/opt/Astral\nHidden=true')).rejects.toThrow('Invalid application path')
})
