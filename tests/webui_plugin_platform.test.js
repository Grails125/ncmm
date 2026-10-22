const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../internal/webui/static/app.js'), 'utf8');
const boot = /\r?\n  boot\(\);\r?\n\}\)\(\);\s*$/;

function harness(request) {
  const nodes = new Map(), messages = [];
  let now = Date.UTC(2026, 9, 22, 12);
  let timer;
  const document = { querySelector(selector) {
    if (!nodes.has(selector)) nodes.set(selector, { innerHTML: '', textContent: '', value: '', checked: false, disabled: false, dataset: {}, classList: { add() {}, remove() {}, toggle() {} }, showModal() { this.open = true; }, close() { this.open = false; }, addEventListener() {} });
    return nodes.get(selector);
  }, querySelectorAll() { return []; } };
  const context = vm.createContext({ document, request, Date: class extends Date { static now() { return now; } }, setTimeout(fn) { timer = fn; return 1; }, clearTimeout() { timer = null; }, report: (...args) => messages.push(args) });
  vm.runInContext(source.replace(boot, '\n api = request; toast = report; globalThis.ui = { state, renderPlugins, loadPluginMarket, updateSchedulePluginPanel, checkPluginUpdates, openDaemonSettings, openPluginUpgrade, installPlugin };\n})();'), context);
  return { ...context.ui, nodes, get: document.querySelector, messages, advance(ms) { now += ms; const callback = timer; timer = null; callback?.(); } };
}

const daemon = { name: 'qqbot', title: 'QQ 机器人', type: 'daemon', running: true, autoStart: true, source: { tag: 'v1.0.1' }, update: { latestTag: 'v1.0.2', updateAvailable: true } };
const task = { name: 'demo', title: '<script>alert(1)</script>', type: 'task', recommendedCron: '0 8 * * *' };

test('市场卡片关联已安装来源、显示真实版本与管理升级入口，并转义元数据', async () => {
  const ui = harness(async () => [{ name: 'qqbot', title: 'QQ 机器人', repository: 'crossgg/ncmm-qqbot', author: '<img src=x>', features: ['扫码登录', '<script>'], update: { latestTag: 'v1.0.2' } }]);
  ui.state.plugins = [{ ...daemon, source: { repository: 'crossgg/ncmm-qqbot', tag: 'v1.0.1' } }];
  await ui.loadPluginMarket();
  const html = ui.get('#plugin-market-grid').innerHTML;
  assert.match(html, /crossgg\/ncmm-qqbot/);
  assert.match(html, /v1\.0\.2/);
  assert.match(html, /已安装/);
  assert.match(html, /前往管理/);
  assert.match(html, /data-market-upgrade="qqbot"/);
  assert(!html.includes('data-market-install='));
  assert(!html.includes('<img src=x>'));
  assert(!html.includes('<script>'));
  ui.state.plugins = [];
  await ui.loadPluginMarket();
  assert.match(ui.get('#plugin-market-grid').innerHTML, /data-market-install=/);
});

test('手动检查成功后展示60秒倒计时，重复检查被拦截，到时恢复', async () => {
  let checks = 0;
  const ui = harness(async url => {
    if (url.endsWith('/updates')) { checks++; return [{ repository: 'owner/demo', checkedAt: '2026-10-22T12:00:00Z', latestTag: 'v2' }]; }
    return [];
  });
  await ui.checkPluginUpdates(true);
  assert.equal(ui.get('#plugin-check-updates').disabled, true);
  assert.match(ui.get('#plugin-check-updates').textContent, /60/);
  await ui.checkPluginUpdates(true);
  assert.equal(checks, 1);
  ui.advance(30000);
  assert.match(ui.get('#plugin-check-updates').textContent, /30/);
  ui.advance(30000);
  assert.equal(ui.get('#plugin-check-updates').disabled, false);
});

test('重新读取市场缓存恢复检查倒计时，不依赖浏览器存储', async () => {
  const ui = harness(async () => [{ name: 'qqbot', title: 'QQ 机器人', repository: 'crossgg/ncmm-qqbot', update: { checkedAt: '2026-10-22T11:59:40Z', latestTag: 'v1' } }]);
  await ui.loadPluginMarket();
  assert.equal(ui.get('#plugin-check-updates').disabled, true);
  assert.match(ui.get('#plugin-check-updates').textContent, /40/);
});

