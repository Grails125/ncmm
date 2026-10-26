const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../internal/webui/static/app.js'), 'utf8');
const boot = /\r?\n  boot\(\);\r?\n\}\)\(\);\s*$/;
assert(boot.test(source), 'WebUI entry point changed; update the test harness');

function harness() {
  const nodes = new Map();
  const document = { querySelector(selector) {
    if (!nodes.has(selector)) nodes.set(selector, { innerHTML: '', style: {} });
    return nodes.get(selector);
  } };
  const context = vm.createContext({ document, structuredClone });
  vm.runInContext(source.replace(boot, `
    renderConfig = () => {};
    toast = () => {};
    markConfigDirty = () => { state.configDirty = true; };
    globalThis.ui = { state, PRESET_STRATEGIES, applyPreset, renderSchedules, renderRuns, configSectionKeys, configSectionOrder, getTaskQueueLists, getTaskSubtaskInfo, getMissingTaskSubtasks, addTaskToQueue, renderFlowTaskKanban, renderFlowTask, setSchemaValue };
  })();`), context);
  return { ...context.ui, nodes };
}

test('分享歌曲出现在默认队列和任务名称映射中，读取旧队列不擅自增加任务', () => {
  const ui = harness();
  ui.state.config = { data: {} };
  assert(ui.getTaskQueueLists().fastTasks.includes('ShareSong'));
  assert.equal(ui.getTaskSubtaskInfo('ShareSong').name, '分享歌曲');
  ui.state.config.data.task = { fast_tasks: ['VipTask'], slow_tasks: ['ListenIndie'] };
  const queues = ui.getTaskQueueLists();
  assert.deepEqual(Array.from(queues.fastTasks), ['VipTask']);
  assert.deepEqual(Array.from(queues.slowTasks), ['ListenIndie']);
});

test('启用分享歌曲的旧队列展示自动补齐结果，保留自定义顺序与慢队列位置', () => {
  const ui = harness();
  ui.state.config = { data: {
    task: { fast_tasks: ['VipTask', 'custom-extension'], slow_tasks: ['music-partner'] },
    sign: { yunbeiTask: { enableShareSong: true } },
  } };
  assert.deepEqual(Array.from(ui.getTaskQueueLists().fastTasks), ['VipTask', 'custom-extension', 'ShareSong']);
  assert.deepEqual(Array.from(ui.getTaskQueueLists().slowTasks), ['music-partner']);
  ui.state.config.data.task.slow_tasks.push('sharesong');
  assert.deepEqual(Array.from(ui.getTaskQueueLists().fastTasks), ['VipTask', 'custom-extension']);
  assert.deepEqual(Array.from(ui.getTaskQueueLists().slowTasks), ['music-partner', 'sharesong']);
});

test('锦鲤免费许愿仅在开启且队列缺失时追加，保留原有位置和开关', () => {
  const ui = harness();
  ui.state.config = { data: {
    task: { fast_tasks: ['VipTask', 'custom-extension'], slow_tasks: ['ListenIndie'] },
    sign: { enableKoiWish: false },
  } };
  assert.equal(ui.getTaskSubtaskInfo('KoiWish').name, '锦鲤免费许愿');
  assert.deepEqual(Array.from(ui.getTaskQueueLists().fastTasks), ['VipTask', 'custom-extension']);
  ui.state.config.data.sign.enableKoiWish = true;
  assert.deepEqual(Array.from(ui.getTaskQueueLists().fastTasks), ['VipTask', 'custom-extension', 'KoiWish']);
  assert.deepEqual(ui.state.config.data.task.fast_tasks, ['VipTask', 'custom-extension']);
  ui.state.config.data.task.slow_tasks.unshift('koiwish');
  assert.deepEqual(Array.from(ui.getTaskQueueLists().fastTasks), ['VipTask', 'custom-extension']);
  assert.deepEqual(Array.from(ui.getTaskQueueLists().slowTasks), ['koiwish', 'ListenIndie']);
  assert.equal(ui.addTaskToQueue('KoiWish', 'fast'), false);
  assert.equal(ui.state.config.data.sign.enableKoiWish, true);
});

