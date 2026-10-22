const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const [baseURL, testRoot] = process.argv.slice(2);
if (!baseURL || !testRoot || !['127.0.0.1', 'localhost'].includes(new URL(baseURL).hostname)) throw new Error('Provide an isolated local WebUI URL and output directory');
const output = path.join(testRoot, 'screenshots');
fs.mkdirSync(output, { recursive: true });

(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const errors = [];
    let blockedRuns = 0;
    let recoveryChecks = 0;
    let policy = { required: true, currentVersion: '1.2.9-grails.1', requiredVersion: '1.2.9-grails.2', graceUntil: '2026-10-01T00:00:00Z' };
    page.on('pageerror', error => errors.push(error.message));
    await page.addInitScript(() => {
      window.policyCSPViolations = [];
      document.addEventListener('securitypolicyviolation', event => window.policyCSPViolations.push({ directive: event.violatedDirective, source: event.sourceFile, blocked: event.blockedURI }));
    });
    await page.route('**/api/v1/auth/status', async route => {
      const response = await route.fetch();
      const auth = await response.json();
      await route.fulfill({ response, json: { ...auth, version: '1.2.9-grails.1', upgradePolicy: policy } });
    });
    await page.route('**/api/v1/music-partner/points**', route => route.fulfill({ status: 503, json: { error: '隔离测试无账号' } }));
    await page.route('**/api/v1/play-stats', route => route.fulfill({ json: [] }));
    const update = { current_version: '1.2.9-grails.1', latest_version: '1.2.9-grails.2', available: true, canApply: true };
    await page.route('**/api/v1/update', route => route.fulfill({ json: update }));
    await page.route('**/api/v1/update/check', route => { recoveryChecks++; return route.fulfill({ json: update }); });
    await page.route('**/api/v1/update/apply', route => { throw new Error('Test must not apply updates'); });
    await page.route('**/api/v1/system/restart', route => { throw new Error('Test must not restart'); });
    await page.route(/\/api\/v1\/(schedules|plugins)\/[^/]+\/run$/, route => {
      blockedRuns++;
      policy = { ...policy, required: true };
      return route.fulfill({ status: 426, json: { error: '当前版本需要升级后继续执行任务', upgradePolicy: policy } });
    });
    await page.goto(baseURL, { waitUntil: 'networkidle' });
    assert.equal(await page.locator('#auth-title').textContent(), '设置管理员密码', 'use a fresh isolated home');
    await page.locator('#login-view [data-upgrade-policy]').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#auth-version').textContent(), 'v1.2.9-grails.1');
    await page.screenshot({ path: path.join(output, 'upgrade-login.png'), fullPage: true });
    await page.locator('#password-input').fill('PolicyTest#2026');
    await page.locator('#setup-confirm-password').fill('PolicyTest#2026');
    await page.locator('#setup-submit').click();
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    await page.locator('#upgrade-policy-system').click();
    await page.locator('#page-system.active').waitFor();
    await page.locator('#update-check').click();
    await page.waitForFunction(() => !document.querySelector('#update-check').disabled);
    assert.equal(recoveryChecks, 1, 'recovery update check remains available');
    assert(await page.locator('#update-apply').isEnabled());
    assert(await page.locator('#system-restart').isEnabled());
    await page.screenshot({ path: path.join(output, 'upgrade-required.png'), fullPage: true });
    for (const width of [320, 390, 768, 1440]) {
      await page.setViewportSize({ width, height: 1000 });
      const bounds = await page.locator('#upgrade-policy-banner').boundingBox();
      assert(bounds && bounds.x >= 0 && bounds.x + bounds.width <= width + 1, `upgrade banner clipped at ${width}`);
      const action = await page.locator('#upgrade-policy-system').boundingBox();
      assert(action && action.x >= 0 && action.x + action.width <= width + 1, `recovery action clipped at ${width}`);
      if (width === 320) await page.screenshot({ path: path.join(output, 'upgrade-required-mobile.png'), fullPage: true });
    }
    policy = { ...policy, required: false };
    await page.locator('#upgrade-policy-banner [data-upgrade-title]').filter({ hasText: '提醒' }).waitFor({ timeout: 15000 });
    const job = await page.evaluate(async () => {
      const auth = await (await fetch('/api/v1/auth/status')).json();
      const response = await fetch('/api/v1/schedules', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-NCMM-CSRF': auth.csrfToken }, body: JSON.stringify({ name: '升级阻断隔离评分', command: 'music-partner', args: [], cron: '0 3 * * *', enabled: false, overlapPolicy: 'skip' }) });
      if (!response.ok) throw new Error(await response.text());
      return response.json();
    });
    await page.locator('.nav-item[data-page="schedules"]').click();
    const run = page.locator(`[data-action="run"][data-id="${job.id}"]`);
    await run.waitFor({ state: 'visible', timeout: 15000 });
    await run.click();
    await page.locator('.toast.error').filter({ hasText: '需要升级' }).waitFor();
    await page.locator('#upgrade-policy-banner [data-upgrade-title]').filter({ hasText: '需要升级' }).waitFor();
    assert.equal(blockedRuns, 1);
    assert(await page.locator('#app-shell').isVisible(), '426 must preserve authenticated session');
    const runs = await page.evaluate(async () => (await fetch('/api/v1/runs')).json());
    assert.equal(runs.length, 0, 'browser must not run actual business tasks');
    policy = { required: false, currentVersion: '1.2.9-grails.1' };
    await page.locator('#upgrade-policy-banner').waitFor({ state: 'hidden', timeout: 15000 });
    await page.locator('#logout-button').click();
    await page.locator('#login-view').waitFor({ state: 'visible' });
    await page.locator('#password-input').fill('PolicyTest#2026');
    await page.locator('#login-submit').click();
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    assert(await page.locator('#upgrade-policy-banner').isHidden());
    // 既有页面仍有内联样式；新增提示必须全部来自允许的外部资源。
    const violations = await page.evaluate(() => window.policyCSPViolations.filter(item => item.source.includes('upgrade-policy') || item.blocked.includes('upgrade-policy') || item.directive !== 'style-src-attr'));
    assert.deepEqual(violations, []);
    assert.equal(await page.locator('[data-upgrade-policy] [style]').count(), 0);
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ result: 'passed', blockedRunsMocked: blockedRuns, recoveryChecksMocked: recoveryChecks, screenshots: output }));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
