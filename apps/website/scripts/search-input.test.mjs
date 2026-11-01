import assert from 'node:assert/strict';
import test from 'node:test';
import { prepareSearchHTML } from './build-search-index.mjs';

test('Chinese index input follows ICU boundaries without changing non-indexed markup', () => {
  const html = '<!doctype html><html lang="zh-CN"><head><title>从源码构建</title></head><body><nav>源码导航</nav><main data-pagefind-body><h1 data-pagefind-meta="title">从源码构建</h1><p>使用 pnpm 构建，https://github.com/oines/astral</p><code>GOWORK=off 源码</code><span data-pagefind-ignore>保持原文</span><script>const text = "源码";</script></main></body></html>';
  const input = prepareSearchHTML(html);
  const expected = [...new Intl.Segmenter('zh-CN', { granularity: 'word' }).segment('从源码构建')].map((item) => item.segment).join('\u200B');
  assert.ok(input.includes(`>${expected}</h1>`));
  assert.ok(input.includes('data-astral-search-title="从源码构建"'));
  assert.ok(input.includes('data-pagefind-meta="title[data-astral-search-title]"'));
  assert.ok(input.includes('<title>从源码构建</title>'));
  assert.ok(input.includes('<nav>源码导航</nav>'));
  assert.ok(input.includes('https://github.com/oines/astral'));
  assert.ok(input.includes('<code>GOWORK=off 源码</code>'));
  assert.ok(input.includes('<span data-pagefind-ignore="">保持原文</span>'));
  assert.ok(input.includes('const text = "源码";'));
  assert.ok(!html.includes('\u200B'));
});

test('English and Chinese pages without indexed regions remain byte-identical', () => {
  const english = '<!doctype html><html lang="en"><main data-pagefind-body>Build Astral with pnpm. 中文代码示例</main></html>';
  const unindexed = '<!doctype html><html lang="zh-CN"><body>源码</body></html>';
  assert.equal(prepareSearchHTML(english), english);
  assert.equal(prepareSearchHTML(unindexed), unindexed);
});