test('锦鲤免费许愿可主动加入队列但不会开启许愿或自动任务', () => {
  const ui = harness();
  ui.state.config = { data: { task: { fast_tasks: ['VipTask'], slow_tasks: ['ListenIndie'] }, sign: { automatic: false, enableKoiWish: false } } };
  assert.equal(ui.addTaskToQueue('koiwish', 'slow'), true);
  assert.deepEqual(Array.from(ui.getTaskQueueLists().slowTasks), ['ListenIndie', 'KoiWish']);
  assert.deepEqual(ui.state.config.data.sign, { automatic: false, enableKoiWish: false });
  assert.equal(ui.state.configDirty, true);
});

test('两种调度模式均展示任务队列，切换为单账号模式不会隐藏队列', () => {
  const ui = harness();
  ui.state.config = { data: { task: {} } };
  for (const mode of ['by-task-group', 'by-account']) {
    ui.setSchemaValue(['task', 'mode'], mode, false);
    const html = ui.renderFlowTask();
    assert.match(html, /data-task-item="ShareSong"/);
    assert.match(html, /data-task-item="music-partner"/);
    assert.doesNotMatch(html, /task-queue-wrapper hidden|display:\s*none/);
    assert.notEqual(ui.nodes.get('#task-queue-wrapper')?.style.display, 'none');
    assert.match(html, /关闭.*分享歌曲.*开关/);
  }
});

test('添加任务只展示两个队列均未包含的已知任务，兼容旧队列大小写', () => {
  const ui = harness();
  const missing = ui.getMissingTaskSubtasks(['viptask', 'custom-extension'], ['sharesong']);
  assert(!missing.some(task => ['VipTask', 'ShareSong', 'custom-extension'].includes(task.id)));
  assert(missing.some(task => task.id === 'ViewVipCenter'));
  const html = ui.renderFlowTaskKanban(['VipTask'], ['ListenIndie']);
  assert.match(html, /<label[^>]+for="task-queue-add"/);
  assert.match(html, /<option value="ShareSong">分享歌曲<\/option>/);
  assert.doesNotMatch(html, /<option value="(?:VipTask|ListenIndie)">/);
  assert.match(html, /data-task-add="fast"/);
  assert.match(html, /data-task-add="slow"/);
});

test('用户明确添加才追加到指定队列，不改变开关、现有顺序和音乐合伙人配置', () => {
  for (const group of ['fast', 'slow']) {
    const ui = harness();
    ui.state.config = { data: {
      task: { sign: false, fast_tasks: ['VipTask', 'custom-extension'], slow_tasks: ['ListenIndie'] },
      sign: { yunbeiTask: { enableShareSong: false } },
      musicPartner: { scoreMin: 2, scoreMax: 5 },
    } };
    assert.equal(ui.addTaskToQueue('sharesong', group), true);
    const root = ui.state.config.data;
    assert.deepEqual(Array.from(root.task.fast_tasks), group === 'fast' ? ['VipTask', 'custom-extension', 'ShareSong'] : ['VipTask', 'custom-extension']);
    assert.deepEqual(Array.from(root.task.slow_tasks), group === 'slow' ? ['ListenIndie', 'ShareSong'] : ['ListenIndie']);
    assert.equal(root.task.sign, false);
    assert.equal(root.sign.yunbeiTask.enableShareSong, false);
    assert.deepEqual(root.musicPartner, { scoreMin: 2, scoreMax: 5 });
    assert.equal(ui.state.configDirty, true);
    assert.equal(ui.addTaskToQueue('ShareSong', group === 'fast' ? 'slow' : 'fast'), false);
    assert.equal(ui.addTaskToQueue('unrecognized-task', 'fast'), false);
    assert.equal(ui.addTaskToQueue('ViewVipCenter', 'invalid'), false);
    assert.equal(ui.getTaskQueueLists().fastTasks.filter(x => x === 'ShareSong').length + ui.getTaskQueueLists().slowTasks.filter(x => x === 'ShareSong').length, 1);
  }
});

test('废弃通用播放字段归入播放模块且不增加导航模块，保留原始兼容数据和其他扩展配置', () => {
  const { configSectionKeys, configSectionOrder } = harness();
  const root = { version: '1.2.9', play: { webSession: false }, ...Object.fromEntries(configSectionOrder.map(key => [key, {}])) };
  const schema = { categories: configSectionOrder.map(id => ({ id })) };
  const original = structuredClone(root);
  assert.deepEqual(Array.from(configSectionKeys(root, schema, 'config')), Array.from(configSectionOrder));
  assert.deepEqual(root, original);
  root.customExtension = { enabled: true };
  assert.equal(configSectionKeys(root, schema, 'config').at(-1), 'customExtension');
  assert.deepEqual(Array.from(configSectionKeys({ version: {}, play: {} }, {}, 'notify')), ['version', 'play']);
});

