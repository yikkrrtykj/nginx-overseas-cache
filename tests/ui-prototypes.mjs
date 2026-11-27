
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { gzipSync } from 'node:zlib';
import { chromium } from '../web/node_modules/playwright-core/index.mjs';

const artifacts = path.resolve('artifacts/ui-prototypes');
const registryRef = 'shadcn@4.23.1';
const registry = {"button":"2007374034d564bb926915baa057e500cab631a4","input":"06c96abffafcd0fbbedb24778f0c2f668e1b2109","dialog":"8c68524ade906dc3754769729ae3cbcb1b9d7b55","table":"160cf7aa14b8467a777eb2405053d2d968f641b9"};
const react = { react: '19.2.8', 'react-dom': '19.2.8' };
const tailwind = { tailwindcss: '4.1.18', '@tailwindcss/vite': '4.1.18' };
const variants = {
  native: {},
  daisyui: { ...tailwind, daisyui: '5.8.3' },
  shadcn: { ...react, ...tailwind, '@base-ui/react': '1.9.0', cn: '0.2.2',
    'class-variance-authority': '0.7.1', 'lucide-react': '0.474.0' },
  mantine: { ...react, '@mantine/core': '9.8.2', '@mantine/hooks': '9.8.2' },
};
const labels = ['Origin', 'Channel', 'Client count', 'Upstream state'];
const rows = Array.from({ length: 20 }, (_, i) => [
  'origin-' + (i % 3 + 1) + '.example', 'fixture-' + (i + 1), String(i + 2), i % 5 ? 'Ready' : 'Reconnecting',
]);
const data = 'const labels = ' + JSON.stringify(labels) + '; const rows = ' + JSON.stringify(rows) + ';\n';
const title = 'Connection group configuration';

const css = `
  :root { color-scheme: dark; font: 14px system-ui; background: #101316; color: #edf0f2;
    --background: #101316; --foreground: #edf0f2; --muted-foreground: #aab4bd; --muted: #20262c; }
  * { box-sizing: border-box; }
  main { margin: 20px auto; padding: 16px; width: min(calc(100% - 28px), 850px); }
  table { width: 100%; margin-top: 12px; border-collapse: collapse; }
  th, td { text-align: left; padding: 5px 10px; border-bottom: 1px solid #343b42; white-space: nowrap; }
  input, button { font: inherit; margin: 4px; }
  button { cursor: pointer; }
  :focus-visible { outline: 2px solid #f0bd65; outline-offset: 2px; }
  .control, .cn-button, .cn-input { padding: 6px 10px; border: 1px solid #343b42; border-radius: 6px; background: #20262c; color: #edf0f2; }
  .cn-table-container { overflow: auto; }
  .cn-dialog-overlay { position: fixed; inset: 0; background: #080a0cb0; }
  dialog, .cn-dialog-content { background: #191e23; color: #edf0f2; border: 1px solid #343b42; border-radius: 6px; padding: 14px; }
  .cn-dialog-content { position: fixed; z-index: 1; top: 45%; left: 50%; transform: translate(-50%,-50%); width: min(520px, 90vw); }
  .cn-dialog-content > button:last-child svg { width: 16px; height: 16px; }
`;

