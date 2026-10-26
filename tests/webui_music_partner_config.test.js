const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../internal/webui/static/app.js'), 'utf8');
const boot = /\r?\n  boot\(\);\r?\n\}\)\(\);\s*$/;
assert(boot.test(source), 'WebUI entry point changed; update the isolated test harness');
const effects = [];
const context = vm.createContext({ structuredClone, effects });
vm.runInContext(source.replace(boot, '\n  markConfigDirty = () => { state.configDirty = true; state.configDirtyMode = "visual"; }; renderConfig = () => effects.push("render"); toast = message => effects.push(message);\n  globalThis.form = { state, completeMusicPartnerRanges, renderFlowRange, renderFlowMusicPartner, fillMusicPartnerTemplates };\n})();'), context);
const { state, completeMusicPartnerRanges, renderFlowRange, renderFlowMusicPartner, fillMusicPartnerTemplates } = context.form;

test('旧策略和固定目标在表单中展开，其他配置保持原值', () => {
  for (const [score, min, max] of [[1, 1, 2], [2, 2, 3], [3, 3, 4], [4, 4, 4]]) {
    const root = { accounts: { main: 'example.json' }, musicPartner: { score, extraLimit: 12, syncYunCircle: false, comments: { '4': ['  保留文本  '] }, waitMin: 21, waitMax: 25 } };
    const expected = structuredClone(root);
    Object.assign(expected.musicPartner, { scoreMin: min, scoreMax: max, extraLimitMin: 12, extraLimitMax: 12 });
    completeMusicPartnerRanges(root);
    assert.deepEqual(root, expected);
    completeMusicPartnerRanges(root);
    assert.deepEqual(root, expected, 'repeated rendering must not change a migrated form');
  }
});

test('完全缺省范围使用默认值，不为无合伙人配置的文档新增模块', () => {
  const empty = { accounts: {} };
  completeMusicPartnerRanges(empty);
  assert.deepEqual(empty, { accounts: {} });
  for (const legacy of [{}, { score: 0, extraLimit: 0 }]) {
    const root = { musicPartner: { ...legacy } };
    completeMusicPartnerRanges(root);
    assert.deepEqual(root.musicPartner, { ...legacy, scoreMin: 3, scoreMax: 4, extraLimitMin: 5, extraLimitMax: 15 });
  }
});

test('新范围优先，缺少一端或有无效旧值时不静默补齐', () => {
  for (const partner of [
    { score: 4, scoreMin: 1, scoreMax: 5, extraLimit: 12, extraLimitMin: 8, extraLimitMax: 12 },
    { score: 4, scoreMin: 2, extraLimit: 12, extraLimitMax: 8 },
    { score: 4, scoreMax: 5, extraLimit: 12, extraLimitMin: 8 },
    { score: 4, scoreMin: null, extraLimit: 12, extraLimitMax: null },
    { score: 4, scoreMin: 3, scoreMax: 4, extraLimitMin: 0, extraLimitMax: 0 },
    { score: 4, scoreMin: 3, scoreMax: 4, extraLimitMin: 0, extraLimitMax: 15 },
    { score: 5, extraLimit: 16 },
  ]) {
    const root = { musicPartner: structuredClone(partner) };
    completeMusicPartnerRanges(root);
    assert.deepEqual(root.musicPartner, partner);
  }
});

test('不完整的区间将缺失一端显示为空，避免误显示默认值', () => {
  state.config = { data: { musicPartner: { score: 4, scoreMin: 2 } } };
  state.configSchema = { targets: { config: { fields: [
    { path: 'musicPartner.scoreMin', default: 3, min: 1, max: 5, step: 1 },
    { path: 'musicPartner.scoreMax', default: 4, min: 1, max: 5, step: 1 },
  ] } } };
  completeMusicPartnerRanges(state.config.data);
  const html = renderFlowRange('评分范围', 'musicPartner.scoreMin', 'musicPartner.scoreMax', '分');
  assert.match(html, /value="2" aria-label="评分范围下限"/);
  assert.match(html, /value="" aria-label="评分范围上限"/);
  assert.deepEqual(state.config.data.musicPartner, { score: 4, scoreMin: 2, extraLimitMin: 5, extraLimitMax: 15 });
});

function recommendationSchema() {
  return { targets: { config: { fields: [
    { path: 'musicPartner.recommendCountMin', title: '每日推荐目标下限', type: 'integer', widget: 'number', default: 0, min: 0, step: 1 },
    { path: 'musicPartner.recommendCountMax', title: '每日推荐目标上限', type: 'integer', widget: 'number', default: 0, min: 0, step: 1 },
    { path: 'musicPartner.recommendOrder', title: '推荐歌曲抽取顺序', type: 'string', widget: 'segmented', default: 'sequential', options: [{ value: 'sequential', label: '按列表顺序' }, { value: 'random', label: '随机抽取' }] },
    { path: 'musicPartner.recommendWaitMin', title: '推荐等待下限', type: 'integer', widget: 'number', default: 16, min: 16, max: 300, step: 1 },
    { path: 'musicPartner.recommendWaitMax', title: '推荐等待上限', type: 'integer', widget: 'number', default: 20, min: 16, max: 300, step: 1 },
    { path: 'musicPartner.recommendNotes', title: '推荐笔记模板', description: '每行一条，支持 {name} 歌曲名、{artist} 作者名；启用时必填，满 7 天后在下次任务中自动删除。', type: 'string-list', widget: 'textarea-list', default: [] },
    { path: 'musicPartner.recommendComments', title: '推荐乐评模板', description: '每行一条，支持 {name} 歌曲名、{artist} 作者名；启用时必填。', type: 'string-list', widget: 'textarea-list', default: [] },
  ] } } };
}

