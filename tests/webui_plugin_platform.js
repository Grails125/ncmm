const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const [baseURL, testRoot] = process.argv.slice(2);
if (!baseURL || !testRoot || !['127.0.0.1', 'localhost'].includes(new URL(baseURL).hostname)) throw Error('isolated local server required');
const output = path.join(testRoot, 'screenshots');
fs.mkdirSync(output, { recursive: true });

(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const errors = [], actions = [], installations = [], checks = [];
    let running = false, autoStart = false, args = [];
    let failAction = '', releaseStart, startGate;
    let failInstall = false;
    let installed = true, installedTag = 'v1.0.1', checkedAt = '';
    const plugins = () => [
      ...(installed ? [{ name: 'qqbot', title: 'QQ 机器人', type: 'daemon', version: installedTag, running, autoStart, source: { repository: 'crossgg/ncmm-qqbot', tag: installedTag }, update: { latestTag: 'v1.0.2', checkedAt, updateAvailable: installedTag !== 'v1.0.2' } }] : []),
      { name: 'demo', title: '定时演示', type: 'task', version: '1.0', description: '<img src=x onerror=alert(1)>仅展示文字' },
    ];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/api/v1/music-partner/points**', route => route.fulfill({ status: 503, json: { error: '隔离测试无账号' } }));
    await page.route('**/api/v1/play-stats', route => route.fulfill({ json: [] }));
    await page.route('**/api/v1/plugins', route => route.fulfill({ json: plugins() }));
    await page.route('**/api/v1/plugins/market', route => route.fulfill({ json: [{ name: 'qqbot', title: 'QQ 机器人', type: 'daemon', repository: 'crossgg/ncmm-qqbot', author: 'crossgg', features: ['扫码登录', '<script>'], update: { repository: 'crossgg/ncmm-qqbot', latestTag: 'v1.0.2', checkedAt }, description: '隔离测试的公开市场入口', homepage: 'https://github.com/crossgg/ncmm-qqbot' }] }));
    await page.route('**/api/v1/plugins/updates', route => {
      const request = route.request().postDataJSON(); checks.push(request);
      if (request.force) checkedAt = new Date().toISOString();
      return route.fulfill({ json: [{ repository: 'crossgg/ncmm-qqbot', latestTag: 'v1.0.2', checkedAt }] });
    });
    await page.route('**/api/v1/plugins/qqbot/daemon/log', route => route.fulfill({ json: { output: '<img src=x onerror=alert(1)>\n日志仅作为文字显示' } }));
    await page.route('**/api/v1/plugins/install', route => {
      installations.push(route.request().postDataJSON());
      if (!failInstall) { installed = true; installedTag = 'v1.0.2'; }
      return route.fulfill(failInstall ? { status: 502, json: { error: '模拟安装失败' } } : { json: { success: true, name: 'qqbot' } });
    });
    await page.route(/\/api\/v1\/plugins\/qqbot\/(daemon|start|stop|restart|upgrade)$/, async route => {
      const action = new URL(route.request().url()).pathname.split('/').at(-1);
      const method = route.request().method();
      const body = method === 'GET' ? null : route.request().postDataJSON();
      actions.push({ action, method, body });
      if (action === 'start' && startGate) await startGate;
      if (action === failAction && method !== 'GET') return route.fulfill({ status: 502, json: { error: `模拟${action}失败` } });
      if (action === 'start' || action === 'restart') running = true;
      if (action === 'stop') running = false;
      if (action === 'upgrade') installedTag = 'v1.0.2';
      if (action === 'daemon' && method === 'PUT') ({ autoStart, args } = body);
      return route.fulfill({ json: { name: 'qqbot', running, autoStart, args, success: true } });
    });
    // 所有可能执行插件或安装程序的请求都在浏览器边界替换，不触发真实业务。
    await page.route(/\/api\/v1\/(plugins|schedules)\/[^/]+\/run$/, route => { errors.push('unexpected task execution'); return route.abort(); });
    await page.route('**/api/v1/plugins/upload', route => { errors.push('unexpected plugin upload'); return route.abort(); });
    await page.goto(baseURL, { waitUntil: 'networkidle' });
    assert.equal(await page.locator('#auth-title').textContent(), '设置管理员密码', 'use a fresh isolated home');
    await page.locator('#password-input').fill('Plugin#2026');
    await page.locator('#setup-confirm-password').fill('Plugin#2026');
    await page.locator('#setup-submit').click();
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    await page.locator('.nav-item[data-page="plugins"]').click();
    const daemon = page.locator('.plugin-card[data-name="qqbot"]');
    await daemon.waitFor({ state: 'visible' });
    assert(await page.locator('#plugin-file-input').isHidden(), 'upload input must remain hidden under CSP');
    assert.equal(await daemon.locator('[data-plugin-action="schedule"]').count(), 0, 'daemon is not a scheduled task');
    assert.equal(await daemon.locator('[data-plugin-action="run"]').count(), 0);
    assert.equal(await page.locator('.plugin-card[data-name="demo"] img').count(), 0, 'plugin text must be escaped');
    await page.locator('#plugin-check-updates').click();
    await page.waitForFunction(() => /秒后可检查/.test(document.querySelector('#plugin-check-updates').textContent));
    assert(await page.locator('#plugin-check-updates').isDisabled());
    assert(checks.some(check => check.force === true));
    assert(checks.some(check => check.force === false), 'automatic version check uses cache');
    await page.reload({ waitUntil: 'networkidle' });
    await daemon.waitFor({ state: 'visible' });
    assert(await page.locator('#plugin-check-updates').isDisabled(), 'server cache must restore cooldown after reload');
    await page.locator('#plugin-tab-market').click();
    const market = page.locator('[data-market-name="qqbot"]');
    await market.waitFor({ state: 'visible' });
    assert.match(await market.innerText(), /crossgg\/ncmm-qqbot/);
    assert.match(await market.innerText(), /已安装/);
    assert.match(await market.innerText(), /可更新 v1.0.2/);
    assert.equal(await market.locator('script').count(), 0);
    assert.equal(await market.locator('[data-market-install]').count(), 0);
    await market.locator('[data-market-upgrade]').click();
    assert(await page.locator('#plugin-upgrade-dialog').isVisible());
    await page.locator('[data-close-dialog="plugin-upgrade-dialog"]').click();
    await page.screenshot({ path: path.join(output, 'plugin-market-desktop.png'), fullPage: true, animations: 'disabled' });
    await market.locator('[data-market-manage]').click();
    assert.equal(await page.locator('#plugin-tab-installed').getAttribute('aria-selected'), 'true');
    assert.equal(await page.locator('#plugin-search').inputValue(), 'qqbot');
    await page.locator('#plugin-search').fill('');
    await page.locator('#plugin-tab-installed').press('ArrowRight');
    assert.equal(await page.locator('#plugin-tab-market').getAttribute('aria-selected'), 'true');
    await page.locator('#plugin-tab-market').press('ArrowLeft');
    assert.equal(await page.locator('#plugin-tab-installed').getAttribute('aria-selected'), 'true');
    failAction = 'start';
    await daemon.locator('[data-plugin-action="start"]').click();
    await page.locator('.toast.error').filter({ hasText: '模拟start失败' }).waitFor();
    assert(await daemon.locator('[data-plugin-action="start"]').isEnabled());
    failAction = '';
    const beforeStart = actions.filter(action => action.action === 'start').length;
    startGate = new Promise(resolve => { releaseStart = resolve; });
    await daemon.locator('[data-plugin-action="start"]').click();
    assert(await daemon.locator('[data-plugin-action="start"]').isDisabled());
    await daemon.locator('[data-plugin-action="restart"]').dispatchEvent('click');
    await page.waitForTimeout(100);
    assert.equal(actions.filter(action => action.action === 'start').length, beforeStart + 1);
    assert.equal(actions.filter(action => action.action === 'restart').length, 0, 'same-plugin actions cannot overlap');
    releaseStart(); startGate = null;
    await daemon.locator('[data-plugin-action="stop"]').waitFor({ state: 'visible' });
    await daemon.locator('[data-plugin-action="settings"]').click();
    await page.locator('#plugin-daemon-autostart').check();
    await page.locator('#plugin-daemon-args').fill('--port\n5678');
    failAction = 'daemon';
    await page.locator('#plugin-daemon-save').click();
    await page.locator('.toast.error').filter({ hasText: '模拟daemon失败' }).waitFor();
    assert(await page.locator('#plugin-daemon-dialog').isVisible());
    assert(await page.locator('#plugin-daemon-save').isEnabled());
    assert.equal(await page.locator('#plugin-daemon-args').inputValue(), '--port\n5678');
    failAction = '';
    await page.locator('#plugin-daemon-save').click();
    await page.locator('#plugin-daemon-dialog').waitFor({ state: 'hidden' });
    assert.equal(autoStart, true); assert.deepEqual(args, ['--port', '5678']);
    await daemon.locator('[data-plugin-action="settings"]').click();
    assert(await page.locator('#plugin-daemon-autostart').isChecked());
    assert.equal(await page.locator('#plugin-daemon-args').inputValue(), '--port\n5678');
    await page.locator('[data-close-dialog="plugin-daemon-dialog"]').click();
    await daemon.locator('[data-plugin-action="logs"]').click();
    await page.locator('#plugin-daemon-log-dialog').waitFor({ state: 'visible' });
    assert.match(await page.locator('#plugin-daemon-log-output').textContent(), /<img src=x onerror=alert\(1\)>/);
    assert.equal(await page.locator('#plugin-daemon-log-output img').count(), 0, 'daemon logs must not execute HTML');
    await page.locator('[data-close-dialog="plugin-daemon-log-dialog"]').click();
    await daemon.locator('[data-plugin-action="upgrade"]').click();
    await page.locator('#plugin-upgrade-token').fill('fixture-token-not-a-secret');
    failAction = 'upgrade';
    await page.locator('#plugin-upgrade-submit').click();
    await page.locator('.toast.error').filter({ hasText: '模拟upgrade失败' }).waitFor();
    assert(await page.locator('#plugin-upgrade-dialog').isVisible());
    assert(await page.locator('#plugin-upgrade-submit').isEnabled());
    assert.equal(await page.locator('#plugin-upgrade-token').inputValue(), 'fixture-token-not-a-secret');
    await page.locator('#plugin-upgrade-token').fill('');
    await page.locator('#plugin-upgrade-proxy').selectOption('https://ghproxy.net');
    failAction = '';
    await page.locator('#plugin-upgrade-submit').click();
    await page.locator('#plugin-upgrade-dialog').waitFor({ state: 'hidden' });
    assert.equal(actions.filter(action => action.action === 'upgrade').at(-1).body.proxy, 'https://ghproxy.net');
    await daemon.locator('[data-plugin-action="upgrade"]').click();
    assert.equal(await page.locator('#plugin-upgrade-token').inputValue(), '');
    await page.locator('[data-close-dialog="plugin-upgrade-dialog"]').click();
    assert(!await page.evaluate(() => JSON.stringify(localStorage).includes('fixture-token-not-a-secret')));
    await daemon.locator('[data-plugin-action="stop"]').click();
    await daemon.locator('[data-plugin-action="start"]').waitFor({ state: 'visible' });
    installed = false;
    await page.locator('#plugin-refresh').click();
    await daemon.waitFor({ state: 'hidden' });
    await page.locator('#plugin-tab-market').click();
    await page.locator('[data-market-install="crossgg/ncmm-qqbot"]').click();
    assert.equal(await page.locator('#plugin-install-repo').inputValue(), 'crossgg/ncmm-qqbot');
    await page.locator('#plugin-install-proxy').selectOption('https://githubproxy.cc');
    failInstall = true;
    await page.locator('#plugin-install-submit').click();
    await page.locator('.toast.error').filter({ hasText: '模拟安装失败' }).waitFor();
    assert(await page.locator('#plugin-install-dialog').isVisible());
    assert(await page.locator('#plugin-install-submit').isEnabled());
    failInstall = false;
    await page.locator('#plugin-install-submit').click();
    await page.locator('#plugin-install-dialog').waitFor({ state: 'hidden' });
    assert.equal(installations.at(-1).target, 'crossgg/ncmm-qqbot');
    assert.equal(installations.at(-1).proxy, 'https://githubproxy.cc');
    assert(await market.locator('[data-market-manage]').isVisible());
    assert.equal(await market.locator('[data-market-install]').count(), 0, 'newly installed market item must switch to management');
    assert.equal(await market.locator('[data-market-upgrade]').count(), 0, 'up-to-date install must remove upgrade action');
    await page.locator('#plugin-tab-installed').click();
    await page.locator('.plugin-card[data-name="demo"] [data-plugin-action="schedule"]').click();
    assert.deepEqual(await page.locator('#schedule-plugin-select option').evaluateAll(options => options.map(option => option.value)), ['demo']);
    await page.locator('#schedule-dialog').evaluate(dialog => dialog.close());
    await page.locator('.toast').last().waitFor({ state: 'hidden' });
    await page.screenshot({ path: path.join(output, 'plugin-platform-desktop.png'), fullPage: true, animations: 'disabled' });
    for (const width of [320, 390, 768]) {
      await page.setViewportSize({ width, height: 1000 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, `mobile viewport overflow at ${width}`);
      // 后台轮询会替换卡片节点，在同一次浏览器求值中读取元素和布局，避免跨请求读到已脱离的旧节点。
      const installedLayout = await page.locator('.plugin-card[data-name="qqbot"], #plugin-check-updates:visible, #plugin-cards-grid button:visible').evaluateAll(elements => elements.map(element => {
        const { x, width, height } = element.getBoundingClientRect();
        return { card: element.classList.contains('plugin-card'), action: element.dataset.pluginAction, label: element.textContent.trim(), x, width, height };
      }));
      const bounds = installedLayout.find(element => element.card);
      const fitsViewport = element => element && element.width > 0 && element.height > 0 && element.x >= 0 && element.x + element.width <= width + 1;
      if (!fitsViewport(bounds)) await page.screenshot({ path: path.join(output, `plugin-clipped-${width}.png`), fullPage: true });
      assert(fitsViewport(bounds), `plugin card clipped at ${width}: ${JSON.stringify(bounds)}`);
      const pluginActions = installedLayout.filter(element => !element.card);
      for (const action of ['start', 'restart', 'logs', 'settings', 'upgrade', 'delete', 'schedule', 'run']) {
        assert(pluginActions.some(element => element.action === action), `plugin action missing at ${width}: ${action}`);
      }
      for (const action of pluginActions) {
        assert(fitsViewport(action), `plugin action clipped at ${width}: ${action.label} ${JSON.stringify(action)}`);
      }
      await daemon.locator('[data-plugin-action="upgrade"]').click();
      const dialog = await page.locator('#plugin-upgrade-dialog').boundingBox();
      assert(dialog && dialog.x >= 0 && dialog.x + dialog.width <= width + 1, `upgrade dialog clipped at ${width}`);
      await page.locator('[data-close-dialog="plugin-upgrade-dialog"]').click();
      await page.locator('#plugin-tab-market').click();
      const marketActions = await page.locator('#plugin-market-grid button:visible').evaluateAll(elements => elements.map(element => {
        const { x, width, height } = element.getBoundingClientRect();
        return { label: element.textContent.trim(), x, width, height };
      }));
      assert(marketActions.length > 0, `market actions missing at ${width}`);
      for (const action of marketActions) {
        assert(fitsViewport(action), `market action clipped at ${width}: ${action.label} ${JSON.stringify(action)}`);
      }
      if (width === 390) await page.screenshot({ path: path.join(output, 'plugin-market-mobile.png'), fullPage: true, animations: 'disabled' });
      await page.locator('#plugin-tab-installed').click();
      if (width === 390) await page.screenshot({ path: path.join(output, 'plugin-platform-mobile.png'), fullPage: true, animations: 'disabled' });
    }
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ result: 'passed', actionsMocked: actions.length, installationsMocked: installations.length, screenshots: output }));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
