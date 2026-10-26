const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const [baseURL, testRoot] = process.argv.slice(2);
if (!baseURL || !testRoot || !['127.0.0.1', 'localhost'].includes(new URL(baseURL).hostname)) {
  throw new Error('Provide a local, isolated WebUI URL and test output directory');
}
const outputDir = path.join(testRoot, 'screenshots');
fs.mkdirSync(outputDir, { recursive: true });
const main = './main/cookie.json', secondary = './other/cookie.json', unavailable = './unknown/cookie.json';
const names = { [main]: '主账号余额测试', [secondary]: '辅助零余额测试', [unavailable]: '余额未知测试' };

async function expectYunbei(page, label, expected) {
  await page.waitForFunction(({ label, pattern }) => {
    const row = [...document.querySelectorAll('#accounts-table tr')].find(row => row.textContent.includes(label));
    return row && new RegExp(pattern).test(row.cells[4]?.textContent.trim() || '');
  }, { label, pattern: expected.source }, { timeout: 15000 });
  const cell = page.locator('#accounts-table tr').filter({ hasText: label }).locator('td').nth(4);
  assert.match((await cell.innerText()).trim(), expected, `${label} must use its current balance while preserving today's income`);
}

async function expectGrowth(page, label, expected) {
  await page.waitForFunction(({ label, pattern }) => {
    const row = [...document.querySelectorAll('#accounts-table tr')].find(row => row.textContent.includes(label));
    return row && new RegExp(pattern).test(row.cells[5]?.textContent.trim() || '');
  }, { label, pattern: expected.source }, { timeout: 15000 });
  const cell = page.locator('#accounts-table tr').filter({ hasText: label }).locator('td').nth(5);
  assert.match((await cell.innerText()).trim(), expected, `${label} must separate current growth total from today's income`);
}

