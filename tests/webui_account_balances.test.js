const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../internal/webui/static/app.js'), 'utf8');
const boot = /\r?\n  boot\(\);\r?\n\}\)\(\);\s*$/;
assert(boot.test(source), 'WebUI entry point changed; update the isolated test harness');
const account = './main/cookie.json';
const response = (data, status = 200) => ({ ok: status === 200, status, headers: { get: () => 'application/json' }, json: async () => data });

function harness(fetcher) {
  const elements = new Map(), calls = [];
  const document = { querySelector(selector) {
    if (!elements.has(selector)) elements.set(selector, { textContent: '', innerHTML: '', querySelectorAll: () => [] });
    return elements.get(selector);
  } };
  const context = vm.createContext({ AbortController, document, fetch: (...args) => { calls.push(args); return fetcher(...args); } });
  vm.runInContext(source.replace(boot, '\n  globalThis.ui = { state, renderAccounts, refreshAccountBalances };\n})();'), context);
  const ui = context.ui;
  ui.state.authenticated = true;
  ui.state.currentPage = 'accounts';
  ui.state.config = { data: { accounts: { main: account, secondary: [] } } };
  return { ...ui, calls, growth() {
    const cells = [...document.querySelector('#accounts-table').innerHTML.matchAll(/<td(?: [^>]*)?>([\s\S]*?)<\/td>/g)];
    return cells[5]?.[1].trim();
  } };
}

function growthRun(total, today = 6) {
  return { id: 'growth-history', command: 'sign', status: 'success', startedAt: new Date().toISOString(), rewards: [{ account, growthKnown: true, growthToday: today, growthTotal: total }] };
}

test('没有今日任务收益仍展示接口返回的成长总值，等级缺失不丢弃总值', async () => {
  const ui = harness(async () => response({ account, growthTotal: 8224, vipLevel: null, errors: { growth: 'VIP 等级暂不可用' } }));
  await ui.refreshAccountBalances();
  assert.equal(ui.growth(), '-- / 8224');
  assert.equal(ui.calls.length, 1);
  assert.equal(ui.calls[0][0], `/api/v1/accounts/data?account=${encodeURIComponent(account)}`);
});

test('成长总值使用实时数据且保留真实零，今日成长继续取任务收益', async () => {
  let total = 8224;
  const ui = harness(async () => response({ account, growthTotal: total }));
  ui.state.runs = [growthRun(7777)];
  await ui.refreshAccountBalances();
  assert.equal(ui.growth(), '6 / 8224');
  total = 0;
  await ui.refreshAccountBalances(true);
  assert.equal(ui.growth(), '6 / 0');
});

test('成长总值缺失或刷新失败显示未知，不沿用任务总值或上次余额', async () => {
  let payload = { account, growthTotal: 8224 }, status = 200;
  const ui = harness(async () => response(payload, status));
  ui.state.runs = [growthRun(7777)];
  await ui.refreshAccountBalances();
  assert.equal(ui.growth(), '6 / 8224');
  for (const value of [null, undefined, '8224', NaN]) {
    payload = { account, growthTotal: value };
    await ui.refreshAccountBalances(true);
    assert.equal(ui.growth(), '6 / 未知');
  }
  status = 503;
  payload = { error: '模拟查询失败' };
  await ui.refreshAccountBalances(true);
  assert.equal(ui.growth(), '6 / 未知');
});

test('其他账号响应的成长总值不能写入当前账号', async () => {
  const ui = harness(async () => response({ account: './other/cookie.json', growthTotal: 9999 }));
  await ui.refreshAccountBalances();
  assert.equal(ui.growth(), '-- / 未知');
});
