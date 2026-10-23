const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const root = path.join(__dirname, '..');
const source = fs.readFileSync(path.join(root, 'internal/webui/static/app.js'), 'utf8');
const html = fs.readFileSync(path.join(root, 'internal/webui/static/index.html'), 'utf8');
const boot = /\r?\n  boot\(\);\r?\n\}\)\(\);\s*$/;
const commandSelect = html.match(/<select id="schedule-command">([\s\S]*?)<\/select>/)[1];
const commands = [...commandSelect.matchAll(/<option value="([^"]+)">([^<]+)<\/option>/g)].map(([, value, label]) => ({ value, label }));

function harness() {
  const nodes = new Map();
  const calls = [], inputs = [];
  const document = { querySelector(selector) {
    if (!nodes.has(selector)) nodes.set(selector, {
      value: '', innerHTML: '', textContent: '', placeholder: '', options: [],
      classList: { add() {}, remove() {}, toggle() {} },
      addEventListener() {}, showModal() {}, close() {},
    });
    return nodes.get(selector);
  } };
  document.querySelectorAll = () => inputs;
  const context = vm.createContext({ document, calls });
  vm.runInContext(source.replace(boot, `
    api = async (url, options) => { calls.push({ url, options }); };
    refreshSchedules = async () => {};
    toast = () => {};
    globalThis.ui = { state, updateScheduleCommandHelp, updateSchedulePluginPanel, openScheduleDialog, saveSchedule, syncPluginParamsToArgs, parseArgs };
  })();`), context);
  return { ...context.ui, get: document.querySelector, calls, inputs };
}

test('全部可定时执行的命令保留命令值并提供中文名称', () => {
  const scheduler = fs.readFileSync(path.join(root, 'internal/webui/scheduler.go'), 'utf8');
  const allowed = scheduler.match(/allowedCommands\s*=\s*map\[string\]bool\{([\s\S]*?)\}/);
  assert(allowed, '调度器命令定义变更，请同步测试');
  const values = [...allowed[1].matchAll(/"([^"]+)"\s*:\s*true/g)].map(([, value]) => value);
  assert.deepEqual(commands.map(command => command.value).sort(), values.sort());
  for (const { value, label } of commands) {
    assert(label.includes(value), value);
    assert.match(label, /[\u4e00-\u9fff]/, `${value} 缺少中文名称`);
  }
});

test('切换每种命令都有参数说明，保留已填写参数，并建立可访问的说明关联', () => {
  const ui = harness();
  assert.match(html, /id="schedule-args"[^>]*aria-describedby="schedule-args-help"/);
  assert.match(html, /id="schedule-args-help"[^>]*aria-live="polite"/);
  ui.get('#schedule-args').value = '--cookie-file "sub/my account.json"';
  for (const { value } of commands) {
    ui.get('#schedule-command').value = value;
    ui.updateSchedulePluginPanel();
    const help = ui.get('#schedule-args-help').innerHTML;
    assert.match(help, /只填写参数/, value);
    assert.match(help, /空格/, value);
    assert(help.length > 100, value);
    assert(ui.get('#schedule-args').placeholder, value);
    assert.equal(ui.get('#schedule-args').value, '--cookie-file "sub/my account.json"', value);
  }
});

test('打开新建和已有任务时立即显示对应帮助，避免残留上一命令提示', () => {
  const ui = harness();
  ui.openScheduleDialog();
  assert.match(ui.get('#schedule-args-help').innerHTML, /--only-fast/);
  ui.openScheduleDialog({ command: 'music-partner', args: ['--cookie-file', 'sub/partner.json'] });
  assert.match(ui.get('#schedule-args-help').innerHTML, /评分规则/);
  assert.doesNotMatch(ui.get('#schedule-args-help').innerHTML, /--only-fast/);
  assert.equal(ui.get('#schedule-args').value, '--cookie-file sub/partner.json');
});

