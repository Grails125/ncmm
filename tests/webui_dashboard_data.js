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
const main = './main/cookie.json', secondary = './other/cookie.json';
const snapshot = account => ({
  account, partner: { account, period: 73, week: 2, integral: account === main ? 127 : 12, currentWeekIntegral: account === main ? 70 : 2, startDate: 1791734400000, endDate: 1794153599999 },
  growthTotal: account === main ? 8209 : 100, vipLevel: account === main ? 6 : 1, yunbeiBalance: account === main ? 9876 : 0,
  updatedAt: new Date().toISOString(), errors: {},
});

async function assertAccountDataLayout(page, desktop) {
  const items = await page.locator('.music-partner-points-values > div').evaluateAll(elements => elements.map(element => {
    const bounds = element.getBoundingClientRect();
    return { id: element.querySelector('dd')?.id, top: bounds.top, left: bounds.left, right: bounds.right };
  }));
  assert.deepEqual(items.map(item => item.id), ['account-data-yunbei-balance', 'account-data-vip-level', 'account-data-growth-total', 'music-partner-points-integral', 'music-partner-points-weekly'], 'account metrics must follow the requested order');
  if (desktop) {
    assert(items.every(item => Math.abs(item.top - items[0].top) < 2), 'desktop account metrics must share one row');
    assert(items.every((item, index) => index === 0 || item.left >= items[index - 1].right), 'desktop account metrics must remain in visual order');
  } else {
    assert(items.some(item => item.top > items[0].top + 2), 'narrow account metrics must wrap into multiple rows');
  }
}

async function assertTelemetryAligned(page) {
  const pills = await page.locator('.telemetry-pill').evaluateAll(elements => elements.map(element => {
    const [label, value] = element.querySelectorAll('span');
    const first = label.getBoundingClientRect(), second = value.getBoundingClientRect();
    const labelStyle = getComputedStyle(label), valueStyle = getComputedStyle(value);
    return { label: label.textContent, value: value.textContent, labelCenter: first.top + first.height / 2, valueCenter: second.top + second.height / 2, labelRight: first.right, valueLeft: second.left, valueHeight: second.height, labelFont: labelStyle.fontFamily, valueFont: valueStyle.fontFamily, labelLineHeight: labelStyle.lineHeight, valueLineHeight: valueStyle.lineHeight, lineHeight: Number.parseFloat(valueStyle.lineHeight) };
  }));
  assert.equal(pills.length, 2, 'runtime and next-task telemetry must both be present');
  for (const pill of pills) {
    assert(Math.abs(pill.labelCenter - pill.valueCenter) <= 2, `telemetry label/value are not vertically aligned: ${JSON.stringify(pill)}`);
    assert(pill.valueLeft >= pill.labelRight, `telemetry label/value must stay on the same row: ${JSON.stringify(pill)}`);
    assert.equal(pill.labelFont, pill.valueFont, 'telemetry label/value must use the same font family');
    assert.equal(pill.labelLineHeight, pill.valueLineHeight, 'telemetry label/value must share an explicit line height');
    assert(Number.isFinite(pill.lineHeight) && pill.lineHeight > 0, 'telemetry line height must be explicit');
    assert(pill.valueHeight <= pill.lineHeight + 1, `telemetry value wrapped: ${JSON.stringify(pill)}`);
  }
}

