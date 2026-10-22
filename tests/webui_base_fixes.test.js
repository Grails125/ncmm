const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../internal/webui/static/app.js'), 'utf8');
const bootCall = /\r?\n  boot\(\);\r?\n\}\)\(\);\s*$/;
assert(bootCall.test(source));

function harness(authFixture) {
  const nodes = new Map();
  const classList = { add() {}, remove() {}, toggle() {} };
  const document = { documentElement: { classList }, querySelector(selector) {
    if (!nodes.has(selector)) nodes.set(selector, { value: '', textContent: selector === '#auth-version' ? 'v1.2.0' : '', classList, focus() {}, close() {} });
    return nodes.get(selector);
  } };
  document.querySelector('#auth-version');
  const calls = [];
  const inputs = [];
  document.querySelectorAll = () => inputs;
  const context = vm.createContext({ document, localStorage: { getItem() { return null; } }, authFixture, calls });
  vm.runInContext(source.replace(bootCall, `
    applyTheme = () => {};
    bindEvents = () => {};
    publicApi = async () => authFixture;
    api = async (url, options) => { calls.push({url, options}); return {success:true}; };
    toast = () => {};
    refreshPlugins = async () => {};
    globalThis.ui = { boot, setAuthMode, installPlugin, syncPluginParamsToArgs, state };
  })();`), context);
  return { ...context.ui, nodes, calls, inputs, document };
}

test('已设置密码的普通登录页显示后端当前版本', async () => {
  const ui = harness({ configured: true, setupRequired: false, authenticated: false, version: 'v1.2.9-custom\nbuild detail' });
  await ui.boot();
  assert.equal(ui.nodes.get('#auth-version').textContent, 'v1.2.9-custom');
});

test('结构化插件参数使用分隔符，插件自己的 config 不会覆盖宿主配置', () => {
  const ui = harness({});
  ui.document.querySelector('#schedule-command').value = 'plugin';
  ui.document.querySelector('#schedule-plugin-select').value = 'demo';
  ui.inputs.push({ dataset: { param: 'config', type: 'string' }, value: 'plugin.yaml' });
  ui.syncPluginParamsToArgs();
  assert.equal(ui.document.querySelector('#schedule-args').value, 'run demo -- --config plugin.yaml');
});

test('未知版本不显示硬编码或过时版本', () => {
  const ui = harness({});
  ui.setAuthMode('login', {});
  assert.equal(ui.nodes.get('#auth-version').textContent, 'NCMM');
});

test('在线安装表单发送统一的 target、tag 和 token 契约', async () => {
  const ui = harness({});
  await ui.installPlugin(' https://github.com/owner/demo ', ' v2 ', ' test-token ');
  assert.equal(ui.calls[0].url, '/api/v1/plugins/install');
  assert.equal(ui.calls[0].options.body.target, 'https://github.com/owner/demo');
  assert.equal(ui.calls[0].options.body.tag, 'v2');
  assert.equal(ui.calls[0].options.body.token, 'test-token');
  assert.equal(ui.calls[0].options.body.repoOrURL, undefined);
});
