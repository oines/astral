import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { readdir, readFile } from 'node:fs/promises';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';
import { gunzipSync } from 'node:zlib';
import test from 'node:test';

const execute = promisify(execFile);
const probe = fileURLToPath(new URL('./search-index-probe.mjs', import.meta.url));
const noMatch = 'zzastralnomatchzz0123456789';
const fragments = new URL('../dist/pagefind/fragment/', import.meta.url);

for (const [language, cases] of [
  ['zh-cn', [
    ['/zh-cn/docs/hosting/build-from-source/', ['源码', '构建', '从源码构建', '源', '码', '构', '建', 'pnpm', 'GOWORK']],
    ['/zh-cn/docs/computers/connect/', ['电脑']],
    ['/zh-cn/docs/chat/overview/', ['聊天']],
    ['/zh-cn/docs/usage/files/', ['文件']],
  ]],
  ['en', [
    ['/docs/hosting/build-from-source/', ['Build from source', 'pnpm', 'GOWORK']],
    ['/docs/chat/telegram/', ['Telegram']],
  ]],
]) {
  test(`${language} generated index finds normal words, titles, and code terms`, async () => {
    const titleCases = [];
    for (const file of await readdir(fragments)) {
      const data = gunzipSync(await readFile(new URL(file, fragments)));
      assert.ok(data.subarray(0, 12).equals(Buffer.from('pagefind_dcd')));
      const fragment = JSON.parse(data.subarray(12));
      if ((fragment.url.startsWith('/zh-cn/') ? 'zh-cn' : 'en') === language) {
        assert.ok(!fragment.meta.title.includes('\u200B'), 'Display titles must remain unsegmented.');
        titleCases.push([fragment.url, [fragment.meta.title]]);
      }
    }
    const source = new URL(language === 'zh-cn' ? '../src/content/docs/zh-cn/docs/' : '../src/content/docs/docs/', import.meta.url);
    const documentationCount = (await readdir(source, { recursive: true })).filter((file) => /\.mdx?$/.test(file)).length;
    assert.equal(titleCases.length, documentationCount, 'Every document in each language needs a search regression.');
    const allCases = [...cases, ...titleCases];
    const queries = [...new Set(allCases.flatMap(([, terms]) => terms)), noMatch];
    const output = await execute(process.execPath, [probe, language, JSON.stringify(queries)], { timeout: 30_000 });
    const results = new Map(JSON.parse(output.stdout).map((item) => [item.query, item.pages]));
    for (const [route, terms] of allCases) {
      for (const query of terms) {
        const pages = results.get(query);
        assert.ok(pages.some((page) => page.route === route), `Missing ${route} for ${JSON.stringify(query)}`);
        for (const page of pages) assert.ok(!page.title.includes('\u200B'), 'Display titles must remain unsegmented.');
        if (language === 'zh-cn' && route.endsWith('/build-from-source/')) {
          assert.equal(pages.find((page) => page.route === route).title, '从源码构建');
        }
      }
    }
    assert.deepEqual(results.get(noMatch), []);
  });
}
