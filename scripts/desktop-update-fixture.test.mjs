import assert from 'node:assert/strict'
import test from 'node:test'
import { patchCompiledUpdateFeed } from './desktop-update-fixture.mjs'

const official = 'https://github.com/oines/astral/releases/latest/download/'
const fixture = 'http://127.0.0.1:24567/'
const ownership = `if (JSON.stringify(values("url")) !== ${JSON.stringify(JSON.stringify([official]))}) return;`

test('only the unique compiled feed literal changes; cache ownership and all surrounding bytes survive', () => {
  const before = `// 中文 fixture\r\nvar updateFeed = "${official}";\r\nfunction cacheRoot() { ${ownership} }\r\nconst anotherURL = "${official}";\r\n`
  const expected = `// 中文 fixture\r\nvar updateFeed = "${fixture}";\r\nfunction cacheRoot() { ${ownership} }\r\nconst anotherURL = "${official}";\r\n`
  const patched = patchCompiledUpdateFeed(before, fixture)
  assert.equal(patched, expected)
  assert.ok(patched.includes(ownership), 'The cache ownership check must retain the official URL byte for byte')
})

test('compiled const, var and let declarations accept either exact quote style', async t => {
  for (const keyword of ['const', 'var', 'let']) for (const quote of ['"', "'"]) await t.test(`${keyword} ${quote}`, () => {
    const before = `${keyword} updateFeed = ${quote}${official}${quote};`
    assert.equal(patchCompiledUpdateFeed(before, fixture), `${keyword} updateFeed = "${fixture}";`)
  })
  const ipv6 = 'http://[::1]:24567/'
  assert.equal(patchCompiledUpdateFeed(`var updateFeed = "${official}";`, ipv6), `var updateFeed = "${ipv6}";`)
})

test('missing, duplicate, changed and ambiguous compiled feed declarations fail closed', async t => {
  for (const [name, source] of [
    ['missing', `const ownership = "${official}";`],
    ['similar identifier', `var updateFeedBackup = "${official}";`],
    ['local declaration', `function unrelated() { var updateFeed = "${official}"; }`],
    ['duplicate', `var updateFeed = "${official}"; let updateFeed = "${official}";`],
    ['one official and one changed', `var updateFeed = "${official}"; var updateFeed = "https://example.test/";`],
    ['changed', 'var updateFeed = "https://example.test/";'],
    ['official URL suffix', `var updateFeed = "${official}other/";`],
    ['concatenation', `var updateFeed = "${official}" + "other/";`],
    ['template', `var updateFeed = \`${official}\`;`],
    ['escaped literal', `var updateFeed = "https:\\u002f\\u002fgithub.com/oines/astral/releases/latest/download/";`],
    ['nonliteral', 'var updateFeed = officialFeed;'],
    ['malformed source', `var updateFeed = "${official};`],
    ['block comment', `/*\nvar updateFeed = "${official}";\n*/`],
    ['template fake declaration', `const text = \`\nvar updateFeed = "${official}";\n\`;`],
    ['changed declaration and official comment', `var updateFeed = "https://example.test/";\n/*\nvar updateFeed = "${official}";\n*/`],
  ]) await t.test(name, () => {
    assert.throws(() => patchCompiledUpdateFeed(source, fixture), /Fixture/)
  })
})

test('comments and templates do not create extra compiled declarations or get patched', () => {
  const ignored = `/*\nvar updateFeed = "${official}";\n*/\nconst text = \`\nvar updateFeed = "${official}";\n\`;\n`
  assert.equal(patchCompiledUpdateFeed(`${ignored}var updateFeed = "${official}";`, fixture), `${ignored}var updateFeed = "${fixture}";`)
})

test('only canonical numeric loopback HTTP roots with an explicit port are accepted', async t => {
  for (const feed of [
    'https://example.test/', 'http://example.test:24567/', 'http://localhost:24567/',
    'http://127.0.0.1.example.test:24567/', 'http://127.0.0.2:24567/', 'https://127.0.0.1:24567/',
    'http://127.0.0.1/', 'http://127.0.0.1:80/', 'http://127.0.0.1:0/', 'http://127.0.0.1:65536/',
    'http://127.0.0.1:24567/path/', 'http://127.0.0.1:24567/?query=1', 'http://127.0.0.1:24567/#hash',
    'http://127.0.0.1:24567/?', 'http://127.0.0.1:24567/#',
    'http://user:password@127.0.0.1:24567/', 'http://127.1:24567/', 'http://2130706433:24567/',
    'http://0177.0.0.1:24567/', 'http://127.0.0.1:024567/', 'http://127.0.0.1:24567',
    'http:\\127.0.0.1:24567\\', ' http://127.0.0.1:24567/', 'http://127.0.0.1:24567/\n',
    '', undefined,
  ]) await t.test(String(feed), () => {
    assert.throws(() => patchCompiledUpdateFeed(`var updateFeed = "${official}";`, feed))
  })
})
