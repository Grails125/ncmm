// 仅本地隔离实例使用。短信端点全部拦截，不发送真实短信、不登录网易云账号。
const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');
const [baseURL, outputDir] = process.argv.slice(2);
const password = 'Release#120A';
const assert = (value, message) => { if (!value) throw new Error(message); };

(async () => {
  fs.mkdirSync(outputDir, { recursive: true });
  const options = { headless: true };
  if (process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH) options.executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH;
  const browser = await chromium.launch(options);
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  let sends = 0, logins = 0, cancels = 0;
  let releaseSend;
  const sendGate = new Promise(resolve => { releaseSend = resolve; });
  await page.route('**/api/v1/accounts/sms/**', async route => {
    const req = route.request(); const url = new URL(req.url());
    if (url.pathname.endsWith('/send')) {
      sends++; const body = req.postDataJSON();
      assert(body.phone === '13800000000' && body.countryCode === 86 && body.filename === 'fan1.json', 'SMS send target mismatch');
      await sendGate;
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: `fixture-${sends}`, retryAfter: 60 }) });
    }
    if (url.pathname.endsWith('/login')) {
      logins++; const body = req.postDataJSON();
      assert(body.id === 'fixture-1' && body.captcha === '123456' && !('phone' in body), 'SMS verify contract mismatch');
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ message: '短信登录成功，账号已保存' }) });
    }
    if (req.method() === 'DELETE') { cancels++; return route.fulfill({ status: 204 }); }
    throw new Error('unexpected SMS route');
  });
  await page.goto(baseURL, { waitUntil: 'networkidle' });
  if (await page.locator('#auth-title').isVisible()) {
    await page.locator('#password-input').fill(password);
    if ((await page.locator('#auth-title').textContent()).includes('设置')) {
      await page.locator('#setup-confirm-password').fill(password); await page.locator('#setup-submit').click();
    } else await page.locator('#login-submit').click();
  }
  await page.locator('#app-shell').waitFor({ state: 'visible' });
  await page.locator('[data-page="accounts"]').click();
  await page.locator('#account-add').click();
  await page.locator('[data-login-method="sms"]').click();
  assert(await page.locator('#account-submit').isDisabled(), 'SMS verify enabled before send');
  await page.locator('#sms-phone').fill('13800000000');
  await page.locator('#sms-send').click();
  assert(await page.locator('#account-filename').isDisabled(), 'pending SMS allowed target filename edits');
  assert(await page.locator('.account-type [data-main="true"]').isDisabled(), 'pending SMS allowed account role changes');
  releaseSend();
  await page.locator('#sms-status').filter({ hasText: '验证码已发送' }).waitFor();
  assert(await page.locator('#sms-send').isDisabled(), 'send cooldown missing');
  assert((await page.locator('#sms-send').textContent()).includes('秒后重发'), 'countdown missing');
  await page.locator('#sms-captcha').fill('123456');
  await page.screenshot({ path: path.join(outputDir, 'sms-desktop.png'), fullPage: true });
  await page.locator('#account-submit').click();
  await page.locator('#account-panel').waitFor({ state: 'hidden' });
  assert(logins === 1, 'SMS login was not submitted once');
  await page.locator('#account-add').click();
  assert((await page.locator('#sms-phone').inputValue()) === '' && (await page.locator('#sms-captcha').inputValue()) === '', 'closed panel retained login inputs');
  await page.locator('#sms-captcha').fill('111111');
  await page.locator('[data-login-method="cookie"]').click();
  assert((await page.locator('#sms-captcha').inputValue()) === '', 'method switch retained captcha');
  assert(await page.locator('#sms-phone').isDisabled(), 'hidden SMS phone blocks Cookie login');
  await page.locator('[data-login-method="sms"]').click();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator('#account-panel').scrollIntoViewIfNeeded();
  await page.screenshot({ path: path.join(outputDir, 'sms-mobile.png'), fullPage: true });
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), 'mobile page overflow');
  assert(!errors.length, `browser errors: ${errors.join('; ')}`);
  fs.writeFileSync(path.join(outputDir, 'sms-browser-report.json'), JSON.stringify({ sends, logins, cancels, pageErrors: errors, realSMS: false }, null, 2));
  await browser.close();
})().catch(error => { console.error(error.message); process.exit(1); });
