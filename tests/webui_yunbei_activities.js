const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const [baseURL, testRoot] = process.argv.slice(2);
assert.ok(baseURL && testRoot && ['127.0.0.1', 'localhost'].includes(new URL(baseURL).hostname), 'use an isolated local WebUI');
const fields = ['enableRedPacket', 'enableLottery', 'enableLotteryWatchVideo', 'enableWatchVideoYunbei'];
const keys = ['RedPacket', 'Lottery', 'LotteryWatchVideo', 'WatchVideoYunbei'];
const output = path.join(testRoot, 'screenshots');
fs.mkdirSync(output, { recursive: true });

(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const errors = [];
  const runs = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.route('**/api/v1/schedules/*/run', route => { runs.push(route.request().url()); return route.abort(); });
  try {
    await page.goto(baseURL, { waitUntil: 'networkidle' });
    await page.locator('#auth-title').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#auth-title').textContent(), '设置管理员密码');
    await page.locator('#password-input').fill('ActivityTest#2026');
    await page.locator('#setup-confirm-password').fill('ActivityTest#2026');
    await page.locator('#setup-submit').click();
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    const initial = await (await page.request.get(`${baseURL}/api/v1/config`)).json();
    const partner = JSON.stringify(initial.data.musicPartner);
    await page.locator('.nav-item[data-page="config"]').click();
    await page.locator('#config-sections [data-section="sign"]').click();
    const input = key => page.locator(`[data-path='${JSON.stringify(['sign', 'yunbeiTask', key])}']`);
    for (const field of fields) {
      await input(field).waitFor({ state: 'visible' });
      assert.equal(await input(field).isChecked(), false, `${field} must default off`);
      await input(field).check();
    }
    const saved = page.waitForResponse(r => r.url().endsWith('/api/v1/config') && r.request().method() === 'PUT');
    await page.locator('#config-save').click();
    assert.equal((await saved).status(), 200);
    await page.waitForFunction(() => !document.querySelector('#config-save').disabled);
    await page.reload({ waitUntil: 'networkidle' });
    await page.locator('#config-sections [data-section="sign"]').click();
    for (const field of fields) assert.equal(await input(field).isChecked(), true, `${field} was not saved`);
    const after = await (await page.request.get(`${baseURL}/api/v1/config`)).json();
    assert.equal(JSON.stringify(after.data.musicPartner), partner, 'cloud task configuration changed music partner configuration');
    // 保存保留原始 YAML 队列，运行和看板按已开启的活动补齐有效队列。
    await page.locator('#config-sections [data-section="task"]').click();
    const queueText = await page.locator('#task-queue-wrapper').innerText();
    for (const name of ['时段红包雨', '云贝活动抽奖', '广告增加抽奖次数', '看视频赚云贝']) assert.ok(queueText.includes(name), `${name} missing from effective queues`);
    await page.locator('#config-sections [data-section="sign"]').click();
    await input(fields[3]).scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(output, 'yunbei-activities-desktop.png'), fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: path.join(output, 'yunbei-activities-mobile.png'), fullPage: true });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), 'mobile overflow');
    assert.deepEqual(errors, []);
    assert.deepEqual(runs, [], 'no account task may run in this UI test');
    fs.writeFileSync(path.join(testRoot, 'browser-report.json'), JSON.stringify({ fields, queueKeys: keys, musicPartnerPreserved: true, pageErrors: errors, realAccountTasks: false }, null, 2));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error.message); process.exitCode = 1; });
