import { readdir, readFile } from 'node:fs/promises';
import { resolve, relative, join, extname } from 'node:path';

const root = resolve('dist');
const errors = [];
const origin = 'https://local-site.invalid';
async function walk(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  return (await Promise.all(entries.map((entry) => entry.isDirectory() ? walk(join(dir, entry.name)) : join(dir, entry.name)))).flat();
}
const files = await walk(root).catch(() => { throw new Error('Run pnpm build before pnpm check:site.'); });
const fileSet = new Set(files);
const htmlFiles = files.filter((file) => file.endsWith('.html'));
const pages = new Map(await Promise.all(htmlFiles.map(async (file) => [file, await readFile(file, 'utf8')])));
function decode(value) {
  return value.replace(/&(?:amp|quot|apos|lt|gt|#(\d+)|#x([0-9a-f]+));/gi, (match, dec, hex) => dec ? String.fromCodePoint(Number(dec)) : hex ? String.fromCodePoint(parseInt(hex, 16)) : ({ '&amp;': '&', '&quot;': '"', '&apos;': "'", '&lt;': '<', '&gt;': '>' }[match] || match));
}
function attrs(html, name) {
  return [...html.matchAll(new RegExp(`\\b${name}\\s*=\\s*(?:"([^"]*)"|'([^']*)')`, 'g'))].map((match) => decode(match[1] ?? match[2]));
}
const ids = new Map([...pages].map(([file, html]) => [file, new Set(attrs(html, 'id'))]));
let checkedLinks = 0;
for (const [file, html] of pages) {
  const route = '/' + relative(root, file).replace(/index\.html$/, '');
  const isRedirect = /http-equiv="refresh"/i.test(html);
  if (!isRedirect) {
    if ((html.match(/<h1(?:\s|>)/g) || []).length !== 1) errors.push(`${route}: expected one h1`);
    if (!/<meta\b[^>]*name="description"[^>]*content="[^"]+"/.test(html)) errors.push(`${route}: missing description`);
    if (!/<html\b[^>]*lang="(?:en|zh-CN)"/.test(html)) errors.push(`${route}: missing language`);
  }
  for (const href of [...attrs(html, 'href'), ...attrs(html, 'src')]) {
    if (/^(?:https?:|mailto:|tel:|data:|javascript:|\/\/)/i.test(href)) continue;
    const url = new URL(href, origin + route);
    const path = decodeURIComponent(url.pathname);
    let target = resolve(root, '.' + path);
    if (path.endsWith('/')) target = join(target, 'index.html');
    else if (!fileSet.has(target) && !extname(path)) target = join(target, 'index.html');
    if (!fileSet.has(target)) { errors.push(`${route}: missing target ${href}`); continue; }
    if (url.hash && ids.has(target) && !ids.get(target).has(decodeURIComponent(url.hash.slice(1)))) errors.push(`${route}: missing anchor ${href}`);
    checkedLinks++;
  }
}
const docs = (await walk(resolve('src/content/docs/docs'))).filter((file) => /\.mdx?$/.test(file));
const translated = (await walk(resolve('src/content/docs/zh-cn/docs'))).filter((file) => /\.mdx?$/.test(file));
const sourceNames = docs.map((file) => relative(resolve('src/content/docs/docs'), file)).sort();
const translatedNames = translated.map((file) => relative(resolve('src/content/docs/zh-cn/docs'), file)).sort();
if (JSON.stringify(sourceNames) !== JSON.stringify(translatedNames)) errors.push('English and Chinese documentation routes do not match.');
for (const file of [...docs, ...translated]) {
  const body = await readFile(file, 'utf8');
  if (/source-note|产品依据|Product sources?/i.test(body)) errors.push(`${relative(process.cwd(), file)}: obsolete source footer`);
}
for (const asset of ['pagefind/pagefind.js', 'pagefind/pagefind-entry.json', 'robots.txt', '_redirects', '_headers']) {
  if (!fileSet.has(resolve(root, asset))) errors.push(`Missing generated asset: ${asset}`);
}
const redirects = (await readFile(resolve(root, '_redirects'), 'utf8')).split('\n').filter(line => line.trim() && !line.startsWith('#'));
const redirectSources = new Set();
for (const line of redirects) {
  const [source, destination, status, extra] = line.trim().split(/\s+/);
  if (!source.startsWith('/') || !destination?.startsWith('/') || status !== '301' || extra) {
    errors.push(`Invalid static redirect: ${line}`);
    continue;
  }
  if (redirectSources.has(source)) errors.push(`Duplicate redirect source: ${source}`);
  redirectSources.add(source);
  const target = resolve(root, '.' + destination, 'index.html');
  if (!pages.has(target) || /http-equiv="refresh"/i.test(pages.get(target))) errors.push(`Redirect does not reach a content page: ${line}`);
}
const robots = await readFile(resolve(root, 'robots.txt'), 'utf8');
const productionSite = process.env.SITE_URL?.trim();
if (productionSite) {
  const sitemap = resolve(root, 'sitemap-index.xml');
  if (!fileSet.has(sitemap)) errors.push('Production build is missing its sitemap.');
  if (!robots.includes(`Sitemap: ${new URL('sitemap-index.xml', productionSite)}`) || robots.includes('Disallow: /')) errors.push('Production robots.txt does not allow indexing with the configured sitemap.');
  for (const file of docs) {
    const slug = relative(resolve('src/content/docs/docs'), file).replace(/\.mdx?$/, '');
    for (const prefix of ['', '/zh-cn']) {
      const route = `${prefix}/docs/${slug}/`;
      const html = pages.get(resolve(root, '.' + route, 'index.html')) || '';
      const expected = new URL(route, productionSite).href;
      if (!html.includes(`rel="canonical" href="${expected}"`) && !html.includes(`href="${expected}" rel="canonical"`)) errors.push(`Missing production canonical: ${route}`);
    }
  }
} else {
  if (!robots.includes('Disallow: /')) errors.push('Preview builds must disable indexing.');
  if (fileSet.has(resolve(root, 'sitemap-index.xml'))) errors.push('Preview build unexpectedly contains a sitemap.');
}
if (errors.length) {
  console.error([...new Set(errors)].join('\n'));
  process.exitCode = 1;
} else {
  console.log(`Passed: ${htmlFiles.length} HTML pages, ${checkedLinks} local links/assets/anchors, ${docs.length} documentation routes per language, ${redirects.length} redirects, search assets, and ${productionSite ? 'production' : 'preview'} indexing rules.`);
}