(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  let releaseOld;
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const errors = [], writes = [], requests = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/api/v1/schedules/*/run', route => { writes.push(route.request().url()); return route.abort(); });
    await page.route('**/api/v1/play-stats', route => route.fulfill({ json: [] }));
    let revision = 'balance-fixture-before', phase = 'initial', accountPage = false, active = 0, peak = 0, signalOld;
    const now = new Date().toISOString();
    const profiles = Object.fromEntries(Object.entries(names).map(([account, nickname]) => [account, { nickname }]));
    await page.route('**/api/v1/config', async route => {
      assert.equal(route.request().method(), 'GET', 'balance checks must not write configuration');
      const response = await route.fetch();
      const config = await response.json();
      config.data.accounts = { main, secondary: [secondary, unavailable] };
      config.accountProfiles = profiles;
      config.revision = revision;
      return route.fulfill({ response, json: config });
    });
    await page.route('**/api/v1/runs', route => route.fulfill({ json: [{
      id: 'historic-balances', command: 'sign', jobName: '历史收益样例', status: 'success', startedAt: now, triggeredAt: now, finishedAt: now,
      rewards: [
        { account: main, yunbeiKnown: true, yunbei: 50, yunbeiBalanceKnown: true, yunbeiBalance: 0 },
        { account: secondary, yunbeiKnown: true, yunbei: 7, yunbeiBalanceKnown: true, yunbeiBalance: 999, growthKnown: true, growthToday: 6, growthTotal: 7777 },
        { account: unavailable, yunbeiKnown: true, yunbei: 9, yunbeiBalanceKnown: true, yunbeiBalance: 8888, growthKnown: true, growthToday: 2, growthTotal: 99999 },
      ],
    }] }));
    await page.route('**/api/v1/accounts/data**', async route => {
      assert.equal(route.request().method(), 'GET', 'balance requests must remain read-only');
      const account = new URL(route.request().url()).searchParams.get('account');
      assert(Object.hasOwn(names, account), 'balance requests must preserve the complete configured account path');
      const currentPhase = phase;
      requests.push({ account, phase: currentPhase });
      const counted = accountPage && currentPhase === 'initial';
      if (counted) { active++; peak = Math.max(peak, active); }
      try {
        const result = { account, partner: null, growthTotal: account === main ? 8224 : account === secondary ? 0 : null, vipLevel: null, yunbeiBalance: account === main ? 9876 : account === secondary ? 0 : null, updatedAt: now, errors: account === unavailable ? { yunbei: '模拟余额查询失败', growth: '模拟成长总值查询失败' } : { growth: '模拟等级缺失' } };
        if (counted) await new Promise(resolve => setTimeout(resolve, 30));
        if (currentPhase === 'missing' && account === main) {
          result.yunbeiBalance = null;
          result.growthTotal = null;
        }
        if (currentPhase === 'failed' && account === main) {
          await route.fulfill({ status: 503, json: { error: '模拟临时失败' } });
          return;
        }
        if (currentPhase === 'old-request' && account === main) {
          result.yunbeiBalance = 33333;
          result.growthTotal = 11111;
          signalOld?.();
          await new Promise(resolve => { releaseOld = resolve; });
        } else if (currentPhase === 'new-credentials' && account === main) {
          result.yunbeiBalance = 44444;
          result.growthTotal = 22222;
        }
        await route.fulfill({ json: result }).catch(() => {});
      } finally {
        if (counted) active--;
      }
    });
    let cookieImports = 0;
    await page.route('**/api/v1/accounts/cookie', route => {
      assert.equal(route.request().method(), 'POST');
      cookieImports++;
      phase = 'new-credentials';
      revision = 'balance-fixture-after';
      profiles[main].cookieVerifiedAt = new Date(Date.now() + 1000).toISOString();
      return route.fulfill({ json: { filename: 'cookie.json', main: true, message: '模拟账号凭据已更新' } });
    });

    await page.goto(baseURL, { waitUntil: 'networkidle' });
    await page.locator('#auth-title').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#auth-title').innerText(), '设置管理员密码', 'use a fresh isolated home');
    await page.locator('#password-input').fill('AccountBalance#2026');
    await page.locator('#setup-confirm-password').fill('AccountBalance#2026');
    await page.locator('#setup-submit').click();
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    await page.waitForLoadState('networkidle');
    accountPage = true;
    await page.locator('.nav-item[data-page="accounts"]').click();
    await expectYunbei(page, names[main], /^\+50\s*\/\s*9876$/);
    await expectYunbei(page, names[secondary], /^\+7\s*\/\s*0$/);
    await expectYunbei(page, names[unavailable], /^\+9\s*\/\s*(?:未知|--)$/);
    await expectGrowth(page, names[main], /^--\s*\/\s*8224$/);
    await expectGrowth(page, names[secondary], /^6\s*\/\s*0$/);
    await expectGrowth(page, names[unavailable], /^2\s*\/\s*未知$/);
    assert(peak > 0 && peak <= 2, `account balance requests must use at most two concurrent readers, saw ${peak}`);
    for (const account of Object.keys(names)) assert(requests.some(request => request.account === account), `account not queried: ${account}`);
    await page.screenshot({ path: path.join(outputDir, 'account-current-balances.png'), fullPage: true });

    phase = 'missing';
    await page.locator('#refresh-button').click();
    await expectYunbei(page, names[main], /^\+50\s*\/\s*(?:未知|--)$/);
    await expectGrowth(page, names[main], /^--\s*\/\s*未知$/);
    await page.waitForFunction(() => !document.querySelector('#refresh-button').disabled);
    phase = 'failed';
    await page.locator('#refresh-button').click();
    await page.waitForFunction(() => !document.querySelector('#refresh-button').disabled);
    await expectYunbei(page, names[main], /^\+50\s*\/\s*(?:未知|--)$/);
    await expectGrowth(page, names[main], /^--\s*\/\s*未知$/);
    assert(requests.some(request => request.account === main && request.phase === 'failed'), 'explicit refresh must not reuse cached balance');

    phase = 'old-request';
    const oldStarted = new Promise(resolve => { signalOld = resolve; });
    await page.locator('#refresh-button').click();
    await oldStarted;
    await page.locator('#accounts-table tr').filter({ hasText: names[main] }).locator('[data-account-action="edit"]').click();
    await page.locator('[data-login-method="cookie"]').click();
    await page.locator('#cookie-content').fill('MUSIC_U=isolated-balance-fixture');
    await page.locator('#account-submit').click();
    await expectYunbei(page, names[main], /^\+50\s*\/\s*44444$/);
    await expectGrowth(page, names[main], /^--\s*\/\s*22222$/);
    releaseOld();
    releaseOld = undefined;
    await page.waitForLoadState('networkidle');
    await expectYunbei(page, names[main], /^\+50\s*\/\s*44444$/);
    await expectYunbei(page, names[secondary], /^\+7\s*\/\s*0$/);
    await expectGrowth(page, names[main], /^--\s*\/\s*22222$/);
    await expectGrowth(page, names[secondary], /^6\s*\/\s*0$/);
    assert.equal(cookieImports, 1, 'only one mocked credential change is expected');
    assert.deepEqual(errors, [], 'browser errors');
    assert.deepEqual(writes, [], 'balance checks must not run tasks');
    console.log('Account balances and growth totals: current nonzero/zero, no task prerequisite, unknown without history fallback, full-path isolation, bounded concurrency and stale credential response rejection passed');
  } finally {
    releaseOld?.();
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
