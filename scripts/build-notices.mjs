import { execFileSync } from 'node:child_process'
import { readFile, readdir, writeFile } from 'node:fs/promises'
import path from 'node:path'

const licenseFile = /^(licen[sc]e|copying|notice|ofl)([._-]|$)/i

async function notices(directory) {
  if (!directory) return ''
  const entries = await readdir(directory, { withFileTypes: true })
  const selected = entries.filter(entry => entry.isFile() && licenseFile.test(entry.name)).sort((a, b) => a.name.localeCompare(b.name))
  const text = []
  for (const entry of selected) text.push(`${entry.name}\n\n${await readFile(path.join(directory, entry.name), 'utf8')}`)
  return text.join('\n\n')
}

export async function buildNotices(root, target) {
  const commandOptions = { cwd: root, encoding: 'utf8', maxBuffer: 20 << 20 }
  const packageManager = process.platform === 'win32' ? process.env.npm_execpath : undefined
  if (process.platform === 'win32' && !packageManager) throw new Error('Run this build through pnpm (for example, pnpm build:distribution).')
  const nodePackages = JSON.parse(execFileSync(packageManager ? process.execPath : 'pnpm', [...(packageManager ? [packageManager] : []), 'licenses', 'list', '--prod', '--json'], commandOptions))
  const goPackages = execFileSync('go', ['list', '-m', '-f', '{{if not (eq .Path "github.com/oines/astral")}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}', 'all'], { ...commandOptions, env: { ...process.env, GOWORK: process.env.GOWORK || 'auto' } })
    .split('\n').filter(Boolean).map(line => {
      const [Path, Version, Dir] = line.split('\t')
      return { Path, Version, Dir }
    })
  const sections = ['Astral third-party notices\n\nPackage source licenses are reproduced below. Some build-time dependencies may also appear in this inventory.\n\n' + await readFile(path.join(root, 'THIRD_PARTY.md'), 'utf8')]
  sections.push(`Copied frontend brand assets\n\n${await notices(path.join(root, 'packages/icons/src/assets'))}`)
  for (const entry of Object.values(nodePackages).flat().sort((a, b) => a.name.localeCompare(b.name))) {
    const texts = []
    for (const directory of entry.paths) {
      const text = await notices(directory)
      if (text && !texts.includes(text)) texts.push(text)
    }
    sections.push(`${entry.name} (${entry.versions.join(', ')})\nLicense: ${entry.license}\n${entry.homepage || ''}\n\n${texts.join('\n\n')}`)
  }
  for (const entry of goPackages.sort((a, b) => a.Path.localeCompare(b.Path))) {
    sections.push(`${entry.Path} ${entry.Version}\nhttps://${entry.Path}\n\n${await notices(entry.Dir)}`)
  }
  const goRoot = execFileSync('go', ['env', 'GOROOT'], commandOptions).trim()
  const goLicense = await readFile(path.join(goRoot, 'LICENSE'), 'utf8').catch(error => {
    if (error.code !== 'ENOENT') throw error
    // Homebrew keeps the distribution license beside libexec/GOROOT.
    return readFile(path.join(goRoot, '../LICENSE'), 'utf8')
  })
  sections.push(`Go standard library\n\n${goLicense}`)
  await writeFile(target, sections.join('\n\n' + '='.repeat(80) + '\n\n') + '\n')
}
