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
    const dialogs = [];
    let dismissNext = false;
    let update = { current_version: '1.2.2-grails.1', latest_version: '1.2.2-grails.2', available: true, canApply: true, last_check_time: new Date().toISOString(), os: 'linux', arch: 'amd64', release_notes: '隔离测试更新说明' };
    let applyFails = false;
    let checkFails = false;
    let busy = false;
    let restartCount = 0;
    let probeCount = 0;
    let navigations = 0;
    page.on('pageerror', error => errors.push(error.message));
    page.on('framenavigated', frame => { if (frame === page.mainFrame()) navigations++; });
    page.on('dialog', async dialog => { dialogs.push(dialog.message()); if (dismissNext) { dismissNext = false; await dialog.dismiss(); } else await dialog.accept(); });
    await page.route('**/api/v1/music-partner/points**', route => route.fulfill({ status: 503, json: { error: '隔离测试无账号' } }));
    await page.route('**/api/v1/play-stats', route => route.fulfill({ json: [] }));
    await page.route('**/api/v1/update', route => route.fulfill({ json: update }));
    await page.route('**/api/v1/update/check', route => route.fulfill(checkFails ? { status: 502, json: { error: '模拟检查失败' } } : { json: update }));
    // 更新与重启全部在浏览器边界模拟，绝不请求真实 apply/restart。
    await page.route('**/api/v1/update/apply', route => {
      if (applyFails) return route.fulfill({ status: 502, json: { error: '模拟下载失败' } });
      update = { ...update, canApply: false, restartRequired: true, message: '更新文件已安装，请重启 ncmm 后生效。' };
      return route.fulfill({ json: update });
    });
    await page.route('**/api/v1/system/restart', route => {
      restartCount++;
      const body = route.request().postDataJSON();
      if (busy && !body.force) return route.fulfill({ status: 409, json: { error: 'busy', confirmationRequired: true, running: 2, queued: 3 } });
      probeCount = 0;
      return route.fulfill({ status: 202, json: { restarting: true, instanceId: 'old-instance' } });
    });
    await page.route('**/api/v1/instance', route => {
      probeCount++;
      return route.fulfill({ json: { instanceId: probeCount < 3 ? 'old-instance' : 'new-instance', version: update.latest_version } });
    });
    await page.goto(baseURL, { waitUntil: 'networkidle' });
    await page.locator('#auth-title').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#auth-title').textContent(), '设置管理员密码', 'use a fresh isolated home');
    await page.locator('#password-input').fill('UpdateTest#2026');
    await page.locator('#setup-confirm-password').fill('UpdateTest#2026');
    await page.locator('#setup-submit').click();
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    await page.locator('.nav-item[data-page="system"]').click();
    const action = page.locator('#update-apply');
    await action.waitFor({ state: 'visible' });
    assert.equal(await action.textContent(), '立即更新');
    await page.screenshot({ path: path.join(output, 'update-available.png'), fullPage: true });
    applyFails = true;
    await action.click();
    await page.locator('.toast.error').filter({ hasText: '模拟下载失败' }).waitFor();
    assert.equal(restartCount, 0);
    await page.locator('.toast.error').filter({ hasText: '模拟下载失败' }).waitFor({ state: 'hidden' });
    applyFails = false;
    dismissNext = true;
    await action.click();
    await page.locator('#update-status-badge').filter({ hasText: '待重启' }).waitFor();
    assert.equal(await action.textContent(), '立即重启生效');
    assert.equal(restartCount, 0);
    assert(dialogs.some(message => message.includes('更新已安装')));
    await page.waitForTimeout(250); // 等待既有按钮颜色过渡，截图反映稳定状态。
    await page.screenshot({ path: path.join(output, 'update-ready.png'), fullPage: true });
    for (const width of [320, 768, 1024, 1440]) {
      await page.setViewportSize({ width, height: 1000 });
      await action.scrollIntoViewIfNeeded();
      const box = await action.boundingBox();
      assert(box && box.x >= 0 && box.x + box.width <= width + 1, `update action clipped at ${width}`);
      assert(await action.isEnabled());
      if (width === 320) await page.screenshot({ path: path.join(output, 'update-ready-mobile.png'), fullPage: true });
    }
    busy = true;
    const before = navigations;
    await action.click();
    await page.waitForFunction(() => document.querySelector('#update-apply').textContent.includes('等待服务恢复'));
    assert.equal(navigations, before, 'old instance response must not reload page');
    await page.waitForEvent('framenavigated', { predicate: frame => frame === page.mainFrame() });
    assert.equal(restartCount, 2);
    assert(dialogs.some(message => /2.*3/.test(message)));
    assert(probeCount >= 3, 'must wait for new instance');
    await page.locator('#update-apply').waitFor({ state: 'visible' });
    update = { ...update, restartRequired: false, canApply: false, docker: true, message: '普通容器请重建镜像' };
    await page.locator('#update-check').click();
    await action.waitFor({ state: 'hidden' });
    checkFails = true;
    await page.locator('#update-check').click();
    await page.locator('#update-message').filter({ hasText: '模拟检查失败' }).waitFor();
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ result: 'passed', restartRequestsMocked: restartCount, probes: probeCount, screenshots: output }));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
