/* Optional browser acceptance test. Uses an existing Playwright installation.
 * Run against an isolated offline server, never a live production deployment. */
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs');

(async () => {
  const base = process.env.INCIDENTAI_TEST_URL || 'http://127.0.0.1:8100';
  const output = path.resolve('data/ui-verification');
  fs.mkdirSync(output, { recursive: true });
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_EXECUTABLE_PATH || undefined });
  try {
    const page = await browser.newPage({ viewport: { width: 1500, height: 1000 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(base);
    const status = await page.request.get(base + '/api/v2/status').then(r => r.json());
    assert.equal(status.mode, 'offline', 'acceptance test requires isolated offline mode');
    await page.locator('#new-incident').click();
    await page.locator('#scenario-select').selectOption('deployment-regression');
    await page.locator('#incident-title').fill('浏览器验收 · Checkout 错误率升高');
    const createdPromise = page.waitForResponse(r => r.url() === base + '/api/v2/incidents' && r.request().method() === 'POST');
    await page.locator('#submit-create').click();
    const created = await (await createdPromise).json();
    await page.getByRole('button', { name: '开始调查 ↗' }).click();
    await page.getByRole('button', { name: '批准并执行恢复' }).waitFor({ timeout: 30000 });
    const investigated = await page.request.get(base + '/api/v2/incidents/' + created.id).then(r => r.json());
    assert.equal(investigated.status, 'approval_required');
    assert.equal(investigated.hypotheses[0].type, 'deployment-regression');
    await page.screenshot({ path: path.join(output, 'investigation.png'), fullPage: true });
    const approvedPromise = page.waitForResponse(r => r.url().endsWith('/approve'));
    await page.getByRole('button', { name: '批准并执行恢复' }).click();
    const resolved = await (await approvedPromise).json();
    assert.equal(resolved.status, 'resolved');
    assert.ok(resolved.postmortem.evidence.length > 0);
    await page.getByRole('button', { name: '保存审阅' }).waitFor();
    await page.getByLabel('经验质量状态').selectOption('confirmed');
    const reviewedPromise = page.waitForResponse(r => r.url().endsWith('/memory/review'));
    await page.getByRole('button', { name: '保存审阅' }).click();
    const reviewed = await (await reviewedPromise).json();
    assert.equal(reviewed.memory_status, 'confirmed');
    await page.screenshot({ path: path.join(output, 'resolved.png'), fullPage: true });
    await page.getByRole('button', { name: '经验库', exact: true }).click();
    await page.locator('#memory-view').waitFor({ state: 'visible' });
    assert.match(await page.locator('#memory-view').innerText(), /已确认/);
    await page.getByRole('button', { name: '平台能力', exact: true }).click();
    await page.locator('#platform-view').waitFor({ state: 'visible' });
    await page.getByRole('button', { name: '调查工作台', exact: true }).click();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: path.join(output, 'mobile.png'), fullPage: true });
    const width = await page.evaluate(() => document.documentElement.scrollWidth);
    assert.ok(width <= 392, 'mobile page must not overflow horizontally: ' + width);
    assert.deepEqual(errors, [], 'browser must not report uncaught JavaScript errors');
    console.log(JSON.stringify({ status: 'passed', incident: created.id, screenshots: output, browser_errors: errors }));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
