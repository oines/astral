import { execFileSync } from 'node:child_process'
import { readFile, writeFile } from 'node:fs/promises'
import { imageRepository, recipeHash, recipePath, validateImageConfig } from './computer-image.mjs'
import { isDirectInvocation } from './release-version.mjs'

export const environmentSmoke = `set -euo pipefail
test ! -e /usr/local/bin/astral
! command -v astral
test ! -e /opt/astral
for tool in bash curl wget git ssh rsync rg fd jq tmux gcc clang cmake ninja gdb sqlite3 psql pandoc pdftotext ffmpeg convert gs tesseract java mvn libreoffice uv uvx node npm pnpm go rustc cargo; do command -v "$tool" >/dev/null; done
node --version
pnpm --version
go version
rustc --version
java -version
git lfs version
libreoffice --headless --version
test "$(sqlite3 :memory: 'select 40+2;')" = 42
python - <<'PY'
import numpy, pandas, scipy, matplotlib, PIL, docx, pptx, openpyxl, pypdf, pdfplumber, reportlab, requests, bs4, yaml
from playwright.sync_api import sync_playwright
matplotlib.use('Agg')
assert int(numpy.array([20,22]).sum()) == 42
with sync_playwright() as p:
    browser = p.chromium.launch(headless=True, args=['--no-sandbox'])
    page = browser.new_page()
    page.set_content('<title>Astral environment smoke</title><p>42</p>')
    assert page.title() == 'Astral environment smoke'
    assert page.locator('p').inner_text() == '42'
    browser.close()
print('Python document/data imports and real Chromium passed; no Astral executable.')
PY
`

export async function buildEnvironment({ architecture, publish = false, output }) {
  const native = process.arch === 'x64' ? 'amd64' : process.arch
  if (process.platform !== 'linux' || native !== architecture || process.env.CI !== 'true') throw new Error('Environment images must be built and smoked on a matching native Linux CI runner, never a local machine or QEMU.')
  const recipe = await readFile(recipePath)
  const recipeSHA = recipeHash(recipe)
  const sourceSHA = process.env.GITHUB_SHA
  if (!/^[a-f0-9]{40}$/.test(sourceSHA || '')) throw new Error('GITHUB_SHA must identify the environment build source.')
  const attempt = Number(process.env.GITHUB_RUN_ATTEMPT)
  if (publish && (!/^[1-9][0-9]*$/.test(process.env.GITHUB_RUN_ID || '') || !Number.isSafeInteger(attempt) || attempt < 1 || !output)) throw new Error('Publishing requires a GitHub run ID, attempt and platform record path.')
  const tag = publish ? `${imageRepository}:env-${recipeSHA.slice(0, 12)}-${process.env.GITHUB_RUN_ID}-${attempt}-${architecture}` : `astral-environment-${architecture}:candidate`
  const command = (args, options = {}) => execFileSync('docker', args, { encoding: 'utf8', timeout: 120_000, ...options })
  command(['build', '--pull', '--platform', `linux/${architecture}`, '--label', `io.astral.environment.recipe-sha256=${recipeSHA}`, '--label', 'io.astral.environment.kind=agent-tools', '--label', `org.opencontainers.image.revision=${sourceSHA}`, '--label', 'org.opencontainers.image.source=https://github.com/oines/astral', '--tag', tag, '-'], { input: recipe, stdio: ['pipe', 'inherit', 'inherit'], timeout: 90 * 60_000 })
  const inspected = JSON.parse(command(['image', 'inspect', tag]))[0]
  validateImageConfig({ os: inspected.Os, architecture: inspected.Architecture, config: inspected.Config }, architecture, recipeSHA, sourceSHA)
  command(['run', '--rm', '--network', 'none', '--platform', `linux/${architecture}`, tag, '/bin/bash', '-lc', environmentSmoke], { stdio: 'inherit', timeout: 180_000 })
  if (!publish) return
  command(['push', tag], { stdio: 'inherit', timeout: 20 * 60_000 })
  const digests = JSON.parse(command(['image', 'inspect', tag]))[0].RepoDigests.filter(value => value.startsWith(`${imageRepository}@sha256:`))
  if (digests.length !== 1) throw new Error('The pushed environment did not produce exactly one immutable registry digest.')
  // Smoke the remote digest, not just an unpushed build tag. Docker can reuse
  // identical local layers while still verifying the registry manifest.
  command(['pull', digests[0]], { stdio: 'inherit', timeout: 10 * 60_000 })
  command(['run', '--rm', '--network', 'none', '--platform', `linux/${architecture}`, digests[0], '/bin/bash', '-lc', environmentSmoke], { stdio: 'inherit', timeout: 180_000 })
  await writeFile(output, `${JSON.stringify({ image: imageRepository, architecture, digest: digests[0].split('@')[1], recipe_sha256: recipeSHA, source_sha: sourceSHA, run_id: process.env.GITHUB_RUN_ID, attempt, native_smoke: true, published_smoke: true }, null, 2)}\n`, { flag: 'wx' })
}

if (await isDirectInvocation(import.meta.url)) {
  try {
    const args = process.argv.slice(2)
    const option = name => { const index = args.indexOf(name); return index < 0 ? undefined : args[index + 1] }
    await buildEnvironment({ architecture: option('--arch'), publish: args.includes('--publish'), output: option('--output') })
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
