const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { mkdir, mkdtemp, rm } = require('node:fs/promises');
const { tmpdir } = require('node:os');
const path = require('node:path');
const { chromium } = require('../web/node_modules/playwright-core');

const root = path.resolve(__dirname, '..');
const artifact = path.resolve(process.env.OVERSEAS_SCREENSHOTS || path.join(root, 'artifacts', 'browser'));
const executable = process.env.OVERSEAS_BROWSER_EXECUTABLE;
const sizes = [[1920, 1080], [1440, 900], [1280, 720], [390, 844]];
const oldPassword = 'Local browser fixture password';
const newPassword = 'Updated browser fixture password';

function launch(processPath, args, opts) {
  const p = spawn(processPath, args, { ...opts, stdio: ['ignore', 'pipe', 'inherit'] });
  return { p, ready: new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Fixture startup timed out')), 30000);
    let logs = '';
    p.stdout.on('data', out => {
      logs += out.toString();
      const match = logs.match(/BROWSER_READY (127\.0\.0\.1:\d+)/);
      if (match) {
        clearTimeout(timer);
        resolve('http://' + match[1]);
      }
    });
    p.on('error', err => { clearTimeout(timer); reject(err); });
    p.on('exit', code => { clearTimeout(timer); reject(new Error('Fixture exited: ' + code + '\n' + logs)); });
  }) };
}

async function whenText(page, selector, text) {
  await page.waitForFunction(([selector, value]) => document.querySelector(selector)?.textContent?.includes(value), [selector, text]);
}

async function login(page, url, password) {
  await page.goto(url + '/login');
  await page.locator('#username').fill('admin');
  await page.locator('#password').fill(password);
  await page.locator('#password').press('Enter');
  await page.waitForURL(url + '/');
}

async function fixtures(tmp) {
  const exe = path.join(tmp, process.platform === 'win32' ? 'browser.test.exe' : 'browser.test');
  await new Promise((resolve, reject) => {
    const build = spawn('go', ['test', '-c', '-o', exe, './internal/admin'], { cwd: root, stdio: 'inherit' });
    build.on('error', reject);
    build.on('exit', code => code ? reject(new Error('Fixture build failed')) : resolve());
  });
  return launch(exe, ['-test.run=^TestBrowserFixture$'], {
    cwd: path.join(root, 'internal/admin'),
    env: { ...process.env, OVERSEAS_BROWSER_LISTEN: '127.0.0.1:0' },
  });
}

async function run() {
  await mkdir(artifact, { recursive: true });
  const tmp = await mkdtemp(path.join(tmpdir(), 'overseas-browser-'));
  let browser, go, python;
  try {
    go = await fixtures(tmp);
    const url = await go.ready;
    python = launch(process.env.OVERSEAS_PYTHON || 'python3', ['-B', path.join(__dirname, 'browser_v1.py')], { cwd: root });
    const baseline = await python.ready;
    browser = await chromium.launch({ args: ['--disable-logging'], ...(executable ? { executablePath: executable } : {}) });
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const unexpected = [];
    await page.route('**/*', route => {
      const target = new URL(route.request().url());
      if (target.origin === url) return route.continue();
      unexpected.push(target.origin);
      return route.abort();
    });
    const errors = [];
    page.on('pageerror', err => errors.push(err.message));

    await page.goto(url + '/');
    await page.waitForURL(url + '/login');
    assert.equal((await page.request.get(url + '/api/v2/config')).status(), 401);
    await page.screenshot({ path: path.join(artifact, 'v2-login.png') });
    await login(page, url, oldPassword);
    await whenText(page, '#local-clients', '29');
    await page.waitForFunction(() => !document.querySelector('#save').disabled);
    assert.equal((await page.request.post(url + '/api/v2/logout', {
      headers: { Origin: 'https://other.invalid' }, data: {},
    })).status(), 403);

    const before = await browser.newPage();
    await login(before, baseline, oldPassword);
    await whenText(before, 'body', 'arena-stage');
    for (const [width, height] of sizes) {
      for (const [version, target] of [['v1', before], ['v2', page]]) {
        await target.setViewportSize({ width, height });
        await target.screenshot({ path: path.join(artifact, version + '-' + width + 'x' + height + '.png') });
      }
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'document overflows viewport at ' + width + 'x' + height);
    }
    await before.close();
    await page.setViewportSize({ width: 1440, height: 900 });
    const trigger = page.locator('#groups button').first();
    await trigger.focus();
    await page.keyboard.press('Enter');
    assert.equal(await page.locator('#inspector').isVisible(), true);
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#inspector').isVisible(), false);

    await page.locator('#source').fill('https://origin.example/render?channelId=new-stage&accountId=renderer&token=synthetic-browser-token');
    await page.locator('#discover').click();
    await page.waitForFunction(() => document.querySelector('#channel').value === 'new-stage');
    assert.equal(await page.locator('#cdn').inputValue(), 'cdn.origin.example');
    await page.locator('#source').fill('https://origin.example/render?channelId=other-stage');
    assert.equal(await page.locator('#save').isDisabled(), true);
    await page.locator('#source').fill('https://origin.example/render?channelId=new-stage&accountId=renderer&token=synthetic-browser-token');
    await page.locator('#discover').click();
    await page.waitForFunction(() => !document.querySelector('#save').disabled);
    await page.locator('#save').click();
    await whenText(page, '#diff', 'new-stage');
    assert.equal((await page.locator('#diff').textContent()).includes('synthetic-browser-token'), false);
    await page.locator('#confirm button[value=confirm]').click();
    await whenText(page, '#operation', 'Nginx 检查和重载完成');
    assert.match(await page.locator('#backup').inputValue(), /^admin-/);

    await page.locator('#channel').fill('fail-config');
    await page.locator('#save').click();
    await page.locator('#confirm button[value=confirm]').click();
    await whenText(page, '#operation', '已恢复原配置');
    const live = await (await page.request.get(url + '/api/v2/config')).json();
    assert.equal(live.config.channelId, 'new-stage');

    for (const [mode, tone, value] of [
      ['loading', 'loading', '—'], ['empty', 'healthy', '0'],
      ['stale', 'stale', '—'], ['error', 'error', '—'],
    ]) {
      await page.request.post(url + '/__fixture/state?mode=' + mode, { timeout: 5000 });
      await page.reload({ waitUntil: 'domcontentloaded' });
      await page.waitForFunction(tone => document.querySelector('#status-badge')?.dataset.tone === tone, tone);
      assert.equal(await page.locator('#local-clients').textContent(), value);
      await page.screenshot({ path: path.join(artifact, 'v2-' + mode + '.png') });
    }

    await page.locator('#password-open').click();
    await page.locator('#current-password').fill(oldPassword);
    await page.locator('#new-password').fill(newPassword);
    await page.locator('#password-form button[type=submit]').click();
    await page.waitForURL(url + '/login');
    await login(page, url, newPassword);
    await page.locator('#logout').click();
    await page.waitForURL(url + '/login');
    assert.equal((await page.request.get(url + '/api/v2/status')).status(), 401);
    assert.deepEqual(unexpected, []);
    assert.deepEqual(errors, []);
    console.log('Browser assertions passed. Artifacts:', artifact);
  } finally {
    await browser?.close();
    go?.p.kill();
    python?.p.kill();
    await rm(tmp, { recursive: true, force: true, maxRetries: 10, retryDelay: 150 });
  }
}
run().catch(err => { console.error(err); process.exitCode = 1; });
