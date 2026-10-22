const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../internal/webui/static/app.js'), 'utf8');
const boot = /\r?\n  boot\(\);\r?\n\}\)\(\);\s*$/;
assert(boot.test(source), 'WebUI entry point changed; update the isolated test harness');

function harness(fetcher = async () => { throw new Error('unexpected request'); }) {
  const elements = new Map();
  const calls = [];
  const document = { querySelector(selector) {
    if (!elements.has(selector)) elements.set(selector, { textContent: '', innerHTML: '', disabled: false, className: '', setAttribute(key, value) { this[key] = value; } });
    return elements.get(selector);
  } };
  const context = vm.createContext({ AbortController, document, fetch: (...args) => { calls.push(args); return fetcher(...args); } });
  vm.runInContext(source.replace(boot, '\n  globalThis.points = { state, musicPartnerPointsAccount, musicPartnerPointsView, musicPartnerPeriodDate, renderMusicPartnerPoints, refreshMusicPartnerPoints, refreshMusicPartnerPointsAfterRun, resetMusicPartnerPoints };\n})();'), context);
  const functions = context.points;
  functions.state.authenticated = true;
  functions.state.config = { data: { accounts: { main: './main/cookie.json', secondary: ['./other/cookie.json'] } } };
  return { ...functions, calls, element: selector => document.querySelector(selector) };
}

const response = (data, status = 200) => ({ ok: status === 200, status, headers: { get: () => 'application/json' }, json: async () => data });
const snapshot = (account, integral = 118, weekly = 61) => ({ account, period: 73, week: 2, startDate: 1791734400000, endDate: 1794153599999, integral, currentWeekIntegral: weekly, updatedAt: '2026-10-22T09:00:00Z' });
function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}

test('账号选择独立于播放图表，默认主账号且重复渲染不发请求', () => {
  const ui = harness();
  ui.state.dashboardAccountPath = './other/cookie.json';
  assert.equal(ui.musicPartnerPointsAccount().path, './main/cookie.json');
  ui.renderMusicPartnerPoints();
  ui.renderMusicPartnerPoints();
  assert.equal(ui.calls.length, 0);
  assert.equal(ui.element('#music-partner-points-integral').textContent, '未知');
  ui.state.musicPartnerAccountPath = './other/cookie.json';
  assert.equal(ui.musicPartnerPointsAccount().path, './other/cookie.json');
});

test('真实零积分与缺失积分分别显示，日期明确按北京时间换算', async () => {
  const ui = harness(async () => response(snapshot('./main/cookie.json', 0, null)));
  await ui.refreshMusicPartnerPoints();
  assert.equal(ui.element('#music-partner-points-integral').textContent, '0');
  assert.equal(ui.element('#music-partner-points-weekly').textContent, '未知');
  assert.equal(ui.element('#music-partner-points-status').textContent, '部分积分未知');
  assert.equal(ui.element('#music-partner-points-period').textContent, '第 73 期 · 第 2 周');
  assert.equal(ui.element('#music-partner-points-dates').textContent, '2026/10/12 至 2026/11/08（北京时间）');
  assert.equal(ui.musicPartnerPeriodDate(null), '未知');
  assert.equal(ui.musicPartnerPeriodDate(NaN), '未知');
});

test('没有配置账号时不请求接口；Cookie 缺失错误保留未知', async () => {
  const empty = harness();
  empty.state.config.data.accounts = {};
  await empty.refreshMusicPartnerPoints();
  assert.equal(empty.calls.length, 0);
  assert.equal(empty.element('#music-partner-points-refresh').disabled, true);
  assert.match(empty.element('#music-partner-points-message').textContent, /暂无已配置的 Cookie 账号/);
  const missing = harness(async () => response({ error: '账号 Cookie 文件不存在，请先登录。' }, 400));
  await missing.refreshMusicPartnerPoints();
  assert.equal(missing.element('#music-partner-points-integral').textContent, '未知');
  assert.match(missing.element('#music-partner-points-message').textContent, /Cookie 文件不存在/);
});

test('刷新失败保留所选账号上次积分并标记过期', async () => {
  let failed = false;
  const ui = harness(async () => failed ? response({ error: '暂时无法读取积分' }, 503) : response(snapshot('./main/cookie.json')));
  await ui.refreshMusicPartnerPoints();
  failed = true;
  await ui.refreshMusicPartnerPoints(true);
  assert.equal(ui.element('#music-partner-points-integral').textContent, '118');
  assert.equal(ui.element('#music-partner-points-weekly').textContent, '61');
  assert.equal(ui.element('#music-partner-points-status').textContent, '已过期');
  assert.match(ui.element('#music-partner-points-updated').textContent, /上次成功查询/);
  assert.match(ui.element('#music-partner-points-message').textContent, /保留上次数据/);
});