test('四种日常预设关闭乐迷团，音乐人冲刺不额外开启公共笔记和推歌', () => {
  const { PRESET_STRATEGIES: presets } = harness();
  for (const key of ['novice', 'musician', 'stealth', 'sign_only']) {
    assert.equal(presets[key].config.task.fansgroup, false, key);
  }
  assert.equal(presets.musician.config.task.note, false);
  assert.equal(presets.musician.config.task['daily-song-share'], false);
  assert.equal(presets.musician.config.musician.enableVipNote, true);
  assert.equal(presets.sign_only.config.task['vip-member-gift'], true);
});

test('推荐预设采用新版默认任务和播放目标，关闭需额外凭据的每日推歌', () => {
  const preset = harness().PRESET_STRATEGIES.novice.config;
  assert.equal(preset.task['musician-vip'], true);
  assert.equal(preset.task['daily-song-share'], false);
  assert.equal(preset.sign.enableSecondaries, false);
  assert.equal(preset.fansgroup.enableSecondaries, false);
  assert.equal(preset.vipMemberGift.enableSecondaries, true);
  assert.equal(preset.playids.daily_min, 80);
  assert.equal(preset.playids.daily_max, 200);
  assert.equal(preset.playids.run_min, 40);
  assert.equal(preset.playids.run_max, 80);
  assert.equal(preset.mixPlay.countTarget, false);
});

test('预设仅在主动应用时改变任务开关，不覆盖账号、歌单源和合伙人规则', () => {
  const ui = harness();
  const original = {
    accounts: { main: './my-cookie.json' },
    task: { fansgroup: true },
    playids: { playlistIds: ['123456'], ids: '12，34' },
    musicPartner: { scoreMin: 2, scoreMax: 5, extraLimitMin: 9, extraLimitMax: 12, waitMin: 25, waitMax: 35, comments: { '4': ['保留我的乐评'] } },
  };
  ui.state.config = { data: structuredClone(original) };
  assert.equal(ui.state.config.data.task.fansgroup, true);
  for (const key of ['novice', 'musician', 'stealth', 'sign_only']) {
    ui.applyPreset(key);
    assert.deepEqual(ui.state.config.data.accounts, original.accounts);
    assert.deepEqual(ui.state.config.data.musicPartner, original.musicPartner);
    assert.deepEqual(ui.state.config.data.playids.playlistIds, original.playids.playlistIds);
    assert.equal(ui.state.config.data.playids.ids, original.playids.ids);
    assert.equal(ui.state.configDirty, true);
  }
  ui.applyPreset('music_partner');
  assert.equal(ui.state.config.data.task['music-partner'], true);
  assert.deepEqual(ui.state.config.data.musicPartner.comments, original.musicPartner.comments);
});

test('延迟中的定时任务显示可执行时间并提供停止入口，到时后仍显示排队', () => {
  const ui = harness();
  ui.state.schedules = [{ id: 'delayed', name: '延迟评分', command: 'music-partner', cron: '0 8 * * *', enabled: true, queued: 1, randomDelaySeconds: 60 }];
  ui.state.runs = [{ id: 'run-delayed', jobId: 'delayed', jobName: '延迟评分', command: 'music-partner', status: 'queued', triggeredAt: new Date().toISOString(), delaySeconds: 60, notBefore: new Date(Date.now() + 60000).toISOString() }];
  ui.renderSchedules();
  const scheduleHTML = ui.nodes.get('#schedule-table').innerHTML;
  assert.match(scheduleHTML, /延迟中/);
  assert.match(scheduleHTML, /data-action="stop"/);
  assert.match(scheduleHTML, /0[～–—~-]60 秒/);
  ui.renderRuns();
  assert.match(ui.nodes.get('#runs-table').innerHTML, /延迟中/);
  assert.match(ui.nodes.get('#runs-table').innerHTML, /最早/);
  ui.state.runs[0].notBefore = new Date(Date.now() - 1000).toISOString();
  ui.renderRuns();
  assert.doesNotMatch(ui.nodes.get('#runs-table').innerHTML, /延迟中/);
  assert.match(ui.nodes.get('#runs-table').innerHTML, /排队中/);
});
