const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../internal/webui/static/app.js'), 'utf8');
const boot = /\r?\n  boot\(\);\r?\n\}\)\(\);\s*$/;

function harness() {
  const nodes = new Map();
  const document = { querySelector(selector) {
    if (!nodes.has(selector)) nodes.set(selector, { textContent: '', innerHTML: '', className: '', dataset: {}, classList: { toggle() {} }, setAttribute() {} });
    return nodes.get(selector);
  } };
  const context = vm.createContext({ document });
  vm.runInContext(source.replace(boot, '\n  globalThis.ui = { state, todayAccountRewards, dashboardSeries, renderDashboard, renderDashboardChart, accountRewardFrom };\n})();'), context);
  const ui = context.ui;
  ui.state.config = { data: { accounts: { main: './main/cookie.json', secondary: ['./other/cookie.json'] } } };
  return { ...ui, node: selector => document.querySelector(selector) };
}
function run(account, reward, dayOffset = 0) {
  const date = new Date(); date.setDate(date.getDate() + dayOffset);
  return { id: String(Math.random()), command: 'music-partner', startedAt: date.toISOString(), status: 'success', rewards: [{ account, ...reward }] };
}

test('合伙人日增量按执行相加，七日按账号和日期分组，未知不充当零', () => {
  const ui = harness();
  ui.state.runs = [run('./main/cookie.json', { musicPartnerKnown: true, musicPartnerDelta: 9 }), run('./main/cookie.json', { musicPartnerKnown: true, musicPartnerDelta: -2 }), run('./other/cookie.json', { musicPartnerKnown: true, musicPartnerDelta: 500 }), run('./main/cookie.json', { musicPartnerKnown: true, musicPartnerDelta: 4 }, -1)];
  const today = ui.todayAccountRewards().find(item => item.account === './main/cookie.json');
  assert.equal(today.musicPartnerDelta, 7);
  assert.equal(today.musicPartnerKnown, true);
  const points = ui.dashboardSeries({ path: './main/cookie.json' }, 'musicPartner');
  assert.equal(points.at(-1).value, 7);
  assert.equal(points.at(-2).value, 4);
  assert.equal(points.at(-3).known, false);
});

test('今日收益只展示非零变动，不显示余额总值、未知或无变化账号', () => {
  const ui = harness();
  ui.state.runs = [run('./main/cookie.json', { yunbeiKnown: true, yunbei: 0, yunbeiBalanceKnown: true, yunbeiBalance: 8888, growthKnown: true, growthToday: 3, growthTotal: 7777, musicPartnerKnown: true, musicPartnerDelta: -2 }), run('./other/cookie.json', { yunbeiKnown: true, yunbei: 0, musicPartnerKnown: true, musicPartnerDelta: 0 })];
  ui.renderDashboard();
  const html = ui.node('#account-rewards-list').innerHTML;
  assert.match(html, /成长值 \+3/);
  assert.match(html, /合伙人积分 -2/);
  assert.doesNotMatch(html, /8888|7777|云贝|--/);
  assert.equal((html.match(/class="reward-row"/g) || []).length, 1);
});

test('没有收益变化时显示明确空状态', () => {
  const ui = harness();
  ui.state.runs = [run('./main/cookie.json', { musicPartnerKnown: true, musicPartnerDelta: 0 })];
  ui.renderDashboard();
  assert.equal(ui.node('#account-rewards-list').textContent || ui.node('#account-rewards-list').innerHTML, '今日暂无收益变化');
});

test('同名Cookie不得借用其他目录收益，旧日志唯一文件名仍兼容', () => {
  const ui = harness();
  assert.equal(ui.accountRewardFrom([{ account: './main/cookie.json', musicPartnerDelta: 9 }], './other/cookie.json'), undefined);
  ui.state.config.data.accounts.secondary = [];
  assert.equal(ui.accountRewardFrom([{ account: '/data/cookie.json', musicPartnerDelta: 9 }], './main/cookie.json').musicPartnerDelta, 9);
  assert.equal(ui.accountRewardFrom([{ account: '/data/cookie.json' }, { account: '/other/cookie.json' }], './main/cookie.json'), undefined);
  assert.equal(ui.accountRewardFrom([{ account: './main/cookie.json' }], '/unknown/cookie.json'), undefined);
  assert.equal(ui.accountRewardFrom([{ account: 'main/../main/cookie.json', musicPartnerDelta: 6 }], './main/cookie.json').musicPartnerDelta, 6);
});

test('路径迁移后的同一账号合并今日收益，排队跨午夜按实际开始日计入', () => {
  const ui = harness();
  ui.state.config.data.accounts = { main: './cookie.json', secondary: [] };
  const queued = run('E:\\Downloads\\ncmm\\cookie.json', { musicPartnerKnown: true, musicPartnerDelta: 4 });
  const previous = new Date(queued.startedAt); previous.setDate(previous.getDate() - 1);
  queued.triggeredAt = previous.toISOString();
  ui.state.runs = [run('./cookie.json', { musicPartnerKnown: true, musicPartnerDelta: 3 }), queued];
  const rewards = ui.todayAccountRewards();
  assert.equal(rewards.length, 1);
  assert.equal(rewards[0].account, './cookie.json');
  assert.equal(rewards[0].musicPartnerDelta, 7);
  const points = ui.dashboardSeries({ path: './cookie.json' }, 'musicPartner');
  assert.equal(points.at(-1).value, 7);
  assert.equal(points.at(-2).known, false);
});

test('同次执行同时有完整匹配和模糊旧路径时优先完整记录，不叠加猜测重复项', () => {
  const ui = harness();
  ui.state.config.data.accounts.secondary = [];
  const entry = run('./main/cookie.json', { musicPartnerKnown: true, musicPartnerDelta: 4 });
  entry.rewards.push({ account: '/data/cookie.json', musicPartnerKnown: true, musicPartnerDelta: 4 });
  ui.state.runs = [entry];
  assert.equal(ui.dashboardSeries({ path: './main/cookie.json' }, 'musicPartner').at(-1).value, 4);
  const rewards = ui.todayAccountRewards();
  assert.equal(ui.accountRewardFrom(rewards, './main/cookie.json').musicPartnerDelta, 4);
});

test('积分负变化保持在图表坐标范围内', () => {
  const ui = harness();
  ui.state.runs = [run('./main/cookie.json', { musicPartnerKnown: true, musicPartnerDelta: -3 })];
  ui.state.dashboardMetric = 'musicPartner';
  ui.renderDashboardChart([{ path: './main/cookie.json', label: '主账号', main: true }]);
  const html = ui.node('#dashboard-chart').innerHTML;
  assert.match(html, /音乐合伙人积分变化/);
  assert.match(html, /：-3/);
  for (const [, value] of html.matchAll(/cy="([\d.-]+)"/g)) assert(Number(value) >= 24 && Number(value) <= 156);
});
