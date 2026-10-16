/* Real-backend UI acceptance for local projects. Requires an isolated OFFLINE
 * IncidentAI server; does not execute fixture code or call any external model.
 * Existing Playwright/runtime: same environment variables as ui-smoke.cjs.
 * Synthetic fixture directories are intentionally retained, never deleted. */
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs');
const os = require('node:os');

(async () => {
  const base = (process.env.INCIDENTAI_TEST_URL || 'http://127.0.0.1:8100').replace(/\/$/, '');
  const address = new URL(base);
  assert.ok(['localhost', '127.0.0.1', '[::1]'].includes(address.hostname), 'smoke test must use a loopback server');
  assert.equal(address.protocol, 'http:', 'smoke test expects a local HTTP server');
  assert.equal(address.pathname, '/', 'server URL must not contain a path');
  assert.equal(address.username + address.password + address.search + address.hash, '', 'server URL must not contain credentials, a query, or a fragment');

  const output = path.resolve('data/local-project-ui-verification');
  fs.mkdirSync(output, { recursive: true });
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_EXECUTABLE_PATH || undefined });
  try {
    const context = await browser.newContext({ viewport: { width: 1500, height: 1000 } });
    const statusResponse = await context.request.get(base + '/api/v2/status');
    assert.equal(statusResponse.status(), 200, 'offline server must be running');
    const status = await statusResponse.json();
    assert.equal(status.mode, 'offline', 'local-project smoke test requires isolated offline mode');

    const fixture = fs.mkdtempSync(path.join(os.tmpdir(), 'incidentai-local-ui-'));
    fs.mkdirSync(path.join(fixture, 'logs'));
    const secrets = ['synthetic-ui-log-credential-7c35', 'synthetic-ui-dotenv-credential-8b46'];
    const fixtureFiles = {
      'retry.py': 'class PaymentRetryExceeded(Exception):\n    pass\n\ndef retry_payment():\n    raise PaymentRetryExceeded("Synthetic fixture; never execute")\n',
      [path.join('logs', 'sample.log')]: '2026-10-05T14:35:00Z ERROR PaymentRetryExceeded request_id=synthetic-001\n' +
        `2026-10-05T14:35:01Z ERROR PaymentRetryExceeded {"Authorization":"Bearer ${secrets[0]}"}\n`,
      '.env': `API_KEY=${secrets[1]}\n`,
    };
    for (const [name, content] of Object.entries(fixtureFiles)) fs.writeFileSync(path.join(fixture, name), content, { mode: 0o600 });
    const projectName = '离线浏览器验收 · ' + path.basename(fixture);
    const question = '模拟日志出现 PaymentRetryExceeded，请关联代码并区分事实与待验证假设。';
    const page = await context.newPage();
    page.setDefaultTimeout(15000);
    const errors = [], externalRequests = [], responseChecks = [], modelReviewPosts = [];
    const assertRedacted = (content, source) => {
      for (const secret of secrets) assert.ok(!content.includes(secret), `${source} leaked a synthetic credential`);
    };
    page.on('pageerror', error => errors.push(error.message));
    page.on('request', request => {
      if (request.method() === 'POST' && new URL(request.url()).pathname.endsWith('/model-review')) modelReviewPosts.push(request.url());
    });
    page.on('response', response => {
      const url = new URL(response.url());
      if (url.origin === address.origin && url.pathname.startsWith('/api/v2/projects')) {
        responseChecks.push(response.text().then(body => assertRedacted(body, 'project API response')));
      }
    });
    await page.route('**/*', async route => {
      const url = new URL(route.request().url());
      if (url.origin !== address.origin) {
        externalRequests.push(url.origin);
        await route.abort('blockedbyclient');
        return;
      }
      await route.continue();
    });

    await page.goto(base);
    await page.getByRole('button', { name: '本地项目', exact: true }).click();
    await page.locator('#projects-view').waitFor({ state: 'visible' });
    await page.locator('#new-local-project').click();
    await page.locator('#project-name').fill(projectName);
    await page.locator('#project-repository').fill(fixture);
    await page.locator('#project-logs').fill(path.join(fixture, 'logs'));
    await page.locator('#project-health').fill('');
    const createdPromise = page.waitForResponse(r => r.url() === base + '/api/v2/projects' && r.request().method() === 'POST');
    await page.locator('#save-project').click();
    const createdResponse = await createdPromise;
    assert.equal(createdResponse.status(), 200, 'fixture project must register successfully');
    const project = await createdResponse.json();
    assert.equal(project.repository_path, fixture);
    const projectAPI = base + '/api/v2/projects/' + encodeURIComponent(project.id);
    await page.locator('#validate-project').waitFor({ state: 'visible' });
    const validationPromise = page.waitForResponse(r => r.url() === projectAPI + '/validate' && r.request().method() === 'POST');
    await page.locator('#validate-project').click();
    const validation = await (await validationPromise).json();
    assert.equal(validation.valid, true, 'synthetic source and log paths must validate');
    await page.getByRole('heading', { name: '连接验证通过', exact: true }).waitFor();

    await page.locator('#project-question').fill(question);
    const investigatedPromise = page.waitForResponse(r => r.url() === projectAPI + '/investigations' && r.request().method() === 'POST', { timeout: 60000 });
    await page.locator('#start-project-investigation').click();
    const investigatedResponse = await investigatedPromise;
    assert.equal(investigatedResponse.status(), 200, 'real offline investigation must succeed');
    const run = await investigatedResponse.json();
    assert.equal(run.project_id, project.id);
    assert.equal(run.question, question);
    assert.equal(run.source_changed, false);
    assert.ok(run.findings.length > 0, 'synthetic errors should produce a finding');
    assert.ok(run.hypotheses.length >= 2, 'alternative hypotheses must remain visible');
    assert.ok(run.evidence.some(item => item.kind === 'log_tail' && item.content.includes('PaymentRetryExceeded')), 'log evidence must be real fixture content');
    assert.ok(run.evidence.some(item => item.kind === 'source_match' && item.path === 'retry.py'), 'ripgrep must associate the fixture error with retry.py');
    assert.ok(!run.evidence.some(item => item.path === '.env'), 'dotenv must not be collected as source');
    assertRedacted(JSON.stringify(run), 'saved investigation');
    const report = page.locator('#project-investigation-result');
    await report.waitFor({ state: 'visible' });
    assert.ok((await report.innerText()).includes('本地检查未调用模型'));
    await page.locator('.project-finding .evidence-link').first().click();
    const linkedID = await page.locator('.project-finding .evidence-link').first().innerText();
    const linkedEvidence = page.locator(`.project-evidence[data-evidence-id="${linkedID}"]`);
    assert.equal(await linkedEvidence.evaluate(element => element.open), true, 'finding reference must open the corresponding evidence');
    const codeEvidence = run.evidence.find(item => item.kind === 'source_match' && item.path === 'retry.py');
    const codeDetail = page.locator(`.project-evidence[data-evidence-id="${codeEvidence.id}"]`);
    await codeDetail.locator('summary').click();
    assert.equal(await codeDetail.evaluate(element => element.open), true);
    assert.match(await codeDetail.innerText(), /retry\.py/);
    assertRedacted(await page.locator('body').textContent(), 'desktop DOM');
    await page.screenshot({ path: path.join(output, 'local-project-desktop.png'), fullPage: true });

    const refreshedPromise = page.waitForResponse(r => r.url() === projectAPI + '/investigations' && r.request().method() === 'GET');
    await page.locator('#refresh-local-projects').click();
    const history = await (await refreshedPromise).json();
    assert.ok(history.some(item => item.id === run.id), 'saved run must survive a history refresh');
    await page.locator(`.project-history-row[data-run-id="${run.id}"]`).waitFor({ state: 'visible' });
    await page.reload();
    await page.locator('#projects-view').waitFor({ state: 'visible' });
    await page.locator(`.project-history-row[data-run-id="${run.id}"].selected`).waitFor({ state: 'visible' });
    await report.waitFor({ state: 'visible' });
    assert.ok((await report.innerText()).includes(run.id), 'reload must restore the saved investigation');

    const previewPromise = page.waitForResponse(r => r.url() === projectAPI + '/investigations/' + run.id + '/model-preview' && r.request().method() === 'POST');
    await page.locator('#preview-project-model').click();
    const previewResponse = await previewPromise;
    assert.equal(previewResponse.status(), 409, 'offline mode must refuse model preview');
    assert.match((await previewResponse.json()).error, /未配置模型/);
    await page.locator('#projects-view [role="alert"]').filter({ hasText: '未配置模型' }).waitFor({ state: 'visible' });
    assert.equal(await page.locator('#project-model-dialog[open]').count(), 0, 'failed offline preview must not open a consent dialog');

    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: path.join(output, 'local-project-mobile.png'), fullPage: true });
    const width = await page.evaluate(() => document.documentElement.scrollWidth);
    assert.ok(width <= 390, `mobile page must not overflow horizontally: ${width}`);
    assertRedacted(await page.locator('body').textContent(), 'mobile DOM');
    for (const [name, content] of Object.entries(fixtureFiles)) assert.equal(fs.readFileSync(path.join(fixture, name), 'utf8'), content, 'read-only investigation changed fixture file ' + name);
    await Promise.all(responseChecks);
    assert.deepEqual(modelReviewPosts, [], 'offline UI test must never submit a model review');
    assert.deepEqual(externalRequests, [], 'browser must not issue external requests');
    assert.deepEqual(errors, [], 'browser must not report uncaught JavaScript errors');
    console.log(JSON.stringify({ status: 'passed', mode: 'offline', project: project.id, investigation: run.id, fixture_retained: fixture, screenshots: output, checked_api_responses: responseChecks.length, findings: run.findings.length, evidence: run.evidence.length, mobile_width: width, browser_errors: errors, external_requests: externalRequests.length, model_review_calls: modelReviewPosts.length }));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(JSON.stringify({ status: 'failed', error: error.message })); process.exitCode = 1; });