test('帮助与实际 CLI 参数一致，任务选择和无需参数命令说明准确', () => {
  const ui = harness();
  const files = { 'musician-sign': 'musician', 'musician-vip': 'musician', 'music-partner': 'music_partner', 'daily-song-share': 'daily_song_share', 'vip-member-gift': 'vip_member_gift' };
  for (const { value } of commands.filter(command => command.value !== 'plugin')) {
    ui.get('#schedule-command').value = value;
    ui.updateScheduleCommandHelp();
    const help = ui.get('#schedule-args-help').innerHTML;
    const cli = fs.readFileSync(path.join(root, `internal/ncmm/${files[value] || value}.go`), 'utf8');
    const flags = new Set([...cli.matchAll(/\.Flags\(\)\.\w+\([^\n]*?"([a-z][a-z-]*)"/g)].map(([, flag]) => flag));
    for (const [, flag] of help.matchAll(/--([a-z][a-z-]*)/g)) assert(flags.has(flag), `${value} 未声明 --${flag}`);
    if (value.startsWith('musician')) assert.match(help, /留空/);
    if (value === 'task') {
      assert.match(help, /仅执行显式选择的任务/);
      assert.match(help, /不能同时/);
    }
    if (value === 'music-partner') assert.match(help, /留空/);
  }
});

test('带空格的路径和插件参数示例可按当前解析器正确拆分', () => {
  const ui = harness();
  assert.deepEqual(Array.from(ui.parseArgs('--cookie-file "sub/my account.json"')), ['--cookie-file', 'sub/my account.json']);
  ui.get('#schedule-command').value = 'plugin';
  ui.updateScheduleCommandHelp();
  assert.match(ui.get('#schedule-args-help').innerHTML, /run demo -- --days 7/);
  assert.match(ui.get('#schedule-args-help').innerHTML, /插件说明/);
  assert.deepEqual(Array.from(ui.parseArgs(ui.get('#schedule-args').placeholder)), ['run', 'demo', '--', '--days', '7']);
});

test('含空格的 Windows、UNC 和容器路径创建、编辑再保存时参数保持不变', async () => {
  const paths = [String.raw`C:\Users\my account\cookie.json`, String.raw`\\server\shared folder\cookie.json`, 'C:\\account files\\', '/data/my account/cookie.json'];
  for (const cookiePath of paths) {
    const ui = harness();
    ui.openScheduleDialog();
    ui.get('#schedule-name').value = '指定账号';
    ui.get('#schedule-command').value = 'music-partner';
    ui.get('#schedule-args').value = `--cookie-file "${cookiePath}"`;
    await ui.saveSchedule({ preventDefault() {} });
    const created = ui.calls[0].options.body;
    assert.deepEqual(Array.from(created.args), ['--cookie-file', cookiePath]);
    ui.openScheduleDialog({ ...created, id: 'existing' });
    await ui.saveSchedule({ preventDefault() {} });
    assert.equal(ui.calls[1].url, '/api/v1/schedules/existing');
    assert.deepEqual(Array.from(ui.calls[1].options.body.args), ['--cookie-file', cookiePath]);
  }
});

test('插件参数回填保留单双引号、空值和反斜杠，结构化表单使用相同规则', () => {
  const ui = harness();
  const value = String.raw`他说 "don't change C:\my files\"`;
  const args = ['run', 'demo', '--', '--title', value, '--empty', '', '--literal', String.raw`\n\t\\`];
  ui.openScheduleDialog({ command: 'plugin', args });
  assert.deepEqual(Array.from(ui.parseArgs(ui.get('#schedule-args').value)), args);
  ui.get('#schedule-plugin-select').value = 'demo';
  ui.inputs.push({ dataset: { param: 'title', type: 'string' }, value });
  ui.syncPluginParamsToArgs();
  assert.deepEqual(Array.from(ui.parseArgs(ui.get('#schedule-args').value)), ['run', 'demo', '--', '--title', value]);
  assert.deepEqual(Array.from(ui.parseArgs("--title '保留单引号包围' --path C:\\Users\\root\\cookie.json")), ['--title', '保留单引号包围', '--path', 'C:\\Users\\root\\cookie.json']);
});
