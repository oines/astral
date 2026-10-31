import assert from 'node:assert/strict'
import ts from 'typescript'

const productionFeed = 'https://github.com/oines/astral/releases/latest/download/'

/** Patch only the copied fixture's compiled update source, preserving all other bytes. */
export function patchCompiledUpdateFeed(source, fixtureFeed) {
  assert.equal(typeof source, 'string', 'Fixture must have a compiled Desktop source')
  assert.equal(typeof fixtureFeed, 'string', 'Fixture update feed must be a canonical loopback HTTP root URL')
  const feed = new URL(fixtureFeed)
  assert.ok(feed.protocol === 'http:' && ['127.0.0.1', '[::1]'].includes(feed.hostname) && Number(feed.port) > 0 &&
    !feed.username && !feed.password && feed.pathname === '/' && !feed.search && !feed.hash &&
    fixtureFeed === `http://${feed.host}/`,
  'Fixture update feed must be a canonical loopback HTTP root URL with an explicit port')

  // Parse the already compiled JavaScript so comments, templates and embedded
  // configuration strings cannot impersonate the real top-level declaration.
  const compiled = ts.createSourceFile('index.js', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.JS)
  assert.equal(compiled.parseDiagnostics.length, 0, 'Fixture compiled Desktop source must parse as JavaScript')
  const declarations = compiled.statements.filter(ts.isVariableStatement).flatMap(statement =>
    [...statement.declarationList.declarations].filter(declaration => ts.isIdentifier(declaration.name) && declaration.name.text === 'updateFeed'))
  assert.equal(declarations.length, 1, 'Fixture must replace exactly one top-level compiled updateFeed declaration')
  const declaration = declarations[0]
  assert.ok(declaration.name.getText(compiled) === 'updateFeed' && /^(?:const|var|let)\b/.test(declaration.parent.getText(compiled)),
    'Fixture updateFeed must use a plain const, var or let declaration')
  const literal = declaration.initializer
  assert.ok(literal && ts.isStringLiteral(literal) &&
    [`'${productionFeed}'`, `"${productionFeed}"`].includes(literal.getText(compiled)),
  'Fixture updateFeed must contain the exact official feed literal')

  return source.slice(0, literal.getStart(compiled)) + JSON.stringify(fixtureFeed) + source.slice(literal.end)
}
