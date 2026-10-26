const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const [baseURL, testRoot] = process.argv.slice(2);
if (!baseURL || !testRoot || !['127.0.0.1', 'localhost'].includes(new URL(baseURL).hostname)) {
  throw new Error('Provide a local, isolated WebUI URL and test output directory');
}
const outputDir = path.join(testRoot, 'screenshots');
fs.mkdirSync(outputDir, { recursive: true });
const input = (page, keys) => page.locator(`[data-path='${JSON.stringify(keys)}']`);

async function saveConfig(page) {
  const response = page.waitForResponse(r => r.url().endsWith('/api/v1/config') && r.request().method() === 'PUT');
  await page.locator('#config-save').click();
  const result = await response;
  assert.equal(result.status(), 200, await result.text());
  await page.waitForFunction(() => !document.querySelector('#config-save').disabled);
  return result.request().postDataJSON().data;
}

(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const errors = [];
  const blockedRuns = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.route('**/api/v1/schedules/*/run', route => {
    blockedRuns.push(route.request().url());
    return route.abort();
  });
  try {
    await page.goto(baseURL, { waitUntil: 'networkidle' });
    await page.locator('#auth-title').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#auth-title').textContent(), '设置管理员密码', 'use a fresh isolated home');
    await page.locator('#password-input').fill('TaskConfig#2026');
    await page.locator('#setup-confirm-password').fill('TaskConfig#2026');
    await page.locator('#setup-submit').click();
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    await page.locator('.nav-item[data-page="config"]').click();
    await page.locator('#config-block-task').waitFor({ state: 'visible' });
    assert.equal(await page.locator('[data-task-card="task.fansgroup"] input').isChecked(), false);

    await page.locator('#config-sections [data-section="fansgroup"]').click();
    assert.equal(await input(page, ['fansgroup', 'webSession']).isChecked(), false);
    await input(page, ['fansgroup', 'webSession']).check();
    const commentTokens = page.locator('.schema-map[data-schema-path=\'["fansgroup","commentCheckTokens"]\']');
    await commentTokens.locator('.schema-map-add').click();
    await commentTokens.locator('.schema-map-key').fill('test-account.json');
    assert.equal(await commentTokens.locator('.schema-map-value').getAttribute('type'), 'password');
    await commentTokens.locator('.schema-map-value').fill('test-web-check-token');
    const fansSaved = await saveConfig(page);
    assert.equal(fansSaved.fansgroup.webSession, true);
    assert.deepEqual(fansSaved.fansgroup.commentCheckTokens, { 'test-account.json': 'test-web-check-token' });

    await page.locator('#config-sections [data-section="sign"]').click();
    assert.equal(await input(page, ['sign', 'enableKoiWish']).isChecked(), false);
    await input(page, ['sign', 'enableKoiWish']).check();
    const wishSaved = await saveConfig(page);
    assert.equal(wishSaved.sign.enableKoiWish, true);
    await page.reload({ waitUntil: 'networkidle' });
    await page.locator('#config-sections [data-section="sign"]').click();
    await input(page, ['sign', 'enableKoiWish']).waitFor({ state: 'visible' });
    assert.equal(await input(page, ['sign', 'enableKoiWish']).isChecked(), true, 'koi wish switch was not persisted');
    await input(page, ['sign', 'enableKoiWish']).scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(outputDir, 'koi-wish-sign-config.png'), fullPage: true });
    await page.locator('#config-sections [data-section="task"]').click();
    assert.ok((await page.locator('#task-queue-wrapper').innerText()).includes('锦鲤免费许愿'), 'enabled wish was not added to the queue');
    await page.locator('#config-sections [data-section="sign"]').click();
    await input(page, ['sign', 'enableKoiWish']).uncheck();
    assert.equal((await saveConfig(page)).sign.enableKoiWish, false);

    await page.locator('#config-sections [data-section="musicPartner"]').click();
    await input(page, ['musicPartner', 'scoreMin']).fill('2');
    await input(page, ['musicPartner', 'comments', '4']).fill('保留我的评分乐评\n第二条乐评');
    const seeded = await saveConfig(page);
    for (const preset of ['novice', 'musician', 'stealth', 'sign_only']) {
      await page.locator(`[data-preset="${preset}"]`).click();
      const saved = await saveConfig(page);
      assert.equal(saved.task.fansgroup, false, preset);
      assert.equal(saved.fansgroup.webSession, true, `${preset} changed explicit fansgroup session choice`);
      assert.deepEqual(saved.fansgroup.commentCheckTokens, fansSaved.fansgroup.commentCheckTokens);
      assert.deepEqual(saved.musicPartner, seeded.musicPartner, `${preset} changed partner configuration`);
      if (preset === 'musician') {
        assert.equal(saved.task.note, false);
        assert.equal(saved.task['daily-song-share'], false);
        assert.equal(saved.musician.enableVipNote, true);
      }
      if (preset === 'sign_only') assert.equal(saved.task['vip-member-gift'], true);
    }

    await page.locator('#config-sections [data-section="playids"]').click();
    const ids = '123，456；789 # 中文分隔与注释\n101 // 第二行';
    const playlists = ['123456', 'https://music.163.com/#/playlist?id=987654'];
    await input(page, ['playids', 'ids']).fill(ids);
    await input(page, ['playids', 'playlistIds']).fill(playlists.join('\n'));
    const saved = await saveConfig(page);
    assert.equal(saved.playids.ids, ids);
    assert.deepEqual(saved.playids.playlistIds, playlists);
    await page.reload({ waitUntil: 'networkidle' });
    await page.locator('#config-sections [data-section="playids"]').click();
    assert.equal(await input(page, ['playids', 'ids']).inputValue(), ids);
    assert.equal(await input(page, ['playids', 'playlistIds']).inputValue(), playlists.join('\n'));
    assert.equal(await input(page, ['musicPartner', 'comments', '4']).inputValue(), '保留我的评分乐评\n第二条乐评');
    await page.locator('#config-block-playids').screenshot({ path: path.join(outputDir, 'playlist-inputs.png') });

    await page.locator('.nav-item[data-page="schedules"]').click();
    await page.locator('#schedule-add').click();
    assert.equal(await page.locator('#schedule-random-delay').inputValue(), '0', 'new rules must default to no delay');
    await page.locator('#schedule-name').fill('随机延迟保存验证');
    await page.locator('#schedule-dialog label').filter({ has: page.locator('#schedule-enabled') }).click();
    await page.locator('#schedule-command').selectOption('music-partner');
    // 稀疏时间且后续仍保持禁用；隔离工作区没有真实账号。
    await page.locator('#schedule-cron').fill('0 0 1 1 *');
    await page.locator('#schedule-random-delay').fill('60');
    const createResponse = page.waitForResponse(r => r.url().endsWith('/api/v1/schedules') && r.request().method() === 'POST');
    await page.locator('#schedule-submit').click();
    const created = await createResponse;
    assert.equal(created.status(), 201, await created.text());
    assert.equal(created.request().postDataJSON().randomDelaySeconds, 60);
    await page.locator('#schedule-dialog').waitFor({ state: 'hidden' });
    const row = page.locator('#schedule-table tr').filter({ hasText: '随机延迟保存验证' });
    assert.match(await row.innerText(), /0～60 秒/);
    await row.locator('[data-action="edit"]').click();
    assert.equal(await page.locator('#schedule-random-delay').inputValue(), '60');
    await page.locator('#schedule-random-delay').fill('86401');
    assert.equal(await page.locator('#schedule-random-delay').evaluate(el => el.checkValidity()), false);
    await page.locator('#schedule-random-delay').fill('90');
    const updateResponse = page.waitForResponse(r => /\/api\/v1\/schedules\/[^/]+$/.test(new URL(r.url()).pathname) && r.request().method() === 'PUT');
    await page.locator('#schedule-submit').click();
    assert.equal((await updateResponse).status(), 200);
    await page.locator('#schedule-dialog').waitFor({ state: 'hidden' });
    // 拦截启用请求并保留禁用状态，只核对发出的完整规则，不注册真实自动任务。
    let toggleData;
    await page.route('**/api/v1/schedules/*', async route => {
      if (route.request().method() !== 'PUT') return route.continue();
      toggleData = route.request().postDataJSON();
      return route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: '测试阻止启用' }) });
    });
    const toggleResponse = page.waitForResponse(r => r.request().method() === 'PUT' && r.status() === 400);
    await row.locator('[data-action="toggle"]').click();
    await toggleResponse;
    assert.equal(toggleData.randomDelaySeconds, 90, 'toggle omitted saved delay');
    await page.reload({ waitUntil: 'networkidle' });
    assert.match(await row.innerText(), /0～90 秒/);
    assert.match(await row.innerText(), /已禁用/);
    await page.screenshot({ path: path.join(outputDir, 'schedule-random-delay.png'), fullPage: true });
    assert.deepEqual(errors, []);
    assert.deepEqual(blockedRuns, []);
    console.log(JSON.stringify({ passed: true, checks: ['fansgroup session default/save and masked independent comment credentials', 'koi wish opt-in save/reload and queue display', 'safe defaults and four presets', 'partner configuration preserved', 'playlist and commented IDs save/reload', 'delay create/edit/reload', 'invalid delay form validation', 'toggle preserves delay'], screenshots: outputDir }));
  } catch (error) {
    await page.screenshot({ path: path.join(outputDir, 'failure.png'), fullPage: true }).catch(() => {});
    throw error;
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
