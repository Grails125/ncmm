const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const directory = path.join(__dirname, '../internal/webui/static');
const policySource = fs.readFileSync(path.join(directory, 'upgrade-policy.js'), 'utf8');
const appSource = fs.readFileSync(path.join(directory, 'app.js'), 'utf8');
const policy = { required: true, currentVersion: '1.2.9-grails.1', requiredVersion: '1.2.9-grails.2', graceUntil: '2026-10-01T00:00:00Z', updateURL: 'javascript:alert(1)', reason: '<img src=x onerror=alert(1)>' };

function renderer() {
  const banners = [false, true].map(login => {
    const fields = new Map();
    return { hidden: true, dataset: {}, classList: { toggle() {} }, querySelector(selector) {
      if (!fields.has(selector)) fields.set(selector, { textContent: '', href: '', hidden: false });
      return fields.get(selector);
    }, fields, login };
  });
  const context = vm.createContext({ document: { querySelectorAll: () => banners }, Date, URL });
  vm.runInContext(policySource, context);
  return { render: context.NCMMUpgradePolicy.render, banners };
}

test('到期及宽限期提示使用安全文本和固定发布地址', () => {
  const ui = renderer();
  ui.render(policy);
  for (const banner of ui.banners) {
    assert.equal(banner.hidden, false);
    assert.match(banner.fields.get('[data-upgrade-title]').textContent, /需要升级/);
    assert.match(banner.fields.get('[data-upgrade-message]').textContent, /1\.2\.9-grails\.1.*1\.2\.9-grails\.2/);
    assert.equal(banner.fields.get('[data-upgrade-release]').href, 'https://github.com/Grails125/ncmm/releases/tag/1.2.9-grails.2');
    assert(!banner.fields.get('[data-upgrade-message]').textContent.includes('<img'));
  }
  ui.render({ ...policy, required: false });
  assert.match(ui.banners[0].fields.get('[data-upgrade-message]').textContent, /仍可执行任务/);
  assert.match(ui.banners[0].fields.get('[data-upgrade-title]').textContent, /提醒/);
});

test('旧序号后缀升级到 grails 以及后续版本实际渲染两处提醒', () => {
  const ui = renderer();
  for (const [currentVersion, requiredVersion] of [['1.2.2-grails.1', '1.2.3-grails'], ['1.2.3-grails', '1.2.4-grails'], ['v1.2.2-grails.1', 'v1.2.3-grails']]) {
    for (const required of [false, true]) {
      ui.render({ ...policy, currentVersion, requiredVersion, required });
      for (const banner of ui.banners) {
        assert.equal(banner.hidden, false);
        const message = banner.fields.get('[data-upgrade-message]').textContent;
        assert(message.includes(`当前版本 ${currentVersion}，要求版本 ${requiredVersion}`));
        assert.match(message, required ? /宽限期已结束/ : /仍可执行任务/);
        assert.equal(banner.fields.get('[data-upgrade-release]').href, `https://github.com/Grails125/ncmm/releases/tag/${requiredVersion}`);
      }
    }
  }
});

test('错误拼写、无效序号及附加路径仍不展示受信任发布链接', () => {
  const ui = renderer();
  for (const version of ['1.2.3-grals', '1.2.3-grails.x', '1.2.3-grails+other', '1.2.3-grails/other']) {
    for (const field of ['currentVersion', 'requiredVersion']) {
      ui.render({ ...policy, currentVersion: '1.2.2-grails.1', requiredVersion: '1.2.3-grails', [field]: version });
      assert(ui.banners.every(banner => banner.hidden));
    }
  }
});

test('未知分发版本及已失效策略隐藏提示，不在浏览器自行推导强制升级', () => {
  const ui = renderer();
  for (const invalid of [undefined, {}, { required: false, currentVersion: policy.currentVersion }, { ...policy, currentVersion: '1.2.9' }, { ...policy, requiredVersion: '<svg onload=alert(1)>' }]) {
    ui.render(policy);
    ui.render(invalid);
    assert(ui.banners.every(banner => banner.hidden));
  }
  assert(!policySource.includes('innerHTML'));
  assert(!policySource.includes('.style'));
});

test('业务API的426更新提示并保留登录会话，其他错误保留原状态', async () => {
  const seen = [];
  let response = { status: 426, ok: false, headers: { get: () => 'application/json' }, json: async () => ({ error: '请升级', upgradePolicy: policy }) };
  const context = vm.createContext({ fetch: async () => response, NCMMUpgradePolicy: { render: value => seen.push(value) } });
  const boot = /\r?\n  boot\(\);\r?\n\}\)\(\);\s*$/;
  vm.runInContext(appSource.replace(boot, '\n globalThis.ui = { api, state, applyUpgradePolicy };\n})();'), context);
  context.ui.state.authenticated = true;
  context.ui.state.csrf = 'fixture-csrf';
  await assert.rejects(context.ui.api('/api/v1/schedules/test/run', { method: 'POST' }), error => error.status === 426 && error.details.upgradePolicy.required);
  assert.equal(context.ui.state.authenticated, true);
  assert.equal(context.ui.state.csrf, 'fixture-csrf');
  assert.equal(seen.length, 1);
  response = { ...response, status: 500, json: async () => ({ error: '普通错误' }) };
  await assert.rejects(context.ui.api('/api/v1/status'), /普通错误/);
  assert.equal(seen.length, 1);
  context.ui.applyUpgradePolicy({ required: false, currentVersion: policy.currentVersion });
  assert.equal(seen.length, 2);
  assert.equal(context.ui.state.auth.upgradePolicy.required, false);
});
