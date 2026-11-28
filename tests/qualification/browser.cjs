const assert = require('node:assert/strict');
const { chromium } = require('../../web/node_modules/playwright-core');

async function signIn(page, base, version, password) {
  await page.goto(base + '/login');
  await page.locator('#username').fill('admin');
  await page.locator('#password').fill(password);
  const path = version === 'v1' ? '/api/login' : '/api/v2/login';
  const login = page.waitForResponse(response => response.url() === base + path);
  await page.locator('#password').press('Enter');
  assert.equal((await login).status(), 200);
  await page.waitForURL(base + '/');
}

async function rotate(page, base, currentPassword, newPassword) {
  await page.locator('#password-open').click();
  await page.locator('#current-password').fill(currentPassword);
  await page.locator('#new-password').fill(newPassword);
  const result = page.waitForResponse(response => response.url() === base + '/api/v2/password');
  await page.locator('#password-form button[type=submit]').click();
  assert.equal((await result).status(), 200);
  await page.waitForURL(base + '/login');
}

async function main() {
  const [version, base, evidence] = process.argv.slice(2);
  const browser = await chromium.launch({ args: ['--no-sandbox'], executablePath: process.env.QUAL_BROWSER_EXECUTABLE || undefined });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await context.newPage();
    const originalPassword = process.env.QUAL_PASSWORD;
    await signIn(page, base, version, originalPassword);
    if (version === 'maintenance') {
      const source = new URL('https://webcoder.atum.live/render');
      source.search = new URLSearchParams({
        rendererMode: 'onair', channelId: 'qualification', accountId: 'pgs26p', token: process.env.QUAL_TOKEN
      });
      await page.locator('#source').fill(source.toString());
      const discovery = page.waitForResponse(response => response.url() === base + '/api/v2/discover');
      await page.locator('#discover').click();
      const response = await discovery;
      assert.equal(response.status(), 200);
      const reply = await response.json();
      assert.equal(reply.ok, true);
      assert.equal(await page.locator('#origin').inputValue(), 'webcoder.atum.live');
      await rotate(page, base, originalPassword, originalPassword + '_rotated');
      assert.equal((await context.request.get(base + '/api/v2/config')).status(), 401);
      await signIn(page, base, version, originalPassword + '_rotated');
      await rotate(page, base, originalPassword + '_rotated', originalPassword);
      await signIn(page, base, version, originalPassword);
      console.log('PASS Chromium discovery and live password rotation with session revocation');
      return;
    }
    const configPath = version === 'v1' ? '/api/config' : '/api/v2/config';
    const config = await context.request.get(base + configPath);
    assert.equal(config.status(), 200);
    const report = await config.json();
    assert.equal((report.config ?? report).channelId, 'qualification');
    await page.screenshot({ path: evidence + '/admin-' + version + '.png' });
    if (version === 'v2') {
      await page.locator('#configuration').scrollIntoViewIfNeeded();
      await page.locator('#channel').fill('browser-qualified-change');
      await page.locator('#save').click();
      const applied = page.waitForResponse(response =>
        response.url().endsWith('/api/v2/config') && response.request().method() === 'POST');
      await page.locator('#confirm button[value=confirm]').click();
      assert.equal((await applied).status(), 200);
      const saved = await context.request.get(base + configPath);
      const after = await saved.json();
      assert.equal((after.config ?? after).channelId, 'browser-qualified-change');
      await page.locator('#logout').click();
      await page.waitForURL(base + '/login');
      assert.equal((await context.request.get(base + configPath)).status(), 401);
      console.log('PASS Chromium login, installed Helper-backed configuration save and logout');
    }
  } finally {
    await browser.close();
  }
}

main().catch(error => {
  console.error(error.message);
  process.exitCode = 1;
});
