import { readFile } from 'node:fs/promises'
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { copiedNotices } from './build-notices.mjs'

test('distribution copied notices reproduce the complete Unicode CLDR license', async () => {
  const root = fileURLToPath(new URL('../', import.meta.url))
  const license = await readFile(path.join(root, 'internal/timezone/system_windows_LICENSE.txt'), 'utf8')
  const sections = await copiedNotices(root)
  const cldr = sections.find(section => section.startsWith('Unicode CLDR Windows timezone mapping\n'))
  assert.ok(cldr, 'copied timezone data must have a distribution notice section')
  assert.ok(cldr.includes(license), 'the whole checked-in copyright and permission notice must be reproduced')
  assert.match(cldr, /SPDX-License-Identifier: Unicode-3\.0/)
  assert.match(cldr, /c4bb2c199c5385088f52011c4d4c813f2c36db1e\/common\/supplemental\/windowsZones\.xml/)
  assert.ok(sections.some(section => section.startsWith('Copied frontend brand assets\n')), 'existing copied asset notices must remain included')
})
