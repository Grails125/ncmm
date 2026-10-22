const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { setTimeout: delay } = require('node:timers/promises');

function track(child) {
  const processState = { child, result: null };
  processState.done = new Promise(resolve => {
    const finish = result => { processState.result = result; resolve(result); };
    child.once('error', error => finish({ error }));
    child.once('exit', (code, signal) => finish({ code, signal }));
  });
  return processState;
}

async function waitForExit(state, timeoutMs) {
  let timer;
  try {
    return await Promise.race([
      state.done,
      new Promise(resolve => { timer = setTimeout(() => resolve(null), timeoutMs); }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

async function stop(state, timeoutMs) {
  if (!state || state.result) return;
  state.child.kill('SIGTERM');
  if (await waitForExit(state, timeoutMs)) return;
  state.child.kill('SIGKILL');
  if (!await waitForExit(state, 2000)) throw new Error(`WebUI process ${state.child.pid} did not exit`);
}

async function waitForReady(state, home, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let lastError = 'instance metadata is not ready';
  while (Date.now() < deadline) {
    if (state.result) {
      const { error, code, signal } = state.result;
      throw new Error(`WebUI exited before becoming ready: ${error?.message || signal || `code ${code}`}`);
    }
    try {
      const metadata = JSON.parse(await fs.promises.readFile(path.join(home, 'webui.instance.lock'), 'utf8'));
      const address = /^127\.0\.0\.1:([1-9]\d{0,4})$/.exec(metadata.listen || '');
      if (metadata.pid !== state.child.pid || !metadata.instanceId || !address || Number(address[1]) > 65535) {
        throw new Error('instance metadata does not belong to the new loopback WebUI process');
      }
      const baseURL = `http://${metadata.listen}`;
      const response = await fetch(`${baseURL}/api/v1/instance`, {
        signal: AbortSignal.timeout(Math.max(1, Math.min(1000, deadline - Date.now()))),
      });
      if (!response.ok) throw new Error(`instance endpoint returned HTTP ${response.status}`);
      const instance = await response.json();
      if (instance.instanceId !== metadata.instanceId) throw new Error('instance endpoint identifies a different WebUI process');
      if (!state.result) return baseURL;
    } catch (error) {
      lastError = error.message;
    }
    await delay(Math.min(100, Math.max(0, deadline - Date.now())));
  }
  throw new Error(`WebUI did not become ready within ${timeoutMs} ms: ${lastError}`);
}

async function logTail(logPath) {
  const file = await fs.promises.open(logPath, 'r');
  try {
    const { size } = await file.stat();
    const buffer = Buffer.alloc(Math.min(size, 64 * 1024));
    await file.read(buffer, 0, buffer.length, Math.max(0, size - buffer.length));
    return buffer.toString('utf8');
  } finally {
    await file.close();
  }
}

async function runBrowserTest({ binary, testScript, home, prefixArgs = [], startupTimeoutMs = 30000, shutdownTimeoutMs = 10000 }) {
  home = path.resolve(home);
  const logPath = `${home}.log`;
  await fs.promises.mkdir(home, { recursive: true });
  let server;
  let browserTest;
  let failure;
  let notifyInterrupted;
  const interruption = new Promise(resolve => { notifyInterrupted = resolve; });
  const interrupted = signal => {
    failure = Object.assign(new Error(`Browser test interrupted by ${signal}`), { exitCode: signal === 'SIGINT' ? 130 : 143 });
    notifyInterrupted({ interruption: failure });
    browserTest?.child.kill('SIGTERM');
    server?.child.kill('SIGTERM');
  };
  const onInterrupt = () => interrupted('SIGINT');
  const onTerminate = () => interrupted('SIGTERM');
  process.once('SIGINT', onInterrupt);
  process.once('SIGTERM', onTerminate);
  try {
    const logFD = fs.openSync(logPath, 'w');
    try {
      // 由操作系统原子分配空闲端口，避免前序浏览器连接占用下一组测试的固定端口。
      server = track(spawn(binary, [...prefixArgs, '--home', home, 'web', '--listen', '127.0.0.1:0'], {
        stdio: ['ignore', logFD, logFD], windowsHide: true,
      }));
    } finally {
      fs.closeSync(logFD);
    }
    const ready = await Promise.race([
      waitForReady(server, home, startupTimeoutMs).then(baseURL => ({ baseURL })),
      interruption,
    ]);
    if (ready.interruption) throw ready.interruption;
    const { baseURL } = ready;
    console.log(`WebUI ready: ${baseURL} (pid ${server.child.pid})`);
    browserTest = track(spawn(process.execPath, [testScript, baseURL, home], { stdio: 'inherit', windowsHide: true }));
    const completed = await Promise.race([
      browserTest.done.then(result => ({ result })),
      server.done.then(result => ({ serverExit: result })),
      interruption,
    ]);
    if (completed.interruption) throw completed.interruption;
    if (completed.serverExit) throw new Error(`WebUI exited while the browser test was running: ${completed.serverExit.error?.message || completed.serverExit.signal || `code ${completed.serverExit.code}`}`);
    const { result } = completed;
    if (result.error || result.code !== 0) {
      throw Object.assign(new Error(`Browser test failed: ${result.error?.message || result.signal || `code ${result.code}`}`), {
        exitCode: result.code > 0 ? result.code : 1,
      });
    }
    if (server.result) throw new Error('WebUI exited while the browser test was running');
  } catch (error) {
    failure ||= error;
  } finally {
    for (const state of [browserTest, server]) {
      try {
        await stop(state, shutdownTimeoutMs);
      } catch (error) {
        failure ||= error;
      }
    }
    process.removeListener('SIGINT', onInterrupt);
    process.removeListener('SIGTERM', onTerminate);
  }
  if (failure) {
    let output;
    try { output = await logTail(logPath); } catch (error) { output = `Cannot read server log: ${error.message}`; }
    failure.message += `\nWebUI server log (${logPath}, last 64 KiB):\n${output}`;
    throw failure;
  }
}

if (require.main === module) {
  const [binary, testScript, home] = process.argv.slice(2);
  if (!binary || !testScript || !home) {
    console.error('Usage: node tests/run_webui_browser.js BINARY TEST_SCRIPT HOME');
    process.exitCode = 1;
  } else {
    runBrowserTest({ binary, testScript, home }).catch(error => {
      console.error(error.message);
      process.exitCode = error.exitCode || 1;
    });
  }
}

module.exports = { runBrowserTest };
