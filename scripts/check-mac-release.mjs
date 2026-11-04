// Disposable native CI only: prepare the signed old update fixture while the
// final App is notarized. The updater starts only after notarization succeeds.
import assert from 'node:assert/strict'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { runMacReleaseAcceptance } from './mac-release-acceptance.mjs'

if (process.env.CI !== 'true' || process.env.GITHUB_ACTIONS !== 'true' || !process.env.RUNNER_TEMP) throw new Error('Mac release acceptance requires a disposable GitHub Actions user; never run on a developer account.')
assert.equal(process.platform, 'darwin', 'Mac release acceptance requires a native Mac runner.')
const [app, architecture, ...extra] = process.argv.slice(2)
assert.ok(app && ['arm64', 'x64'].includes(architecture) && extra.length === 0, 'Usage: check-mac-release.mjs APP_PATH ARCH')
assert.equal(process.arch, architecture, 'Mac release acceptance requires its native architecture.')
const repository = fileURLToPath(new URL('../', import.meta.url))
const controller = new globalThis.AbortController()
const terminate = () => controller.abort(new Error('Mac release acceptance was terminated.'))
process.once('SIGTERM', terminate)
process.once('SIGINT', terminate)
try {
  await runMacReleaseAcceptance({
    notarize: { program: process.execPath, args: [path.join(repository, 'scripts/mac-notarize.mjs'), 'app', path.resolve(app), architecture] },
    update: { program: process.execPath, args: [path.join(repository, 'scripts/check-desktop-update.mjs'), '--await-notarization'] },
    cwd: repository, signal: controller.signal,
  })
} finally {
  process.off('SIGTERM', terminate)
  process.off('SIGINT', terminate)
}
