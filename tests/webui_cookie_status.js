const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const [baseURL, testRoot, mode] = process.argv.slice(2);
const reuseTestHome = mode === '--reuse-test-home';
if (mode && !reuseTestHome) throw new Error(`Unknown test option: ${mode}`);
if (!baseURL || !testRoot || !['127.0.0.1', 'localhost'].includes(new URL(baseURL).hostname)) {
  throw new Error('Provide a local, isolated WebUI URL and test output directory');
}
const outputDir = path.join(testRoot, 'screenshots');
fs.mkdirSync(outputDir, { recursive: true });

(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const oldAt = new Date(Date.now() - 3600000).toISOString();
    const loginAt = new Date().toISOString();
    const postLoginAt = new Date(Date.now() + 60000).toISOString();
    const qrAt = new Date(Date.now() + 120000).toISOString();
    const profiles = { './main/cookie.json': { nickname: '主账号' }, './other/cookie.json': { nickname: '另一账号' } };
    let cookieAttempt = 0;
    let runs = [{ id: 'old-invalid', command: 'sign', jobName: '旧 Cookie 失败', status: 'failed', triggeredAt: oldAt, startedAt: oldAt, finishedAt: oldAt, rewards: [{ account: './main/cookie.json', cookieKnown: true, cookieValid: false }] }];
    await page.route('**/api/v1/config', async route => {
      assert.equal(route.request().method(), 'GET', 'this test must not overwrite account configuration');
      const response = await route.fetch();
      const document = await response.json();
      document.data.accounts = { main: './main/cookie.json', secondary: ['./other/cookie.json'] };
      document.accountProfiles = profiles;
      await route.fulfill({ response, json: document });
    });
    await page.route('**/api/v1/runs', route => route.fulfill({ json: runs }));
    await page.route('**/api/v1/play-stats', route => route.fulfill({ json: [] }));
    await page.route('**/api/v1/music-partner/points**', route => route.fulfill({ status: 503, json: { error: '隔离测试未连接网易云账号' } }));
    await page.route('**/api/v1/accounts/cookie', route => {
      cookieAttempt++;
      if (cookieAttempt === 1) return route.fulfill({ status: 400, json: { error: '模拟 Cookie 验证失败' } });
      profiles['./main/cookie.json'].cookieVerifiedAt = loginAt;
      return route.fulfill({ json: { filename: 'cookie.json', main: true, message: '模拟已验证登录成功' } });
    });
    await page.route('**/api/v1/accounts/qrcode', route => route.fulfill({ json: route.request().method() === 'POST'
      ? { id: 'verified-login', status: 'waiting', filename: 'cookie.json', main: true, imageReady: false }
      : null }));
    await page.route('**/api/v1/accounts/qrcode/verified-login', route => {
      profiles['./main/cookie.json'].cookieVerifiedAt = qrAt;
      return route.fulfill({ json: { id: 'verified-login', status: 'succeeded', filename: 'cookie.json', main: true, imageReady: false, message: '模拟二维码登录成功' } });
    });
    await page.goto(baseURL, { waitUntil: 'networkidle' });
    await page.locator('#auth-title').waitFor({ state: 'visible' });
    if (!reuseTestHome) assert.equal(await page.locator('#auth-title').textContent(), '设置管理员密码', 'use a fresh isolated WebUI home');
    else assert.notEqual(await page.locator('#auth-title').textContent(), '设置管理员密码', '--reuse-test-home requires a previously initialized isolated test home');
    await page.locator('#password-input').fill('CookieTest#2026');
    if (reuseTestHome) await page.locator('#login-submit').click();
    else {
      await page.locator('#setup-confirm-password').fill('CookieTest#2026');
      await page.locator('#setup-submit').click();
    }
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    await page.locator('.nav-item[data-page="accounts"]').click();
    const badges = page.locator('#accounts-table .cookie-cell .badge-pill');
    const expectBadges = async values => {
      await page.waitForFunction(expected => JSON.stringify([...document.querySelectorAll('#accounts-table .cookie-cell .badge-pill')].map(node => node.textContent)) === JSON.stringify(expected), values);
      assert.deepEqual(await badges.allTextContents(), values);
    };
    await expectBadges(['失效', '未知']);
    await page.locator('[data-account-action="edit"]').first().click();
    await page.locator('[data-login-method="cookie"]').click();
    await page.locator('#cookie-content').fill('MUSIC_U=isolated-invalid-fixture');
    await page.locator('#account-submit').click();
    await page.locator('.toast.error').filter({ hasText: '模拟 Cookie 验证失败' }).waitFor();
    await expectBadges(['失效', '未知']);
    await page.locator('#account-submit').click();
    await expectBadges(['有效', '未知']);
    assert.equal((await page.locator('#stat-account-health').textContent()).trim(), '1 有效 · 0 失效 · 1 未知');
    await page.reload({ waitUntil: 'networkidle' });
    await expectBadges(['有效', '未知']);

    runs = [{ id: 'new-invalid', command: 'sign', status: 'failed', startedAt: postLoginAt, finishedAt: postLoginAt, rewards: [{ account: './main/cookie.json', cookieKnown: true, cookieValid: false }] }];
    await page.reload({ waitUntil: 'networkidle' });
    await expectBadges(['失效', '未知']);
    await page.locator('[data-account-action="edit"]').first().click();
    await page.locator('[data-login-method="qrcode"]').click();
    await page.locator('#account-submit').click();
    await expectBadges(['有效', '未知']);
    assert.equal((await page.locator('#stat-account-health').textContent()).trim(), '1 有效 · 0 失效 · 1 未知');
    await page.locator('#account-close').click();
    await page.locator('.nav-item[data-page="config"]').click();
    const giftSwitch = page.locator(`input[data-path='["vipMemberGift","enableGiftSecondaries"]']`);
    assert.equal(await giftSwitch.count(), 1, 'auxiliary gift permission missing from WebUI');
    assert.equal(await giftSwitch.isChecked(), false, 'auxiliary gifts must default off');
    await giftSwitch.locator('..').click();
    assert.equal(await giftSwitch.isChecked(), true, 'auxiliary gift permission cannot be edited');
    await page.screenshot({ path: path.join(outputDir, 'cookie-login-and-gift-config.png'), fullPage: true });
    assert.deepEqual(errors, []);
    process.stdout.write(JSON.stringify({ ok: true, cookieImport: 'failed then verified', qrcode: 'verified', accountIsolation: true, reload: true, giftSwitch: true, pageErrors: errors }));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
