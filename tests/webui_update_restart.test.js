const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../internal/webui/static/app.js'), 'utf8');
const boot = /\r?\n  boot\(\);\r?\n\}\)\(\);\s*$/;

function harness(replies = [], answers = []) {
  const nodes = new Map();
  const requests = [];
  const questions = [];
  let clock = 0;
  let reloads = 0;
  const document = { querySelector(selector) {
    if (!nodes.has(selector)) nodes.set(selector, { textContent: '', disabled: false, hidden: false, className: '', classList: { toggle(name, value) { if (name === 'hidden') nodes.get(selector).hidden = value; } } });
    return nodes.get(selector);
  } };
  const context = vm.createContext({ document, AbortController, Date: { now: () => clock }, clearTimeout() {}, setTimeout(fn, ms) { if (ms <= 1000) { clock += ms; fn(); } return 1; },
    location: { reload() { reloads++; } }, confirm(message) { questions.push(message); return answers.length ? answers.shift() : true; },
    request: async (url, options) => { requests.push({ url, options }); const reply = replies.shift(); if (reply instanceof Error) throw reply; return reply; },
  });
  vm.runInContext(source.replace(boot, `
    api = publicApi = request;
    toast = () => {};
    renderSystem = () => {};
    startPolling = () => {};
    globalThis.ui = { state, renderUpdateStatus, applyUpdate, restartService, waitForRestart };
  })();`), context);
  return { ...context.ui, nodes, requests, questions, get reloads() { return reloads; } };
}

test('更新动作按最新、可下载、待重启及未受管容器状态显示', () => {
  const ui = harness();
  ui.state.update = { available: true, canApply: true };
  ui.renderUpdateStatus();
  assert.equal(ui.nodes.get('#update-apply').hidden, false);
  assert.equal(ui.nodes.get('#update-apply').textContent, '立即更新');
  ui.state.update = { available: true, canApply: false, restartRequired: true };
  ui.renderUpdateStatus();
  assert.equal(ui.nodes.get('#update-apply').textContent, '立即重启生效');
  assert.equal(ui.nodes.get('#update-status-badge').textContent, '待重启');
  ui.state.update = { available: true, canApply: false, docker: true };
  ui.renderUpdateStatus();
  assert.equal(ui.nodes.get('#update-apply').hidden, true);
  ui.state.update = { available: false };
  ui.renderUpdateStatus();
  assert.equal(ui.nodes.get('#update-apply').hidden, true);
});

test('重启探测忽略旧实例、网络中断和空响应，直到新实例才返回', async () => {
  const ui = harness([{ instanceId: 'old' }, new Error('offline'), {}, { instanceId: 'new' }]);
  const result = await ui.waitForRestart('old', 10000);
  assert.equal(result.instanceId, 'new');
  assert.equal(ui.requests.length, 4);
  assert.equal(ui.reloads, 0);
});

test('重启失败保留可见错误并恢复按钮，允许稍后重试', async () => {
  const ui = harness([new Error('等待服务重启超时，请检查日志')]);
  ui.state.update = { available: true, restartRequired: true };
  await ui.restartService();
  assert.match(ui.nodes.get('#update-message').textContent, /超时/);
  assert.equal(ui.nodes.get('#update-apply').disabled, false);
  assert.equal(ui.nodes.get('#update-apply').textContent, '立即重启生效');
});

test('旧实例一直在线也必须超时，不能误判已重启', async () => {
  const ui = harness(Array(10).fill({ instanceId: 'old' }));
  await assert.rejects(ui.waitForRestart('old', 2000), /超时/);
  assert.equal(ui.reloads, 0);
});

test('繁忙重启显示服务器实时数量，显式force再请求并在新实例上线后刷新', async () => {
  const busy = Object.assign(new Error('busy'), { status: 409, details: { confirmationRequired: true, running: 2, queued: 3 } });
  const ui = harness([busy, { restarting: true, instanceId: 'old' }, { instanceId: 'old' }, { instanceId: 'new' }]);
  await ui.restartService();
  assert.match(ui.questions[1], /2.*3/);
  assert.equal(ui.requests[0].options.body.force, false);
  assert.equal(ui.requests[1].options.body.force, true);
  assert.equal(ui.reloads, 1);
});

test('应用成功后询问重启，应用失败不会请求重启', async () => {
  const ui = harness([{ available: true, restartRequired: true }, { restarting: true, instanceId: 'old' }, { instanceId: 'new' }]);
  await ui.applyUpdate();
  assert.equal(ui.requests[0].url, '/api/v1/update/apply');
  assert.match(ui.questions[0], /安装.*重启/);
  assert.equal(ui.reloads, 1);
  const failed = harness([new Error('download failed')]);
  await failed.applyUpdate();
  assert.equal(failed.requests.length, 1);
  assert.equal(failed.questions.length, 0);
  assert.equal(failed.state.updateBusy, false);
});

test('取消安装后的重启保留待重启状态，取消繁忙确认不发送force', async () => {
  const installed = harness([{ available: true, restartRequired: true }], [false]);
  await installed.applyUpdate();
  assert.equal(installed.requests.length, 1);
  assert.equal(installed.state.update.restartRequired, true);
  const busy = Object.assign(new Error('busy'), { status: 409, details: { confirmationRequired: true, running: 1, queued: 2 } });
  const ui = harness([busy], [true, false]);
  await ui.restartService();
  assert.equal(ui.requests.length, 1);
  assert.equal(ui.state.restartPending, false);
  assert.equal(ui.reloads, 0);
});

test('更新期间被拒绝重启时不发送force请求', async () => {
  const ui = harness([Object.assign(new Error('正在应用更新'), { status: 409, details: { error: '正在应用更新' } })]);
  await ui.restartService();
  assert.equal(ui.requests.length, 1);
  assert.equal(ui.questions.length, 1);
  assert.equal(ui.reloads, 0);
});