function entry(variant) {
  if (variant === 'native' || variant === 'daisyui') {
    const button = variant === 'native' ? 'control' : 'btn btn-sm';
    const input = variant === 'native' ? 'control' : 'input input-sm';
    return {
      name: 'entry.ts',
      content: `import './style.css';\n` + data + `
      const main = document.querySelector('main')!;
      const make = <K extends keyof HTMLElementTagNameMap>(tag: K, label = ''): HTMLElementTagNameMap[K] => {
        const el = document.createElement(tag); el.textContent = label; return el;
      };
      const heading = make('h1', 'Connection groups');
      const label = make('label', 'Origin hostname');
      const input = make('input'); input.className = '${input}'; input.value = 'origin-1.example'; label.append(input);
      const trigger = make('button', 'Apply configuration'); trigger.className = '${button}';
      const modal = make('dialog');
      modal.setAttribute('aria-labelledby', 'title');
      const title = make('h2', '${title}'); title.id = 'title';
      const accept = make('button', 'Confirm'); accept.className = '${button}';
      accept.addEventListener('click', () => modal.close());
      modal.append(title, accept); trigger.addEventListener('click', () => modal.showModal());
      const table = make('table'); table.className = '${variant === 'daisyui' ? 'table table-xs' : ''}';
      const header = table.createTHead().insertRow(); header.append(...labels.map(label => make('th', label)));
      const body = table.createTBody();
      for (const row of rows) body.insertRow().append(...row.map(label => make('td', label)));
      main.append(heading, label, trigger, modal, table);`,
    };
  }
  const nativeReact = variant === 'mantine';
  return {
    name: 'entry.tsx',
    content: `import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
` + (nativeReact
      ? `import { Button, MantineProvider, Modal, Table, TextInput } from '@mantine/core';\nimport '@mantine/core/styles.css';\n`
      : `import { Dialog, DialogTrigger, DialogContent, DialogTitle, DialogDescription, DialogClose } from './dialog';\nimport { Button } from './button';\nimport { Input } from './input';\nimport { Table, TableHeader, TableRow, TableHead, TableBody, TableCell } from './table';\n`)
      + `import './style.css';\n` + data + (nativeReact
        ? `
function App() {
  const [open, set] = useState(false);
  return <MantineProvider defaultColorScheme="dark"><h1>Connection groups</h1>
      <TextInput label="Origin hostname" defaultValue="origin-1.example" />
      <Button onClick={() => set(true)}>Apply configuration</Button>
      <Modal opened={open} onClose={() => set(false)} title="${title}">
        <Button onClick={() => set(false)}>Confirm</Button>
      </Modal>
      <Table verticalSpacing="xs"><Table.Thead><Table.Tr>{labels.map(label => <Table.Th key={label}>{label}</Table.Th>)}</Table.Tr></Table.Thead>
      <Table.Tbody>{rows.map((row,i) => <Table.Tr key={i}>{row.map((value,j) => <Table.Td key={j}>{value}</Table.Td>)}</Table.Tr>)}</Table.Tbody></Table>
    </MantineProvider>;
}` : `
function App() {
  return <><h1>Connection groups</h1><label>Origin hostname<Input defaultValue="origin-1.example" /></label>
    <Dialog><DialogTrigger render={<Button />}>Apply configuration</DialogTrigger>
      <DialogContent><DialogTitle>${title}</DialogTitle><DialogDescription>Review and confirm.</DialogDescription>
        <DialogClose render={<Button />}>Confirm</DialogClose></DialogContent></Dialog>
    <Table><TableHeader><TableRow>{labels.map(label => <TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader>
    <TableBody>{rows.map((row,i) => <TableRow key={i}>{row.map((value,j) => <TableCell key={j}>{value}</TableCell>)}</TableRow>)}</TableBody></Table>
  </>;
}`)
      + "\ncreateRoot(document.querySelector('main')!).render(<App />);",
  };
}

async function build(tmp, variant, dependencies) {
  const folder = path.join(tmp, variant);
  await mkdir(folder);
  const source = entry(variant);
  await writeFile(path.join(folder, 'package.json'), JSON.stringify({
    name: 'overseas-cache-prototype-' + variant, version: '1.0.0', private: true, type: 'module',
    dependencies: { vite: '7.3.7', ...dependencies },
  }));
  await writeFile(path.join(folder, 'index.html'),
    '<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connection groups</title><main></main><script type="module" src="/' + source.name + '"></script></html>');
  await writeFile(path.join(folder, source.name), source.content);
  const utilities = variant === 'daisyui' || variant === 'shadcn';
  const imports = utilities ? '@import "tailwindcss";\n' : '';
  const daisy = variant === 'daisyui' ? '@plugin "daisyui" { themes: dark --default; }\n' : '';
  await writeFile(path.join(folder, 'style.css'), imports + daisy + css);
  if (utilities) await writeFile(path.join(folder, 'vite.config.js'),
    "import tailwind from '@tailwindcss/vite'; export default {plugins:[tailwind()]};");
  if (variant === 'shadcn') {
    for (const [file, sha] of Object.entries(registry)) {
      const url = 'https://raw.githubusercontent.com/shadcn-ui/ui/' + registryRef + '/apps/v4/registry/bases/base/ui/' + file + '.tsx';
      const res = await fetch(url, { signal: AbortSignal.timeout(20000) });
      assert.equal(res.status, 200, url);
      const bytes = Buffer.from(await res.arrayBuffer());
      assert.equal(createHash('sha1').update('blob ' + bytes.length + '\0').update(bytes).digest('hex'), sha);
      const source = bytes.toString('utf8')
        .replace('@/registry/bases/base/ui/button', './button')
        .replace('import { IconPlaceholder } from "@/app/(create)/components/icon-placeholder"', 'import { XIcon } from "lucide-react"')
        .replace(/<IconPlaceholder[\s\S]*?\/>/g, '<XIcon />');
      await writeFile(path.join(folder, file + '.tsx'), source);
    }
  }
  const npm = process.platform === 'win32' ? 'npm.cmd' : 'npm';
  const install = spawnSync(npm, ['install', '--ignore-scripts', '--no-audit', '--no-fund', '--prefer-offline'], { cwd: folder, stdio: 'inherit' });
  assert.equal(install.status, 0, 'dependency install ' + variant);
  const compilation = spawnSync(process.execPath, [path.join(folder, 'node_modules/vite/bin/vite.js'), 'build'], { cwd: folder, stdio: 'inherit' });
  assert.equal(compilation.status, 0, 'build ' + variant);
  const lock = await readFile(path.join(folder, 'package-lock.json'), 'utf8');
  await writeFile(path.join(artifacts, variant + '-lock.json'), lock);
  return { dist: path.join(folder, 'dist'), packages: Object.keys(JSON.parse(lock).packages).length - 1 };
}

