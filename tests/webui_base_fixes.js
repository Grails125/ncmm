const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const [baseURL, testRoot] = process.argv.slice(2);
if (!baseURL || !testRoot || !['127.0.0.1', 'localhost'].includes(new URL(baseURL).hostname)) {
  throw new Error('Provide a local isolated WebUI URL and output directory');
}

(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  const blockedRuns = [];
  const installations = [];
  const output = path.join(testRoot, 'screenshots');
  fs.mkdirSync(output, { recursive: true });
  page.on('pageerror', error => errors.push(error.message));
  await page.route('**/api/v1/plugins/market', route => route.fulfill({ json: [] }));
  await page.route('**/api/v1/plugins/updates', route => route.fulfill({ json: [] }));
  await page.route('**/api/v1/plugins/install', route => {
    installations.push(route.request().postDataJSON());
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, name: 'test-demo' }) });
  });
  await page.route(/\/api\/v1\/(?:plugins|schedules)\/[^/]+\/run$/, route => {
    blockedRuns.push(route.request().url());
    return route.abort();
  });
  try {
    await page.goto(baseURL, { waitUntil: 'networkidle' });
    const status = await (await page.request.get(`${baseURL}/api/v1/auth/status`)).json();
    assert.equal(status.setupRequired, true, 'use a fresh temporary home');
    const version = `v${status.version.split('\n')[0].replace(/^v/i, '')}`;
    assert.equal(await page.locator('#auth-version').textContent(), version);
    await page.locator('#password-input').fill('BaseFix#2026');
    await page.locator('#setup-confirm-password').fill('BaseFix#2026');
    await page.locator('#setup-submit').click();
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    // 清理这个隔离浏览器的登录态，重现已配置密码但尚未登录的普通入口。
    await page.context().clearCookies();
    await page.reload({ waitUntil: 'networkidle' });
    await page.locator('#login-submit').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#auth-title').textContent(), 'NCMM');
    assert.equal(await page.locator('#auth-version').textContent(), version);
    await page.locator('#login-view').screenshot({ path: path.join(output, 'login-current-version.png') });
    await page.locator('#password-input').fill('BaseFix#2026');
    await page.locator('#login-submit').click();
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    await page.locator('.nav-item[data-page="plugins"]').click();
    for (const [target, tag] of [
      ['owner/demo', 'v2.3.4'],
      ['https://github.com/owner/demo', 'release/v3'],
      ['https://github.com/owner/demo/releases/download/v3/demo.zip', ''],
    ]) {
      await page.locator('#plugin-install-open').click();
      await page.locator('#plugin-install-repo').fill(target);
      await page.locator('#plugin-install-tag').fill(tag);
      await page.locator('#plugin-install-token').fill('test-private-token');
      const response = page.waitForResponse(r => r.url().endsWith('/api/v1/plugins/install'));
      await page.locator('#plugin-install-submit').click();
      assert.equal((await response).status(), 200);
      await page.locator('#plugin-install-dialog').waitFor({ state: 'hidden' });
      const payload = installations.at(-1);
      assert.equal(payload.target, target);
      assert.equal(payload.tag, tag);
      assert.equal(payload.token, 'test-private-token');
      assert.equal(payload.repoOrURL, undefined);
    }
    assert.equal(installations.length, 3);
    assert.deepEqual(blockedRuns, []);
    assert.deepEqual(errors, []);
    console.log('PASS: setup/login version, repository/URL/Tag install form; all install requests mocked; no business tasks');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
