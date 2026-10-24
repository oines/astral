/* global module, process, __dirname, require */
// eslint-disable-next-line @typescript-eslint/no-require-imports -- electron-builder loads this configuration as CommonJS.
const path = require('node:path')

const team = process.env.APPLE_TEAM_ID
const identity = process.env.MAC_SIGNING_IDENTITY
if (team !== '6N6W6Z6739' || !identity?.startsWith('Developer ID Application: ') || !identity.endsWith(` (${team})`)) {
  throw new Error('Signed Mac packaging requires the personal team 6N6W6Z6739 and its full Developer ID Application identity.')
}
if (!process.env.CSC_KEYCHAIN || !path.isAbsolute(process.env.CSC_KEYCHAIN)) {
  throw new Error('Signed Mac packaging requires an absolute CSC_KEYCHAIN path.')
}

const signing = () => import('../../scripts/mac-signing.mjs')

module.exports = {
  extends: './electron-builder.yml',
  forceCodeSigning: true,
  mac: {
    // v26 rejects a certificate-type prefix in this qualifier; our environment
    // and the verification script still require the full Developer ID identity.
    identity: identity.slice('Developer ID Application: '.length),
    type: 'distribution',
    hardenedRuntime: true,
    strictVerify: true,
    preAutoEntitlements: false,
    entitlements: path.join(__dirname, 'resources/entitlements.mac.plist'),
    entitlementsInherit: path.join(__dirname, 'resources/entitlements.mac.plist'),
    // These two programs must already be signed before checksums are generated.
    // Re-signing them here would invalidate the sealed checksum manifest.
    signIgnore: ['/Contents/Resources/bin/astral-darwin-(amd64|arm64)$'],
    // The release workflow submits and verifies notarization explicitly. The
    // built-in integration can otherwise silently skip absent credentials.
    notarize: false,
    timestamp: 'http://timestamp.apple.com/ts01',
  },
  async afterPack(context) {
    const { verifyBinaries } = await signing()
    await verifyBinaries(path.join(context.appOutDir, `${context.packager.appInfo.productFilename}.app/Contents/Resources/bin`))
  },
  async afterSign(context) {
    const { verifyApp } = await signing()
    await verifyApp(path.join(context.appOutDir, `${context.packager.appInfo.productFilename}.app`))
  },
}
