const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../internal/webui/static/app.js'), 'utf8');
const boot = /\r?\n  boot\(\);\r?\n\}\)\(\);\s*$/;
assert(boot.test(source), 'WebUI entry point changed; update the isolated test harness');
const context = vm.createContext({});
vm.runInContext(source.replace(boot, '\n  globalThis.form = { state, completeMusicPartnerRanges, renderFlowRange };\n})();'), context);
const { state, completeMusicPartnerRanges, renderFlowRange } = context.form;

test('旧策略和固定目标在表单中展开，其他配置保持原值', () => {
  for (const [score, min, max] of [[1, 1, 2], [2, 2, 3], [3, 3, 4], [4, 4, 4]]) {
    const root = { accounts: { main: 'example.json' }, musicPartner: { score, extraLimit: 17, syncYunCircle: false, comments: { '4': ['  保留文本  '] }, waitMin: 21, waitMax: 25 } };
    const expected = structuredClone(root);
    Object.assign(expected.musicPartner, { scoreMin: min, scoreMax: max, extraLimitMin: 17, extraLimitMax: 17 });
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
    assert.deepEqual(root.musicPartner, { ...legacy, scoreMin: 3, scoreMax: 4, extraLimitMin: 7, extraLimitMax: 7 });
  }
});

test('新范围优先，缺少一端或有无效旧值时不静默补齐', () => {
  for (const partner of [
    { score: 4, scoreMin: 1, scoreMax: 5, extraLimit: 17, extraLimitMin: 8, extraLimitMax: 12 },
    { score: 4, scoreMin: 2, extraLimit: 17, extraLimitMax: 8 },
    { score: 4, scoreMax: 5, extraLimit: 17, extraLimitMin: 8 },
    { score: 4, scoreMin: null, extraLimit: 17, extraLimitMax: null },
    { score: 5, extraLimit: 501 },
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
  assert.deepEqual(state.config.data.musicPartner, { score: 4, scoreMin: 2, extraLimitMin: 7, extraLimitMax: 7 });
});
