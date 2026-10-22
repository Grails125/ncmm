const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../internal/webui/static/app.js'), 'utf8');
const boot = /\r?\n  boot\(\);\r?\n\}\)\(\);\s*$/;
assert(boot.test(source), 'WebUI entry point changed; update the isolated test harness');
const loginAt = '2026-10-22T10:00:00Z';
const oldAt = '2026-10-22T09:00:00Z';
const newAt = '2026-10-22T11:00:00Z';

function harness() {
  const context = vm.createContext({ document: {} });
  vm.runInContext(source.replace(boot, '\n  globalThis.accountUI = { state, latestAccountProfile };\n})();'), context);
  const ui = context.accountUI;
  ui.state.config = {
    data: { accounts: { main: './main/cookie.json', secondary: ['./other/cookie.json'] } },
    accountProfiles: { './main/cookie.json': { nickname: '新登录', cookieVerifiedAt: loginAt } },
  };
  return ui;
}

const run = (startedAt, valid, account = './main/cookie.json', finishedAt = startedAt) => ({
  startedAt, finishedAt, rewards: [{ account, cookieKnown: true, cookieValid: valid }],
});

test('已验证登录立即从未知变为有效，不需要运行任务', () => {
  const ui = harness();
  const status = ui.latestAccountProfile('./main/cookie.json');
  assert.equal(status?.cookieKnown, true);
  assert.equal(status?.cookieValid, true);
  assert.equal(ui.latestAccountProfile('./other/cookie.json'), null);
});

test('新登录覆盖旧失败及登录前已启动的任务，登录后的失败仍有效', () => {
  const ui = harness();
  ui.state.runs = [run(oldAt, false, './main/cookie.json', newAt)];
  assert.equal(ui.latestAccountProfile('./main/cookie.json')?.cookieValid, true);
  ui.state.runs.unshift(run(newAt, false));
  assert.equal(ui.latestAccountProfile('./main/cookie.json')?.cookieValid, false);
  ui.state.runs.push(run('2026-10-22T12:00:00Z', true));
  assert.equal(ui.latestAccountProfile('./main/cookie.json')?.cookieValid, true, 'task ordering must not decide freshness');
});

test('同名不同目录的 Cookie 和含糊的旧日志不得串账号状态', () => {
  const ui = harness();
  ui.state.runs = [run(newAt, false, './other/cookie.json')];
  assert.equal(ui.latestAccountProfile('./main/cookie.json')?.cookieValid, true);
  assert.equal(ui.latestAccountProfile('./other/cookie.json')?.cookieValid, false);
  ui.state.runs = [run(newAt, false, 'cookie.json')];
  assert.equal(ui.latestAccountProfile('./main/cookie.json')?.cookieValid, true);
  assert.notEqual(ui.latestAccountProfile('./other/cookie.json')?.cookieKnown, true);
  ui.state.config = { data: { accounts: { main: './cookie.json', secondary: ['./other/cookie.json'] } } };
  assert.notEqual(ui.latestAccountProfile('./cookie.json')?.cookieKnown, true, 'a bare historical filename is ambiguous even when one configured path is in the current directory');
});

test('未验证昵称、头像及无效验证时间不能让未知 Cookie 变为有效', () => {
  const ui = harness();
  ui.state.config.accountProfiles['./other/cookie.json'] = { nickname: '仅元数据', cookieVerifiedAt: 'invalid' };
  assert.notEqual(ui.latestAccountProfile('./other/cookie.json')?.cookieKnown, true);
  ui.state.runs = [{ startedAt: newAt, rewards: [{ account: './main/cookie.json', nickname: '网络错误', cookieKnown: false }] }];
  assert.equal(ui.latestAccountProfile('./main/cookie.json')?.cookieValid, true);
});

test('没有登录记录时保留唯一账号的旧日志兼容与 Cookie 未知语义', () => {
  const ui = harness();
  ui.state.config = { data: { accounts: { main: './cookie.json', secondary: [] } } };
  ui.state.runs = [run(oldAt, false, 'cookie.json')];
  assert.equal(ui.latestAccountProfile('./cookie.json')?.cookieKnown, true);
  assert.equal(ui.latestAccountProfile('./cookie.json')?.cookieValid, false);
  ui.state.runs = [];
  assert.equal(ui.latestAccountProfile('./cookie.json'), null);
});

test('Linux 路径大小写不同的 Cookie 不共享登录验证记录', () => {
  const ui = harness();
  ui.state.config.data.accounts = { main: './main/cookie.json', secondary: ['./main/Cookie.json'] };
  assert.equal(ui.latestAccountProfile('./main/cookie.json')?.cookieValid, true);
  assert.notEqual(ui.latestAccountProfile('./main/Cookie.json')?.cookieKnown, true);
});

test('没有时间的历史状态只在没有已验证登录或可靠任务结果时兜底', () => {
  const ui = harness();
  const untimed = { rewards: [{ account: './main/cookie.json', cookieKnown: true, cookieValid: false }] };
  ui.state.runs = [untimed];
  assert.equal(ui.latestAccountProfile('./main/cookie.json')?.cookieValid, true);
  ui.state.config.accountProfiles = {};
  assert.equal(ui.latestAccountProfile('./main/cookie.json')?.cookieValid, false);
  ui.state.runs.push(run(newAt, true));
  assert.equal(ui.latestAccountProfile('./main/cookie.json')?.cookieValid, true);
});
