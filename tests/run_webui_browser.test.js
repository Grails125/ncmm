const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const { runBrowserTest } = require('./run_webui_browser');

const fakeServer = `
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const home = process.argv[process.argv.indexOf('--home') + 1];
const mode = process.argv[2];
fs.writeFileSync(path.join(home, 'server-pid'), String(process.pid));
console.log('fake server startup diagnostic');
if (mode === 'exit') process.exit(17);
if (mode === 'ignore-term' || mode === 'ignore-term-startup') process.on('SIGTERM', () => {});
if (mode === 'timeout' || mode === 'stale' || mode === 'ignore-term-startup') { setInterval(() => {}, 1000); return; }
const server = http.createServer((req, res) => {
  fs.writeFileSync(path.join(home, 'health-request'), req.url);
  if (mode === 'health-timeout') return;
  res.writeHead(200, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify({ instanceId: mode === 'wrong-instance' ? 'other' : 'new-instance' }));
});
server.listen(0, '127.0.0.1', () => {
  fs.writeFileSync(path.join(home, 'webui.instance.lock'), JSON.stringify({
    pid: process.pid, listen: '127.0.0.1:' + server.address().port, instanceId: 'new-instance',
  }));
});
`;

async function fixture(t, mode, browserSource = 'process.exit(0)') {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'ncmm-launcher-'));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const home = path.join(root, 'home');
  await fs.mkdir(home);
  const serverScript = path.join(root, 'server.js');
  const testScript = path.join(root, 'browser.js');
  await fs.writeFile(serverScript, fakeServer);
  await fs.writeFile(testScript, browserSource);
  const startupTimeoutMs = ['timeout', 'stale', 'wrong-instance', 'health-timeout'].includes(mode) ? 1000 : 3000;
  return {
    home, root,
    run: () => runBrowserTest({ binary: process.execPath, testScript, home, prefixArgs: [serverScript, mode], startupTimeoutMs, shutdownTimeoutMs: 500 }),
  };
}

async function assertStopped(home) {
  const pid = Number(await fs.readFile(path.join(home, 'server-pid'), 'utf8'));
  assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' }, 'server must be reaped before the launcher returns');
}

test('使用服务实际分配的端口运行浏览器测试，并等待服务退出', async t => {
  const app = await fixture(t, 'ready', `
    const assert = require('node:assert/strict');
    const fs = require('node:fs');
    const path = require('node:path');
    const [baseURL, home] = process.argv.slice(2);
    const instance = JSON.parse(fs.readFileSync(path.join(home, 'webui.instance.lock')));
    assert.equal(baseURL, 'http://' + instance.listen);
    fs.writeFileSync(path.join(home, 'browser-ran'), baseURL);
  `);
  await app.run();
  assert.match(await fs.readFile(path.join(app.home, 'browser-ran'), 'utf8'), /^http:\/\/127\.0\.0\.1:[1-9]\d+$/);
  await assertStopped(app.home);
});

test('启动提前退出时立即报告退出码和服务日志', async t => {
  const app = await fixture(t, 'exit');
  await assert.rejects(app.run(), error => {
    assert.match(error.message, /exited before becoming ready: code 17/);
    assert.match(error.message, /fake server startup diagnostic/);
    return true;
  });
  await assertStopped(app.home);
});

test('启动超时有界失败并清理进程', async t => {
  const app = await fixture(t, 'timeout');
  await assert.rejects(app.run(), /did not become ready within 1000 ms/);
  await assertStopped(app.home);
});

test('旧实例元数据不能让新进程误报就绪', async t => {
  const app = await fixture(t, 'stale');
  await fs.writeFile(path.join(app.home, 'webui.instance.lock'), JSON.stringify({ pid: process.pid, listen: '127.0.0.1:39134', instanceId: 'old' }));
  await assert.rejects(app.run(), /metadata does not belong to the new loopback WebUI process/);
  await assertStopped(app.home);
});

test('健康响应必须属于元数据标识的实例', async t => {
  const app = await fixture(t, 'wrong-instance');
  await assert.rejects(app.run(), /did not become ready within 1000 ms/);
  assert.equal(await fs.readFile(path.join(app.home, 'health-request'), 'utf8'), '/api/v1/instance');
  await assertStopped(app.home);
});

test('健康请求挂起时仍遵守启动超时并清理进程', async t => {
  const app = await fixture(t, 'health-timeout');
  await assert.rejects(app.run(), /did not become ready within 1000 ms/);
  await assertStopped(app.home);
});

test('服务忽略正常退出信号时仍能有界清理', async t => {
  const app = await fixture(t, 'ignore-term');
  await app.run();
  await assertStopped(app.home);
});

test('保留浏览器测试失败退出码并清理服务', async t => {
  const app = await fixture(t, 'ready', 'process.exit(23)');
  await assert.rejects(app.run(), error => {
    assert.equal(error.exitCode, 23);
    assert.match(error.message, /Browser test failed: code 23/);
    assert.match(error.message, /fake server startup diagnostic/);
    return true;
  });
  await assertStopped(app.home);
});

for (const [stage, signal, exitCode] of [['startup', 'SIGINT', 130], ['browser', 'SIGTERM', 143]]) {
  test(`${stage} 阶段取消时，即使子进程忽略退出信号也能清理并保留 ${exitCode}`, async t => {
    const app = await fixture(t, stage === 'startup' ? 'ignore-term-startup' : 'ignore-term', `
      const fs = require('node:fs');
      const path = require('node:path');
      process.on('SIGTERM', () => {});
      fs.writeFileSync(path.join(process.argv[3], 'browser-pid'), String(process.pid));
      setInterval(() => {}, 1000);
    `);
    const running = app.run().then(() => null, error => error);
    const pidFiles = ['server-pid', ...(stage === 'browser' ? ['browser-pid'] : [])];
    const pids = [];
    let watchdog;
    let forcedByTest = false;
    try {
      for (const name of pidFiles) {
        const deadline = Date.now() + 3000;
        while (true) {
          try {
            const pid = Number(await fs.readFile(path.join(app.home, name), 'utf8'));
            if (!Number.isSafeInteger(pid) || pid <= 1) throw Object.assign(new Error('PID file is not ready'), { code: 'EAGAIN' });
            pids.push(pid);
            break;
          } catch (error) {
            if (!['ENOENT', 'EAGAIN'].includes(error.code) || Date.now() >= deadline) throw error;
            await new Promise(resolve => setTimeout(resolve, 20));
          }
        }
      }
      // 直接派发当前进程的信号事件，兼容 Windows；Linux 子进程实际忽略 SIGTERM。
      watchdog = setTimeout(() => {
        forcedByTest = true;
        for (const pid of pids) { try { process.kill(pid, 'SIGKILL'); } catch {} }
      }, 2500);
      process.emit(signal);
      const error = await running;
      assert.equal(forcedByTest, false, 'cancellation must enter bounded cleanup without the test watchdog');
      assert.equal(error?.exitCode, exitCode);
      assert.match(error.message, new RegExp(`interrupted by ${signal}`));
      for (const pid of pids) assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
    } finally {
      clearTimeout(watchdog);
      for (const pid of pids) { try { process.kill(pid, 'SIGKILL'); } catch {} }
    }
  });
}
