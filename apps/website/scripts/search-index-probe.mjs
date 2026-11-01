// Run the generated browser bundle against local assets, one language per process.
// This keeps Pagefind's language-specific WASM state separate, like a page load.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

const bundle = new URL('../dist/pagefind/', import.meta.url);
const origin = 'https://search-test.invalid';
const [language, queriesJSON] = process.argv.slice(2);
globalThis.document = { currentScript: null, querySelector: () => ({ getAttribute: () => language }) };
globalThis.fetch = async (input) => {
  const url = new URL(typeof input === 'string' ? input : input.url || input.href);
  assert.equal(url.origin, origin, 'Search tests must never make network requests.');
  assert.ok(url.pathname.startsWith('/pagefind/'));
  const file = new URL(url.pathname.slice('/pagefind/'.length), bundle);
  assert.ok(fileURLToPath(file).startsWith(fileURLToPath(bundle)));
  return new globalThis.Response(await readFile(file), { status: 200 });
};
const pagefind = await import(new URL('pagefind.js', bundle));
await pagefind.options({ basePath: `${origin}/pagefind/`, baseUrl: `${origin}/` });
const results = [];
try {
  for (const query of JSON.parse(queriesJSON)) {
    const result = await pagefind.search(query);
    const pages = await Promise.all(result.results.map((item) => item.data()));
    results.push({ query, pages: pages.map((page) => ({ route: new URL(page.url).pathname, title: page.meta.title })) });
  }
} finally {
  await pagefind.destroy();
}
console.log(JSON.stringify(results));
