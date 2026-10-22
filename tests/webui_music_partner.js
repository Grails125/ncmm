const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const [baseURL, testRoot] = process.argv.slice(2);
if (!baseURL || !testRoot || !['127.0.0.1', 'localhost'].includes(new URL(baseURL).hostname)) {
  throw new Error('Provide a local, isolated WebUI URL and test output directory');
}
const outputDir = path.join(testRoot, 'screenshots');
fs.mkdirSync(outputDir, { recursive: true });
const assert = (condition, message) => { if (!condition) throw new Error(message); };
const field = (page, name) => page.locator(`input[data-path='["musicPartner","${name}"]']`);
const taskSwitch = page => page.locator('[data-task-card="task.music-partner"] input');

async function saveConfig(page, expectedStatus = 200) {
  const response = page.waitForResponse(result => result.url().endsWith('/api/v1/config') && result.request().method() === 'PUT');
  await page.locator('#config-save').click();
  const result = await response;
  assert(result.status() === expectedStatus, `configuration save returned ${result.status()}: ${await result.text()}`);
}

async function noOverflow(page) {
  const size = await page.evaluate(() => ({ width: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
  assert(size.scroll <= size.width, `page overflow: ${JSON.stringify(size)}`);
}

(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage();
    const errors = [];
    const blockedRuns = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/api/v1/schedules/*/run', route => {
      blockedRuns.push(route.request().url());
      return route.abort();
    });
    const now = new Date().toISOString();
    await page.route('**/api/v1/runs', route => route.request().method() === 'GET'
      ? route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([{ id: 'partner-failure', jobId: 'partner', jobName: '合伙人评分失败样例', command: 'music-partner', source: 'manual', status: 'failed', triggeredAt: now, startedAt: now, finishedAt: now, exitCode: 7, error: '模拟评分命令失败' }]) })
      : route.continue());

    await page.goto(baseURL, { waitUntil: 'networkidle' });
    await page.locator('#auth-title').waitFor({ state: 'visible' });
    assert((await page.locator('#auth-title').textContent()) === '设置管理员密码', 'use a fresh isolated WebUI home');
    await page.locator('#password-input').fill('PartnerTest#2026');
    await page.locator('#setup-confirm-password').fill('PartnerTest#2026');
    await page.locator('#setup-submit').click();
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    await page.locator('.nav-item[data-page="config"]').click();
    await page.locator('#config-block-musicPartner').waitFor({ state: 'visible' });
    await page.locator('[data-task-card="task.music-partner"]').waitFor({ state: 'visible' });
    assert(await page.locator('.card-flow-section').count() === 16, 'native config section missing');
    assert((await page.locator('#badge-count-config').innerText()) === '16', 'configuration module badge was not updated');
    assert(await page.locator('#config-block-task .task-card').count() === 9, 'native batch task missing');
    assert(!await taskSwitch(page).isChecked(), 'partner batch task must be opt-in');
    assert(await field(page, 'enableMain').isChecked() && !await field(page, 'enableSecondaries').isChecked(), 'default account selection changed');
    assert(await field(page, 'extraTasks').isChecked() && !await field(page, 'fullExtraTasks').isChecked(), 'extra task defaults changed');
    assert(!await field(page, 'syncYunCircle').isChecked(), 'YunCircle publication must default off');
    for (const [name, value] of [['extraLimit', '7'], ['waitMin', '15'], ['waitMax', '20']]) {
      assert(await field(page, name).inputValue() === value, `${name} default changed`);
    }
    assert(await page.locator('#config-block-musicPartner .schema-segment').count() === 4, 'four scoring strategies missing');
    assert((await page.locator('#config-block-musicPartner .schema-segment.active').innerText()).includes('3：3–4 分'), 'strategy 3 must be default');
    assert((await page.locator('#config-block-musicPartner').innerText()).includes('不分析音频质量'), 'rule-based scoring explanation missing');

    await page.locator('[data-preset="music_partner"]').click();
    assert(await taskSwitch(page).isChecked(), 'partner preset did not enable native task');
    await page.locator('#config-sections [data-section="musicPartner"]').click();
    await page.locator('#config-block-musicPartner [data-schema-value="4"]').click();
    await field(page, 'extraLimit').fill('9');
    await field(page, 'waitMin').fill('21');
    await field(page, 'waitMax').fill('23');
    await saveConfig(page);
    await page.reload({ waitUntil: 'networkidle' });
    await page.locator('#config-block-musicPartner').waitFor({ state: 'visible' });
    assert(await taskSwitch(page).isChecked(), 'native task switch was not persisted');
    assert((await page.locator('#config-block-musicPartner .schema-segment.active').innerText()).includes('4：固定 4 分'), 'strategy did not survive reload');
    assert(await field(page, 'waitMin').inputValue() === '21' && await field(page, 'waitMax').inputValue() === '23', 'wait range was not persisted');
    await page.locator('#config-sections [data-section="musicPartner"]').click();
    await field(page, 'waitMax').fill('20');
    await saveConfig(page, 400);
    await field(page, 'waitMax').fill('23');
    await saveConfig(page);
    await page.locator('#config-sections [data-section="musicPartner"]').click();
    await page.waitForFunction(() => Math.abs(document.querySelector('#config-block-musicPartner').getBoundingClientRect().top - document.querySelector('#config-visual').getBoundingClientRect().top - 12) < 4);
    await noOverflow(page);
    await page.locator('#config-block-musicPartner').screenshot({ path: path.join(outputDir, 'music-partner-desktop.png') });

    await page.locator('.nav-item[data-page="schedules"]').click();
    await page.locator('#schedule-add').click();
    await page.locator('#schedule-name').fill('原生合伙人评分（禁用验证）');
    await page.locator('#schedule-dialog label').filter({ has: page.locator('#schedule-enabled') }).click();
    assert(!await page.locator('#schedule-enabled').isChecked(), 'test schedule must stay disabled');
    await page.locator('#schedule-command').selectOption('music-partner');
    assert(await page.locator('#schedule-plugin-fields').isHidden(), 'native task opened plugin controls');
    assert(await page.locator('#schedule-music-partner-help').isVisible(), 'native account argument help missing');
    await page.locator('#schedule-args').fill('--config other.yaml');
    const invalidSchedule = page.waitForResponse(result => result.url().endsWith('/api/v1/schedules') && result.request().method() === 'POST');
    await page.locator('#schedule-submit').click();
    assert((await invalidSchedule).status() === 400, 'unsupported native arguments were accepted');
    await page.locator('#schedule-args').fill('--cookie-file sub/partner.json');
    const createdSchedule = page.waitForResponse(result => result.url().endsWith('/api/v1/schedules') && result.request().method() === 'POST');
    await page.locator('#schedule-submit').click();
    assert((await createdSchedule).ok(), 'native schedule could not be saved');
    await page.locator('#schedule-dialog').waitFor({ state: 'hidden' });
    const row = page.locator('#schedule-table tr').filter({ hasText: '原生合伙人评分（禁用验证）' });
    assert((await row.innerText()).includes('music-partner'), 'saved native schedule was not rendered');
    assert((await row.innerText()).includes('已禁用'), 'test schedule was unexpectedly enabled');
    await page.waitForFunction(() => document.querySelectorAll('.toast').length === 0);
    await page.screenshot({ path: path.join(outputDir, 'music-partner-schedule.png'), fullPage: true });

    await page.locator('.nav-item[data-page="runs"]').click();
    assert((await page.locator('#runs-table').innerText()).includes('失败'), 'failed native command did not display failed');
    await page.locator('.nav-item[data-page="config"]').click();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator('#config-sections [data-section="musicPartner"]').click();
    await page.waitForFunction(() => Math.abs(document.querySelector('#config-block-musicPartner').getBoundingClientRect().top - document.querySelector('#config-visual').getBoundingClientRect().top - 12) < 4);
    await noOverflow(page);
    assert(await page.locator('#config-block-musicPartner .schema-segmented').evaluate(element => element.scrollWidth <= element.clientWidth), 'mobile scoring choices were clipped');
    await page.screenshot({ path: path.join(outputDir, 'music-partner-mobile.png'), fullPage: true });
    assert(errors.length === 0, `browser errors: ${errors.join('; ')}`);
    assert(blockedRuns.length === 0, 'test attempted to run a real task');
    console.log(JSON.stringify({ passed: true, checks: ['native batch toggle', 'config defaults', 'four scoring strategies', 'preset', 'save/reload', 'invalid wait range', 'schedule argument validation', 'disabled native schedule', 'failure display', 'desktop/mobile layout'], screenshots: outputDir }));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