test('推荐任务默认关闭且独立展示模板，不改变已有评分配置', () => {
  state.configSchema = recommendationSchema();
  const partner = { scoreMin: 3, scoreMax: 4, extraLimitMin: 0, extraLimitMax: 15, comments: { '3': ['已有评分乐评'] } };
  state.config = { data: { musicPartner: structuredClone(partner) } };
  const html = renderFlowMusicPartner();
  assert.match(html, /今日歌曲推荐/);
  assert.match(html, /value="0" aria-label="每日推荐目标下限" min="0" step="1"/);
  assert.match(html, /value="0" aria-label="每日推荐目标上限" min="0" step="1"/);
  assert.match(html, /class="schema-segment active"[^>]*>按列表顺序<\/button>/);
  assert.match(html, /奖励按平台结算/);
  assert.match(html, /value="16" aria-label="推荐等待时间下限" min="16" max="300"/);
  assert.match(html, /value="20" aria-label="推荐等待时间上限" min="16" max="300"/);
  assert.match(html, /aria-label="推荐笔记模板"/);
  assert.match(html, /aria-label="推荐乐评模板"/);
  assert.match(html, /满 7 天后在下次任务中自动删除/);
  assert.match(html, /服务器返回的笔记、乐评状态和积分变化/);
  assert.deepEqual(state.config.data.musicPartner, partner, 'rendering defaults must not enable recommendation or modify scoring');
});

test('推荐模板保留占位符和正文并转义HTML，缺失等待上限保持可见', () => {
  state.configSchema = recommendationSchema();
  const partner = {
    scoreMin: 3, scoreMax: 4, extraLimitMin: 5, extraLimitMax: 15,
    recommendCount: 2, recommendWaitMin: 18,
    recommendNotes: ['分享《{name}》—{artist}', '</textarea><script>bad()</script>'],
    recommendComments: ['  喜欢《{name}》  '],
  };
  state.config = { data: { musicPartner: structuredClone(partner) } };
  const html = renderFlowMusicPartner();
  assert.match(html, /value="2" aria-label="每日推荐目标下限"/);
  assert.match(html, /value="2" aria-label="每日推荐目标上限"/);
  assert.match(html, /value="18" aria-label="推荐等待时间下限"/);
  assert.match(html, /value="" aria-label="推荐等待时间上限"/);
  assert.match(html, /分享《\{name\}》—\{artist\}/);
  assert.match(html, /&lt;\/textarea&gt;&lt;script&gt;bad\(\)&lt;\/script&gt;/);
  assert.match(html, />  喜欢《\{name\}》  <\/textarea>/);
  assert.doesNotMatch(html, /<script>/);
  assert.deepEqual(state.config.data.musicPartner, { ...partner, recommendCountMin: 2, recommendCountMax: 2 });
});

test('推荐目标允许5至10，支持随机抽取且不带三首上限', () => {
  state.configSchema = recommendationSchema();
  const partner = { scoreMin: 3, scoreMax: 4, extraLimitMin: 5, extraLimitMax: 15, recommendCountMin: 5, recommendCountMax: 10, recommendOrder: 'random' };
  state.config = { data: { musicPartner: structuredClone(partner) } };
  const html = renderFlowMusicPartner();
  assert.match(html, /value="5" aria-label="每日推荐目标下限"/);
  assert.match(html, /value="10" aria-label="每日推荐目标上限"/);
  for (const input of html.match(/<input[^>]*aria-label="每日推荐目标[上下]限"[^>]*>/g)) {
    assert.doesNotMatch(input, /\smax=/);
  }
  assert.match(html, /class="schema-segment active"[^>]*>随机抽取<\/button>/);
  assert.deepEqual(state.config.data.musicPartner, partner);
});

