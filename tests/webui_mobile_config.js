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
const comments = Array.from({ length: 12 }, (_, i) => `第 ${i + 1} 条：旋律与节奏层次清晰，期待下一首作品。`).join('\n');
const commentField = page => page.locator('textarea[data-path=\'["musicPartner","comments","3"]\']');

async function save(page) {
  const response = page.waitForResponse(r => r.url().endsWith('/api/v1/config') && r.request().method() === 'PUT');
  await page.locator('#config-save').click();
  assert.equal((await response).status(), 200, 'configuration must save successfully');
  await page.locator('#config-dirty').waitFor({ state: 'hidden' });
}

async function noOverflow(page) {
  const sizes = await page.evaluate(() => ({ width: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
  assert(sizes.scroll <= sizes.width + 1, `document overflow: ${JSON.stringify(sizes)}`);
}

async function noClippedCards(page) {
  const clipped = await page.locator('#config-visual').evaluate(container => {
    const frame = container.getBoundingClientRect();
    return [...container.querySelectorAll('.task-card, .config-card-box, .kanban-col, .range-inputs-row, .partner-comment-pools')]
      .filter(element => element.getClientRects().length)
      .filter(element => {
        const bounds = element.getBoundingClientRect();
        return bounds.left < frame.left - 1 || bounds.right > frame.right + 1 || element.scrollWidth > element.clientWidth + 1;
      }).map(element => ({ className: element.className, width: element.clientWidth, scroll: element.scrollWidth, left: element.getBoundingClientRect().left, right: element.getBoundingClientRect().right, frame: { left: frame.left, right: frame.right } }));
  });
  assert.deepEqual(clipped, [], 'configuration cards and range controls must not be clipped');
}

async function selectModule(page, key) {
  const button = page.locator(`#config-sections [data-section="${key}"]`);
  await button.scrollIntoViewIfNeeded();
  await button.click();
  await page.locator(`#config-block-${key}`).waitFor({ state: 'visible' });
  await page.waitForFunction(key => document.querySelector(`#config-sections [data-section="${key}"]`).classList.contains('active'), key);
}

async function assertSaveBar(page) {
  const boxes = await page.locator('#config-save-actions').evaluate(element => {
    const bounds = node => { const r = node.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height, bottom: r.bottom, right: r.right }; };
    return { bar: bounds(element), save: bounds(document.querySelector('#config-save')), reload: bounds(document.querySelector('#config-reload')), badge: bounds(document.querySelector('#config-dirty')), width: innerWidth, height: innerHeight };
  });
  assert(boxes.bar.x >= 0 && boxes.bar.right <= boxes.width && boxes.bar.bottom <= boxes.height, 'save bar must remain in the viewport');
  assert(boxes.bar.height <= 64, 'mobile save bar must remain one compact row');
  assert(Math.abs(boxes.save.y - boxes.reload.y) < 2 && boxes.save.height >= 40, 'save and discard must share a touchable row');
  assert(boxes.badge.right <= boxes.reload.x + 1, 'dirty status must not overlap the buttons');
}

(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage();
    const errors = [];
    const blockedRuns = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/api/v1/schedules/*/run', route => { blockedRuns.push(route.request().url()); return route.abort(); });
    // 开发时仅在隔离实例中覆盖样式；CI 默认验证二进制内嵌资源。
    if (process.env.NCMM_TEST_SOURCE_CSS === 'true') {
      await page.route('**/workspace.css', route => route.fulfill({ contentType: 'text/css', body: fs.readFileSync(path.join(__dirname, '../internal/webui/static/workspace.css'), 'utf8') }));
    }
    await page.goto(baseURL, { waitUntil: 'networkidle' });
    await page.locator('#auth-title').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#auth-title').textContent(), '设置管理员密码', 'use a fresh isolated home');
    await page.locator('#password-input').fill('MobileTest#2026');
    await page.locator('#setup-confirm-password').fill('MobileTest#2026');
    await page.locator('#setup-submit').click();
    await page.locator('#app-shell').waitFor({ state: 'visible' });
    await page.locator('.nav-item[data-page="config"]').click();
    await page.locator('#config-block-task').waitFor({ state: 'visible' });

    for (const width of [1440, 768]) {
      await page.setViewportSize({ width, height: 1000 });
      await noOverflow(page);
      assert(await page.locator('.card-flow-section:visible').count() > 1, 'desktop and tablet must retain the continuous configuration flow');
      await noClippedCards(page);
      await page.screenshot({ path: path.join(outputDir, `config-${width}.png`), animations: 'disabled' });
    }

    for (const width of [390, 320]) {
      await page.setViewportSize({ width, height: 844 });
      await selectModule(page, 'task');
      await page.screenshot({ path: path.join(outputDir, `config-${width}-before-scroll.png`), animations: 'disabled' });
      const scroll = await page.evaluate(() => ({
        html: getComputedStyle(document.documentElement).overflowY,
        workspace: getComputedStyle(document.querySelector('.workspace')).overflowY,
        body: getComputedStyle(document.querySelector('#config-visual')).overflowY,
        kanban: [...document.querySelectorAll('.kanban-col-body')].map(element => getComputedStyle(element).overflowY),
      }));
      assert.equal(scroll.html, 'visible', 'mobile configuration must use document scrolling');
      assert.equal(scroll.workspace, 'visible', 'workspace must not trap vertical scrolling');
      assert.equal(scroll.body, 'visible', 'configuration must not trap vertical scrolling');
      assert(scroll.kanban.every(value => value === 'visible'), 'task queues must not have nested scrolling');
      assert.equal(await page.locator('.card-flow-section:visible').count(), 1, 'mobile shows the selected module directly');
      const modules = await page.locator('#config-sections').evaluate(element => ({ width: element.clientWidth, scroll: element.scrollWidth, items: [...element.children].map(child => child.getBoundingClientRect().width), rows: getComputedStyle(element).gridTemplateRows.split(' ').length }));
      assert(modules.scroll > modules.width && modules.rows === 2, 'module choices must use two horizontally scrollable rows');
      assert(modules.items.every(width => width >= 68 && width <= 85), 'module choices must remain compact');
      await page.locator('#config-sections').evaluate(element => { element.scrollLeft = 0; });
      await page.locator('#config-sections').hover();
      await page.mouse.wheel(250, 0);
      await page.waitForFunction(() => document.querySelector('#config-sections').scrollLeft > 100);
      await noOverflow(page);
      await noClippedCards(page);
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.mouse.move(width / 2, 600);
      await page.mouse.wheel(0, 600);
      await page.waitForFunction(() => document.scrollingElement.scrollTop > 100);
      assert(await page.locator('.kanban-item-name').evaluateAll(elements => elements.filter(element => element.getClientRects().length).every(element => element.clientWidth > 0 && element.scrollWidth <= element.clientWidth + 1)), 'task queue actions must not squeeze away task names');
      await page.locator('.kanban-grid').evaluate(element => element.scrollIntoView({ block: 'start' }));
      await page.screenshot({ path: path.join(outputDir, `task-queues-${width}.png`), animations: 'disabled' });
      await selectModule(page, 'musicPartner');
      assert.equal(await page.locator('.card-flow-section:visible').count(), 1, 'module change must hide the previous module');
      assert.equal(await page.locator('#config-block-musicPartner .range-inputs-row').count(), 5, 'scoring and recommendation ranges remain available');
      assert.equal(await page.locator('#config-block-musicPartner .partner-comment-pools textarea').count(), 5, 'all score comment pools remain available');
      for (const name of ['recommendNotes', 'recommendComments']) assert.equal(await page.locator(`textarea[data-path='["musicPartner","${name}"]']`).count(), 1, 'recommendation template pool missing');
      assert.equal(await page.locator('#config-block-musicPartner .schema-segment').count(), 2, 'recommendation order choices missing');
      await commentField(page).fill(`${comments}\n窄屏 ${width}`);
      await page.locator('#config-dirty').waitFor({ state: 'visible' });
      await assertSaveBar(page);
      await page.screenshot({ path: path.join(outputDir, `music-partner-${width}.png`), animations: 'disabled' });
      await selectModule(page, 'task');
      await selectModule(page, 'musicPartner');
      assert.equal(await commentField(page).inputValue(), `${comments}\n窄屏 ${width}`, 'module switches must retain unsaved comments');
      await save(page);
      await page.reload({ waitUntil: 'networkidle' });
      await selectModule(page, 'musicPartner');
      assert.equal(await commentField(page).inputValue(), `${comments}\n窄屏 ${width}`, 'long comments must persist after reload');
      await noOverflow(page);
    }

    await page.locator('#config-mode [data-mode="yaml"]').click();
    await page.locator('#yaml-editor').waitFor({ state: 'visible' });
    const yaml = await page.locator('#yaml-editor').inputValue();
    assert(yaml.includes('comments:'), 'YAML must edit the selected music partner module');
    await page.locator('#yaml-editor').fill(yaml.replace('窄屏 320', 'YAML 保存验证'));
    await page.locator('#config-dirty').waitFor({ state: 'visible' });
    await assertSaveBar(page);
    await noOverflow(page);
    await page.screenshot({ path: path.join(outputDir, 'music-partner-yaml-320.png'), animations: 'disabled' });
    await save(page);
    await page.locator('#config-mode [data-mode="visual"]').click();
    await selectModule(page, 'musicPartner');
    assert((await commentField(page).inputValue()).includes('YAML 保存验证'), 'YAML save must be reflected in visual comments');
    await page.reload({ waitUntil: 'networkidle' });
    await selectModule(page, 'musicPartner');
    assert((await commentField(page).inputValue()).includes('YAML 保存验证'), 'YAML changes must persist after reload');
    await selectModule(page, 'task');
    await page.locator('#config-search').fill('乐评');
    assert(await page.locator('#config-block-musicPartner').isVisible(), 'search must still find comments');
    await page.locator('#config-search').fill('');

    await page.locator('#menu-button').click();
    await page.locator('.nav-item[data-page="accounts"]').click();
    assert(await page.locator('#config-save-actions').isHidden(), 'save bar must not leak onto another page');
    assert.equal(await page.locator('.workspace').evaluate(element => getComputedStyle(element).overflowY), 'hidden', 'other pages retain their original workspace layout');
    assert.equal(await page.locator('.page-body').evaluate(element => getComputedStyle(element).overflowY), 'auto', 'other pages retain their original content scrolling');
    assert.equal(errors.length, 0, `browser errors: ${errors.join('; ')}`);
    assert.equal(blockedRuns.length, 0, 'test must not trigger real tasks');
    console.log(JSON.stringify({ ok: true, viewports: [1440, 768, 390, 320], commentsAndYaml: true, screenshots: outputDir }));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