(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const errors = [], writes = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/api/v1/schedules/*/run', route => { writes.push(route.request().url()); return route.abort(); });
    await page.route('**/api/v1/play-stats', route => route.fulfill({ json: [] }));
    await page.route('**/api/v1/schedules', route => {
      assert.equal(route.request().method(), 'GET', 'dashboard must not create scheduled tasks');
      return route.fulfill({ json: [{ id: 'display-only', name: '展示下次运行时间', command: 'music-partner', args: [], cron: '0 0 1 1 *', enabled: true, nextRuns: [new Date(Date.now() + 3600000).toISOString()] }] });
    });
    await page.route('**/api/v1/config', async route => {
      assert.equal(route.request().method(), 'GET', 'dashboard must not write configuration');
      const response = await route.fetch();
      const config = await response.json();
      config.data.accounts = { main, secondary: [secondary] };
      config.accountProfiles = { [main]: { nickname: '主账号测试' }, [secondary]: { nickname: '辅助测试' } };
      return route.fulfill({ response, json: config });
    });
    const now = new Date().toISOString();
    const rewards = [
      { account: main, yunbeiKnown: true, yunbei: 50, yunbeiBalanceKnown: true, yunbeiBalance: 9876, growthKnown: true, growthToday: 6, growthTotal: 8209, musicPartnerKnown: true, musicPartnerDelta: 8 },
      { account: secondary, yunbeiKnown: true, yunbei: 0, musicPartnerKnown: true, musicPartnerDelta: 0 },
    ];
    await page.route('**/api/v1/runs', route => route.fulfill({ json: [
      { id: 'latest', jobName: '音乐合伙人评分', command: 'music-partner', status: 'success', startedAt: now, triggeredAt: now, finishedAt: now, rewards },
      { id: 'earlier', jobName: '音乐合伙人评分', command: 'music-partner', status: 'success', startedAt: now, triggeredAt: now, finishedAt: now, rewards: [{ account: main, musicPartnerKnown: true, musicPartnerDelta: -2 }] },
    ] }));
    let mode = 'success', releaseOld, oldStarted;
    await page.route('**/api/v1/accounts/data**', async route => {
      const account = new URL(route.request().url()).searchParams.get('account');
      let result = snapshot(account);
      if (mode === 'delayed' && account === main) {
        oldStarted?.();
        await new Promise(resolve => { releaseOld = resolve; });
      } else if (mode === 'partial' && account === main) {
        result = { account, partner: null, growthTotal: null, vipLevel: null, yunbeiBalance: 0, updatedAt: now, errors: { partner: '暂无合伙人资格', growth: '成长值暂不可用' } };
      }
      await route.fulfill({ json: result }).catch(() => {});
    });
    await page.goto(baseURL, { waitUntil: 'networkidle' });
    await page.locator('#auth-title').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#auth-title').textContent(), '设置管理员密码', 'use a fresh isolated home');
    await page.locator('#password-input').fill('DashboardTest#2026');
    await page.locator('#setup-confirm-password').fill('DashboardTest#2026');
    await page.locator('#setup-submit').click();
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    await page.waitForFunction(() => document.querySelector('#music-partner-points-status').textContent === '已更新');
    assert.match(await page.locator('#music-partner-points-title').innerText(), /账号数据/);
    for (const [selector, expected] of [['#music-partner-points-integral', '127'], ['#music-partner-points-weekly', '70'], ['#account-data-growth-total', '8209'], ['#account-data-vip-level', 'Lv.6'], ['#account-data-yunbei-balance', '9876']]) {
      assert.equal(await page.locator(selector).innerText(), expected);
    }
    await assertAccountDataLayout(page, true);
    await assertTelemetryAligned(page);
    assert.notEqual(await page.locator('#dashboard-next-run').innerText(), '暂无计划');
    const daily = await page.locator('#account-rewards-list').innerText();
    assert.match(daily, /云贝 \+50/);
    assert.match(daily, /成长值 \+6/);
    assert.match(daily, /合伙人积分 \+6/);
    assert.doesNotMatch(daily, /9876|8209|辅助测试|--/);
    await page.locator('#dashboard-metric [data-metric="musicPartner"]').click();
    assert.match(await page.locator('#dashboard-metric-title').innerText(), /音乐合伙人积分变化/);
    assert.match(await page.locator('#dashboard-chart').innerHTML(), /：6/);
    await page.screenshot({ path: path.join(outputDir, 'dashboard-data-desktop.png'), fullPage: true });

    mode = 'partial';
    await page.locator('#music-partner-points-refresh').click();
    await page.waitForFunction(() => document.querySelector('#music-partner-points-status').textContent === '部分查询失败');
    assert.equal(await page.locator('#music-partner-points-integral').innerText(), '未知');
    assert.equal(await page.locator('#account-data-growth-total').innerText(), '未知');
    assert.equal(await page.locator('#account-data-vip-level').innerText(), '未知');
    assert.equal(await page.locator('#account-data-yunbei-balance').innerText(), '0');
    assert.match(await page.locator('#music-partner-points-message').innerText(), /暂无合伙人资格/);

    mode = 'delayed';
    const started = new Promise(resolve => { oldStarted = resolve; });
    await page.locator('#music-partner-points-refresh').click();
    await started;
    await page.locator('#music-partner-points-account').selectOption(secondary);
    await page.waitForFunction(() => document.querySelector('#music-partner-points-integral').textContent === '12');
    releaseOld();
    await page.waitForLoadState('networkidle');
    assert.equal(await page.locator('#music-partner-points-integral').innerText(), '12');
    assert.equal(await page.locator('#account-data-growth-total').innerText(), '100');
    assert.equal(await page.locator('#account-data-yunbei-balance').innerText(), '0');
    for (const width of [375, 320]) {
      await page.setViewportSize({ width, height: 900 });
      const dimensions = await page.evaluate(() => ({ width: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
      assert(dimensions.scroll <= dimensions.width, `dashboard overflow at ${width}: ${JSON.stringify(dimensions)}`);
      const cardDimensions = await page.locator('.music-partner-points-panel').evaluate(element => ({ width: element.clientWidth, scroll: element.scrollWidth }));
      assert(cardDimensions.scroll <= cardDimensions.width, `account card overflow at ${width}: ${JSON.stringify(cardDimensions)}`);
      const chartDimensions = await page.locator('.dashboard-chart-panel').evaluate(element => ({ right: element.getBoundingClientRect().right, scroll: element.scrollWidth, width: element.clientWidth }));
      assert(chartDimensions.right <= width && chartDimensions.scroll <= chartDimensions.width, `chart card overflow at ${width}: ${JSON.stringify(chartDimensions)}`);
      await assertAccountDataLayout(page, false);
      await assertTelemetryAligned(page);
      await page.locator('.music-partner-points-panel').screenshot({ path: path.join(outputDir, `dashboard-data-${width}.png`) });
      await page.locator('.dashboard-chart-panel').screenshot({ path: path.join(outputDir, `dashboard-chart-${width}.png`) });
    }
    assert.deepEqual(errors, [], 'browser errors');
    assert.deepEqual(writes, [], 'dashboard must not run tasks');
    console.log('Dashboard metric order, desktop five-column/narrow wrapping, telemetry alignment, partial errors, account switching, rewards and 7-day metric passed');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