test('旧推荐数量只在完整新范围缺省时迁移，显式关闭和不完整区间保留', () => {
  const base = { scoreMin: 3, scoreMax: 4, extraLimitMin: 5, extraLimitMax: 15 };
  for (const value of [0, 3, 5, 10]) {
    const root = { musicPartner: { ...base, recommendCount: value } };
    completeMusicPartnerRanges(root);
    assert.deepEqual(root.musicPartner, { ...base, recommendCount: value, recommendCountMin: value, recommendCountMax: value });
    completeMusicPartnerRanges(root);
    assert.equal(root.musicPartner.recommendCountMax, value);
  }
  for (const fields of [
    { recommendCount: 3, recommendCountMin: 0, recommendCountMax: 0 },
    { recommendCount: 3, recommendCountMin: 5, recommendCountMax: 10 },
    { recommendCount: 3, recommendCountMin: 0 },
    { recommendCount: 3, recommendCountMax: 0 },
    { recommendCount: 3, recommendCountMin: null },
    { recommendCount: -1 }, { recommendCount: 1.5 }, { recommendCount: null },
  ]) {
    const root = { musicPartner: { ...base, ...fields } };
    const expected = structuredClone(root);
    completeMusicPartnerRanges(root);
    assert.deepEqual(root, expected);
  }
  state.configSchema = recommendationSchema();
  state.config = { data: { musicPartner: { ...base, recommendCount: 3, recommendCountMin: 0 } } };
  const html = renderFlowMusicPartner();
  assert.match(html, /value="" aria-label="每日推荐目标上限"/);
});

function templateDefaultsSchema() {
  return { targets: { config: { fields: [
    ...[1, 2, 3, 4, 5].map(score => ({ path: `musicPartner.comments.${score}`, title: `${score} 分乐评`, widget: 'textarea-list', group: 'partner-comments', default: [`默认${score}分乐评`] })),
    { path: 'musicPartner.recommendNotes', group: 'partner-recommend', default: ['默认笔记 {name}', '{artist} 的作品'] },
    { path: 'musicPartner.recommendComments', group: 'partner-recommend', default: ['默认推荐乐评'] },
    { path: 'musicPartner.recommendCountMax', group: 'partner-recommend', default: 0 },
  ] } } };
}

test('旧配置缺失的评分档位继续显示空白，默认模板需主动填入', () => {
  state.configTarget = 'config';
  state.configSchema = templateDefaultsSchema();
  state.config = { data: { musicPartner: { scoreMin: 3, scoreMax: 4, extraLimitMin: 5, extraLimitMax: 15, comments: { '4': ['  保留评分乐评  '] } } } };
  const before = structuredClone(state.config.data);
  const html = renderFlowMusicPartner();
  assert.doesNotMatch(html, /默认[1-5]分乐评/);
  assert.match(html, /aria-label="1 分乐评"><\/textarea>/);
  assert.match(html, /aria-label="4 分乐评">  保留评分乐评  <\/textarea>/);
  assert.match(html, /data-partner-templates="partner-comments"/);
  assert.match(html, /data-partner-templates="partner-recommend"/);
  assert.deepEqual(state.config.data, before);
});

test('填入默认模板分别补空池，保留自定义原文与任务范围并等待保存', () => {
  state.configTarget = 'config';
  state.configSchema = templateDefaultsSchema();
  state.configDirty = false;
  state.configDirtyMode = '';
  const partner = {
    scoreMin: 2, scoreMax: 4, recommendCountMin: 0, recommendCountMax: 0,
    enableMain: false, syncYunCircle: false,
    comments: { '1': [], '2': ['  保留评分乐评  '], '4': ['\t'] },
    recommendNotes: [], recommendComments: ['  保留推荐乐评  '],
  };
  state.config = { data: { task: { 'music-partner': false }, musicPartner: structuredClone(partner) } };
  effects.length = 0;
  fillMusicPartnerTemplates('partner-comments');
  const expected = { ...partner, comments: { '1': ['默认1分乐评'], '2': partner.comments['2'], '3': ['默认3分乐评'], '4': ['默认4分乐评'], '5': ['默认5分乐评'] } };
  assert.deepEqual(JSON.parse(JSON.stringify(state.config.data.musicPartner)), expected);
  assert.equal(state.configDirty, true);
  assert.equal(state.configDirtyMode, 'visual');
  assert.match(effects.at(-1), /4.*保存/);
  fillMusicPartnerTemplates('partner-recommend');
  expected.recommendNotes = ['默认笔记 {name}', '{artist} 的作品'];
  assert.deepEqual(JSON.parse(JSON.stringify(state.config.data.musicPartner)), expected);
  assert.equal(state.config.data.task['music-partner'], false);
  assert.match(effects.at(-1), /1.*保存/);
  state.config.data.musicPartner.recommendNotes.push('新自定义模板');
  assert.equal(state.configSchema.targets.config.fields[5].default.length, 2, '填写后编辑不能污染 schema 默认值');
});

test('没有空模板时不标记修改，也不处理通知配置或未知模板组', () => {
  state.configTarget = 'config';
  state.configSchema = templateDefaultsSchema();
  state.config = { data: { musicPartner: { recommendNotes: ['已有笔记'], recommendComments: ['已有乐评'] } } };
  state.configDirty = false;
  effects.length = 0;
  fillMusicPartnerTemplates('partner-recommend');
  assert.equal(state.configDirty, false);
  assert.match(effects.at(-1), /没有.*空白/);
  fillMusicPartnerTemplates('unknown');
  assert.equal(state.configDirty, false);
  state.configTarget = 'notify';
  fillMusicPartnerTemplates('partner-comments');
  assert.equal(state.configDirty, false);
  state.configTarget = 'config';
});