test('常驻与任务插件操作分离，定时表单只接受任务插件且文本转义', () => {
  const ui = harness();
  ui.state.plugins = [daemon]; ui.renderPlugins();
  const html = ui.get('#plugin-cards-grid').innerHTML;
  assert.match(html, /data-plugin-action="stop"/);
  assert.match(html, /data-plugin-action="upgrade"/);
  assert(!html.includes('data-plugin-action="schedule"'));
  assert(!html.includes('data-plugin-action="run"'));
  ui.state.plugins = [daemon, task];
  ui.renderPlugins();
  assert(!ui.get('#plugin-cards-grid').innerHTML.includes('<script>'));
  ui.get('#schedule-command').value = 'plugin';
  ui.updateSchedulePluginPanel('qqbot');
  const choices = ui.get('#schedule-plugin-select').innerHTML;
  assert(choices.includes('value="demo"'));
  assert(!choices.includes('qqbot'));
  assert.equal(ui.get('#schedule-plugin-select').value, 'demo');
});

test('插件更新检查去重，失败恢复按钮并允许再次检查', async () => {
  const requests = [];
  let release;
  const pending = new Promise(resolve => { release = resolve; });
  let fails = true;
  const ui = harness(async (url, options) => {
    requests.push({ url, options });
    if (url.endsWith('/updates')) { await pending; if (fails) throw new Error('检查失败'); }
    return [];
  });
  const first = ui.checkPluginUpdates(true);
  assert.equal(ui.get('#plugin-check-updates').disabled, true);
  await ui.checkPluginUpdates(true);
  assert.equal(requests.length, 1);
  release(); await first;
  assert.equal(ui.get('#plugin-check-updates').disabled, false);
  assert.match(ui.messages[0][0], /检查失败/);
  fails = false;
  await ui.checkPluginUpdates(false);
  assert.equal(requests[1].options.body.force, false);
  assert.equal(ui.get('#plugin-check-updates').disabled, false);
});

test('常驻设置读取服务端参数，打开升级弹窗清除上次凭据', async () => {
  const requests = [];
  const ui = harness(async (url) => { requests.push(url); return { autoStart: true, args: ['--port', '5678'] }; });
  await ui.openDaemonSettings(daemon);
  assert.equal(requests[0], '/api/v1/plugins/qqbot/daemon');
  assert.equal(ui.get('#plugin-daemon-autostart').checked, true);
  assert.equal(ui.get('#plugin-daemon-args').value, '--port\n5678');
  assert.equal(ui.get('#plugin-daemon-dialog').open, true);
  ui.get('#plugin-upgrade-token').value = 'previous-fixture-token';
  ui.get('#plugin-upgrade-proxy').value = 'https://ghproxy.net';
  ui.openPluginUpgrade(daemon);
  assert.equal(ui.get('#plugin-upgrade-token').value, '');
  assert.equal(ui.get('#plugin-upgrade-proxy').value, '');
  assert.match(ui.get('#plugin-upgrade-title').textContent, /v1.0.2/);
});

test('安装沿用统一target/tag/token/proxy契约，失败保留表单，成功清空凭据', async () => {
  const requests = [];
  let fails = true;
  const ui = harness(async (url, options) => {
    requests.push({ url, options });
    if (url.endsWith('/install') && fails) throw new Error('下载失败');
    return url.endsWith('/install') ? { success: true } : [];
  });
  ui.get('#plugin-install-dialog').open = true;
  ui.get('#plugin-install-token').value = 'fixture-token';
  await ui.installPlugin(' owner/repo ', ' v2 ', ' fixture-token ', '');
  assert.equal(JSON.stringify(requests[0].options.body), JSON.stringify({ target: 'owner/repo', tag: 'v2', token: 'fixture-token', proxy: '' }));
  assert.equal(ui.get('#plugin-install-dialog').open, true);
  assert.equal(ui.get('#plugin-install-submit').disabled, false);
  assert.equal(ui.get('#plugin-install-token').value, 'fixture-token');
  fails = false;
  await ui.installPlugin('owner/repo', 'v2', '', 'https://ghproxy.net');
  assert.equal(requests[1].options.body.proxy, 'https://ghproxy.net');
  assert.equal(ui.get('#plugin-install-dialog').open, false);
  assert.equal(ui.get('#plugin-install-token').value, '');
});