test('切换账号后查询失败不展示前一个账号的旧积分', async () => {
  const ui = harness(async url => url.includes(encodeURIComponent('./main/cookie.json'))
    ? response(snapshot('./main/cookie.json')) : response({ error: '账号暂不可用' }, 503));
  await ui.refreshMusicPartnerPoints();
  ui.state.musicPartnerAccountPath = './other/cookie.json';
  await ui.refreshMusicPartnerPoints();
  assert.equal(ui.element('#music-partner-points-integral').textContent, '未知');
  assert.equal(ui.element('#music-partner-points-weekly').textContent, '未知');
  assert.equal(ui.state.musicPartnerPoints.data, null);
  assert.equal(ui.element('#music-partner-points-status').textContent, '查询失败');
});

test('切换同名 Cookie 路径时旧响应不得覆盖新账号', async () => {
  const first = deferred(), second = deferred();
  const ui = harness(url => url.includes(encodeURIComponent('./main/cookie.json')) ? first.promise : second.promise);
  const oldRequest = ui.refreshMusicPartnerPoints();
  ui.state.musicPartnerAccountPath = './other/cookie.json';
  const newRequest = ui.refreshMusicPartnerPoints();
  assert.equal(ui.calls[0][1].signal.aborted, true);
  second.resolve(response(snapshot('./other/cookie.json', 9, 3)));
  await newRequest;
  first.resolve(response(snapshot('./main/cookie.json', 118, 61)));
  await oldRequest;
  assert.equal(ui.state.musicPartnerPoints.account, './other/cookie.json');
  assert.equal(ui.element('#music-partner-points-integral').textContent, '9');
});

test('同账号普通刷新去重，强制刷新使旧响应失效', async () => {
  const first = deferred(), second = deferred();
  let count = 0;
  const ui = harness(() => (++count === 1 ? first.promise : second.promise));
  const oldRequest = ui.refreshMusicPartnerPoints();
  await ui.refreshMusicPartnerPoints();
  assert.equal(ui.calls.length, 1);
  const newRequest = ui.refreshMusicPartnerPoints(true);
  second.resolve(response(snapshot('./main/cookie.json', 120, 63)));
  await newRequest;
  first.resolve(response(snapshot('./main/cookie.json', 118, 61)));
  await oldRequest;
  assert.equal(ui.element('#music-partner-points-integral').textContent, '120');
});

test('仅相关任务新完成时自动刷新，普通轮询与后台页面不查询', async () => {
  const ui = harness(async () => response(snapshot('./main/cookie.json')));
  await ui.refreshMusicPartnerPoints();
  const running = [{ id: 'partner-1', command: 'music-partner', status: 'running' }];
  await ui.refreshMusicPartnerPointsAfterRun(running);
  await ui.refreshMusicPartnerPointsAfterRun([{ id: 'sign-1', command: 'sign', finishedAt: '2026-10-22T09:01:00Z' }]);
  assert.equal(ui.calls.length, 1);
  const finished = [{ ...running[0], status: 'success', finishedAt: '2026-10-22T09:02:00Z' }];
  await ui.refreshMusicPartnerPointsAfterRun(finished);
  await ui.refreshMusicPartnerPointsAfterRun(finished);
  assert.equal(ui.calls.length, 2);
  ui.state.currentPage = 'config';
  await ui.refreshMusicPartnerPointsAfterRun([...finished, { id: 'batch-2', command: 'task', finishedAt: '2026-10-22T09:03:00Z' }]);
  assert.equal(ui.calls.length, 2);
});

test('返回错误账号或退出登录后的响应不得写入积分', async () => {
  const wrong = harness(async () => response(snapshot('./other/cookie.json')));
  await wrong.refreshMusicPartnerPoints();
  assert.equal(wrong.element('#music-partner-points-integral').textContent, '未知');
  assert.match(wrong.state.musicPartnerPoints.error, /账号不匹配/);
  const request = deferred();
  const ui = harness(() => request.promise);
  const pending = ui.refreshMusicPartnerPoints();
  ui.state.authenticated = false;
  ui.resetMusicPartnerPoints();
  request.resolve(response(snapshot('./main/cookie.json')));
  await pending;
  assert.equal(ui.state.musicPartnerPoints.data, null);
});
