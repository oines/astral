import { readdir, readFile, rm } from 'node:fs/promises';
import { join, relative, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import * as pagefind from 'pagefind';
import { parse, serialize } from 'parse5';

const chineseWordSegmenter = new Intl.Segmenter('zh-CN', { granularity: 'word' });
const excludedElements = new Set(['script', 'style', 'template', 'code', 'pre']);
const attribute = (node, name) => node.attrs?.find((item) => item.name === name)?.value;
const textContent = (node) => node.nodeName === '#text' ? node.value : (node.childNodes || []).map(textContent).join('');

/** Align only the Chinese index input with Pagefind's query-side ICU segmentation. */
export function prepareSearchHTML(html) {
  const document = parse(html);
  const root = document.childNodes.find((node) => node.tagName === 'html');
  if (!/^zh(?:-|$)/i.test(attribute(root, 'lang') || '')) return html;
  let changed = false;
  function visit(node, indexed = false) {
    if (excludedElements.has(node.tagName) || attribute(node, 'data-pagefind-ignore') !== undefined) return;
    indexed ||= attribute(node, 'data-pagefind-body') !== undefined;
    let metadata = node.attrs?.find((item) => item.name === 'data-pagefind-meta');
    if (indexed && node.tagName === 'h1' && !metadata) {
      metadata = { name: 'data-pagefind-meta', value: 'title' };
      node.attrs.push(metadata);
    }
    if (indexed && metadata && /(^|,)\s*title\s*(?=,|$)/.test(metadata.value)) {
      // Keep display metadata verbatim while segmenting the heading's index text.
      node.attrs.push({ name: 'data-astral-search-title', value: textContent(node) });
      metadata.value = metadata.value.replace(/(^|,)\s*title\s*(?=,|$)/g, '$1title[data-astral-search-title]');
    }
    if (indexed && node.nodeName === '#text' && /\p{Script=Han}/u.test(node.value)) {
      node.value = node.value.replace(/\p{Script=Han}+/gu, (run) => [...chineseWordSegmenter.segment(run)].map((item) => item.segment).join('\u200B'));
      changed = true;
    }
    for (const child of node.childNodes || []) visit(child, indexed);
  }
  visit(document);
  return changed ? serialize(document) : html;
}

async function htmlFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = await Promise.all(entries.map((entry) => entry.isDirectory()
    ? htmlFiles(join(directory, entry.name))
    : entry.name.endsWith('.html') ? [join(directory, entry.name)] : []));
  return files.flat().sort();
}

function checked(response) {
  if (response.errors.length) throw new Error(`Pagefind: ${response.errors.join('; ')}`);
  return response;
}

export async function buildSearchIndex(directory = resolve('dist')) {
  const files = await htmlFiles(directory);
  const { index } = checked(await pagefind.createIndex());
  if (!index) throw new Error('Pagefind did not create a search index.');
  let aligned = 0;
  try {
    for (const file of files) {
      const html = await readFile(file, 'utf8');
      const input = prepareSearchHTML(html);
      if (input !== html) aligned++;
      checked(await index.addHTMLFile({ sourcePath: relative(directory, file), content: input }));
    }
    const outputPath = join(directory, 'pagefind');
    await rm(outputPath, { recursive: true, force: true });
    checked(await index.writeFiles({ outputPath }));
    console.log(`Built Pagefind index from ${files.length} HTML files; aligned ${aligned} Chinese pages with query segmentation.`);
  } finally {
    await pagefind.close();
  }
}

// Pagefind 1.5.2 uses charabia for indexing and Intl.Segmenter for queries.
// Pre-segment an in-memory HTML copy; never change the published HTML or query.
// Remove after adopting an upstream release containing Pagefind/pagefind#1383.
if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  await buildSearchIndex(fileURLToPath(new URL('../dist/', import.meta.url)));
}