async function measure(browser, variant, result) {
  const files = [];
  for (const name of await readdir(result.dist, { recursive: true })) {
    if (/\.(html|css|js)$/.test(name)) files.push([name, await readFile(path.join(result.dist, name))]);
  }
  const index = new Map(files.map(([name, data]) => ['/' + name, data]));
  const contentType = { '.html': 'text/html', '.css': 'text/css', '.js': 'text/javascript' };
  const server = http.createServer((req, res) => {
    const route = new URL(req.url, 'http://localhost').pathname;
    const name = route === '/' ? '/index.html' : route;
    const data = index.get(name);
    if (!data) { res.writeHead(404).end(); return; }
    res.writeHead(200, { 'Content-Type': contentType[path.extname(name)], 'Cache-Control': 'no-store' }).end(data);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const origin = 'http://127.0.0.1:' + server.address().port;
  const timings = [];
  try {
    for (let i = 0; i < 5; i++) {
      const page = await browser.newPage({ viewport: { width: 960, height: 780 } });
      const errors = [];
      page.on('pageerror', err => errors.push(err.message));
      await page.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
      await page.goto(origin);
      await page.getByRole('row').nth(20).waitFor();
      timings.push(await page.evaluate(() => performance.now()));
      assert.equal(await page.getByRole('textbox', { name: 'Origin hostname' }).isVisible(), true);
      const trigger = page.getByRole('button', { name: 'Apply configuration' });
      await trigger.focus();
      await page.keyboard.press('Enter');
      const dialog = page.getByRole('dialog', { name: title });
      await dialog.waitFor();
      await page.keyboard.press('Escape');
      await dialog.waitFor({ state: 'hidden' });
      assert.deepEqual(errors, []);
      if (i === 0) await page.screenshot({ path: path.join(artifacts, variant + '.png') });
      await page.close();
    }
  } finally {
    await new Promise(resolve => server.close(resolve));
  }
  timings.sort((a, b) => a - b);
  return { variant, npmPackages: result.packages, assetFiles: files.length,
    plainBytes: files.reduce((acc, [, b]) => acc + b.length, 0),
    gzipBytes: files.reduce((acc, [, b]) => acc + gzipSync(b).length, 0),
    medianRenderedMs: Number(timings[2].toFixed(1)), keyboardDialog: true };
}

async function main() {
  await mkdir(artifacts, { recursive: true });
  const tmp = await mkdtemp(path.join(os.tmpdir(), 'overseas-ui-prototypes-'));
  const browser = await chromium.launch();
  try {
    const results = [];
    for (const [variant, deps] of Object.entries(variants)) {
      results.push(await measure(browser, variant, await build(tmp, variant, deps)));
      await writeFile(path.join(artifacts, 'comparison.json'), JSON.stringify(results, null, 2) + '\n');
    }
    console.table(results);
  } finally {
    await browser.close();
    await rm(tmp, { recursive: true, force: true });
  }
}
await main();
