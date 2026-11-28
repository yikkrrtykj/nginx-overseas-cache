const assert = require('node:assert/strict');
const { chromium } = require('../../web/node_modules/playwright-core');

async function main() {
  const [version, base, evidence] = process.argv.slice(2);
  const browser = await chromium.launch({ args: ['--no-sandbox'] });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await context.newPage();
    await page.goto(base + '/login');
    await page.locator('#username').fill('admin');
    await page.locator('#password').fill(process.env.QUAL_PASSWORD);
    const login = page.waitForResponse(response => response.url() === base + '/api/login');
    await page.locator('#password').press('Enter');
    assert.equal((await login).status(), 200);
    await page.waitForURL(base + '/');
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
    }
  } finally {
    await browser.close();
  }
}

main().catch(error => {
  console.error(error.message);
  process.exitCode = 1;
});
