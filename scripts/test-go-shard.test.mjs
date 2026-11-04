import assert from 'node:assert/strict'
import test from 'node:test'
import { runShard, shardTests, testNames, testPattern } from './test-go-shard.mjs'

test('native discovery includes tests, examples and fuzz seeds, with duplicate package names deduplicated', () => {
  assert.deepEqual(testNames('TestOne\nFuzzInput\nExample\nExampleClient_read\nTestOne\nok pkg 0.2s\n? pkg [no test files]\n'), ['Example', 'ExampleClient_read', 'FuzzInput', 'TestOne'])
})

test('every test name belongs to exactly one deterministic shard on every platform', () => {
  const names = Array.from({ length: 2000 }, (_, index) => `TestRealBehavior${index}`)
  for (const count of [1, 2, 4, 8]) {
    const shards = Array.from({ length: count }, (_, index) => shardTests(names, index, count))
    assert.deepEqual(shards.flat().sort(), [...names].sort())
    for (const [index, selected] of shards.entries()) {
      assert.deepEqual(shardTests([...names].reverse(), index, count), selected)
      const matches = new RegExp(testPattern(selected))
      assert.deepEqual(names.filter(name => matches.test(name)).sort(), selected)
      assert.equal(matches.test('TestRealBehaviorExtra'), false)
    }
  }
  assert.throws(() => shardTests(names, 4, 4), /Invalid/)
})

test('discovery or execution failures cannot turn a shard green', () => {
  assert.throws(() => runShard(0, 1, { execute: () => ({ status: 1, stderr: 'compile failed' }) }), /discovery failed/)
  assert.throws(() => runShard(0, 1, { execute: () => ({ status: 0, stdout: 'ok package' }) }), /no tests/)
  const calls = []
  assert.throws(() => runShard(0, 1, {
    execute: (_program, args) => {
      calls.push(args)
      return calls.length === 1 ? { status: 0, stdout: 'TestOne\nExample\nFuzzInput\n' } : { status: 1 }
    }, log: () => {},
  }), /shard 1\/1 failed/)
  assert.ok(calls.every(args => args.includes('-race')))
  assert.deepEqual(calls[1].slice(-3), ['-run', '^(Example|FuzzInput|TestOne)$', './...'])
})
