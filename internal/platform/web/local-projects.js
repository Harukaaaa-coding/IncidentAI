"use strict";

(() => {
  const $ = selector => document.querySelector(selector);
  const array = value => Array.isArray(value) ? value : [];
  const text = value => value == null ? "" : String(value);
  const json = value => typeof value === "string" ? value : JSON.stringify(value, null, 2);
  const stored = key => { try { return localStorage.getItem(key) || ""; } catch { return ""; } };
  const remember = (key, value) => { try { localStorage.setItem(key, value); } catch { /* Optional browser state. */ } };
  const state = { projects: [], selected: stored("incidentai-project"), selectedRun: stored("incidentai-project-run"), history: [], run: null, validation: null, review: null, preview: null, loading: false, loaded: false, busy: "", error: "", request: 0, question: "请检查这个项目的代码、日志与健康状态，区分已确认的问题和需要进一步验证的假设，并给出带证据的建议。" };
  const labels = { passed: "通过", pass: "通过", success: "通过", ok: "通过", healthy: "正常", warning: "需注意", warn: "需注意", failed: "失败", error: "错误", unavailable: "不可用", skipped: "未检查", unknown: "未知", running: "检查中", investigating: "检查中", completed: "已完成", complete: "已完成", needs_evidence: "需补充证据", needs_attention: "需关注", inconclusive: "尚无定论", needs_verification: "待验证", high: "高", medium: "中", low: "低", info: "信息", confirmed: "已确认", supported: "有证据支持", possible: "待验证", unverified: "待验证", unsupported: "证据不足", rejected: "不支持" };
  const statusLabel = value => labels[value] || text(value) || "未知";
  function node(tag, className, content) { const result = document.createElement(tag); if (className) result.className = className; if (content != null) result.textContent = text(content); return result; }
  function add(parent, ...children) { children.flat().filter(Boolean).forEach(child => parent.append(child)); return parent; }
  function button(content, className, action, id) { const result = node("button", className, content); result.type = "button"; result.addEventListener("click", action); if (id) result.id = id; return result; }
  function badge(value) { const tone = ["passed", "pass", "success", "ok", "healthy", "completed", "complete", "confirmed"].includes(value) ? "good" : ["failed", "error", "high"].includes(value) ? "bad" : ["warning", "warn", "medium", "needs_evidence", "needs_attention", "inconclusive", "needs_verification"].includes(value) ? "warn" : ""; return node("span", `status-pill ${tone}`, statusLabel(value)); }
  function date(value) { if (!value) return "—"; const parsed = new Date(value); return Number.isNaN(parsed.getTime()) ? value : new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(parsed); }
  function panel(title, subtitle) { return add(node("section", "panel"), add(node("div", "section-label"), node("h2", "", title), subtitle ? node("span", "sub-label", subtitle) : null)); }
  function empty(message) { return node("p", "empty-note", message); }
  const path = () => `/projects/${encodeURIComponent(state.selected)}`;
  const runPath = () => `${path()}/investigations/${encodeURIComponent(state.run.id)}`;
  async function api(route, body) {
    const response = await fetch(`/api/v2${route}`, { method: body === undefined ? "GET" : "POST", headers: { "Accept": "application/json", ...(body === undefined ? {} : { "Content-Type": "application/json" }) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    let result; const raw = await response.text();
    try { result = raw ? JSON.parse(raw) : null; } catch { const error = new Error(`服务响应不可用（HTTP ${response.status}），请确认已启动支持本地项目的新版本。`); error.status = response.status; throw error; }
    if (!response.ok) { const error = new Error(result?.error || `请求失败（HTTP ${response.status}）`); error.status = response.status; throw error; }
    return result;
  }
  function report(error) { state.error = error?.message || text(error); render(); }
  async function refresh() {
    if (state.loading || state.busy) return;
    state.loading = true; state.error = ""; render();
    try {
      const result = await api("/projects"); state.projects = array(result?.projects || result); state.loaded = true;
      if (!state.projects.some(project => project.id === state.selected)) { state.selected = state.projects[0]?.id || ""; state.selectedRun = ""; }
      if (state.selected) await selectProject(state.selected, true); else { state.run = null; state.history = []; }
    } catch (error) { state.error = error.message; }
    finally { state.loading = false; render(); }
  }
  async function selectProject(id, restoring = false) {
    const request = ++state.request;
    if (!restoring || state.selected !== id) state.selectedRun = "";
    state.selected = id; state.run = null; state.review = null; state.validation = null; state.history = []; state.error = ""; state.preview = null;
    remember("incidentai-project", id); remember("incidentai-project-run", state.selectedRun); render();
    try {
      const result = await api(`/projects/${encodeURIComponent(id)}/investigations`);
      if (request !== state.request) return;
      state.history = array(result?.investigations || result);
      const selected = state.history.find(run => run.id === state.selectedRun) || state.history[0];
      if (selected) await selectRun(selected.id); else render();
    } catch (error) { if (request === state.request) report(error); }
  }
  async function selectRun(id) {
    const projectID = state.selected, request = ++state.request;
    state.selectedRun = id; state.review = null; state.preview = null; remember("incidentai-project-run", id);
    state.run = state.history.find(run => run.id === id) || null; render();
    try {
      const result = await api(`/projects/${encodeURIComponent(projectID)}/investigations/${encodeURIComponent(id)}`);
      if (request !== state.request || state.selected !== projectID) return;
      state.run = result; render();
      try { const review = await api(`/projects/${encodeURIComponent(projectID)}/investigations/${encodeURIComponent(id)}/model-review`); if (request === state.request) { state.review = review; render(); } }
      catch (error) { if (error.status !== 404 && request === state.request) report(error); }
    } catch (error) { if (request === state.request) report(error); }
  }
  async function validate() {
    if (state.busy || !state.selected) return;
    const projectID = state.selected; state.busy = "validate"; state.error = ""; render();
    try { const result = await api(`/projects/${encodeURIComponent(projectID)}/validate`, {}); if (state.selected === projectID) state.validation = result; }
    catch (error) { state.error = error.message; }
    finally { state.busy = ""; render(); }
  }
  async function investigate() {
    if (state.busy || !state.selected) return;
    const projectID = state.selected; state.busy = "investigate"; state.error = ""; render();
    try {
      const run = await api(`/projects/${encodeURIComponent(projectID)}/investigations`, { question: state.question.trim() });
      if (state.selected === projectID) { state.run = run; state.selectedRun = run.id; state.review = null; state.preview = null; state.history = [run, ...state.history.filter(item => item.id !== run.id)]; remember("incidentai-project-run", run.id); }
    } catch (error) { state.error = error.message; }
    finally { state.busy = ""; render(); }
  }
  function renderChecks(checks, title = "连接检查") {
    const result = panel(title);
    for (const check of array(checks)) add(result, add(node("div", "project-check"), badge(check.status), add(node("div", ""), node("h3", "", check.name), node("p", "", check.summary))));
    if (!array(checks).length) result.append(empty("暂无检查结果。"));
    return result;
  }
  function evidenceLinks(ids) {
    const result = node("div", "evidence-links");
    for (const id of array(ids)) result.append(button(id, "evidence-link", () => {
      const evidence = Array.from(document.querySelectorAll(".project-evidence")).find(element => element.dataset.evidenceId === id);
      if (evidence) { evidence.open = true; evidence.scrollIntoView({ block: "center" }); evidence.querySelector("summary").focus({ preventScroll: true }); }
    }));
    return result;
  }
  function renderInvestigation(run) {
    const result = node("div", "project-report"); result.id = "project-investigation-result";
    const header = panel("检查结果", date(run.completed_at || run.started_at));
    add(header, add(node("div", "tag-row"), badge(run.status), node("span", "soft-tag", "本地只读检查"), node("span", "soft-tag", "本地检查未调用模型")), node("p", "project-report-question", run.question), node("p", "context-note mono", run.id));
    if (run.source_changed) header.append(node("p", "project-warning", "检查过程中源文件发生变化，证据可能来自不同版本。请等项目改动稳定后重新检查。"));
    result.append(header, renderChecks(run.checks, "已执行的检查"));
    const findings = panel("发现与建议", `${array(run.findings).length} 项`);
    if (!array(run.findings).length) findings.append(empty("本轮没有形成已确认的问题。未发现不等于项目无故障，请结合假设与检查限制。"));
    for (const finding of array(run.findings)) {
      add(findings, add(node("article", "project-finding"), add(node("div", "project-finding-title"), badge(finding.severity), node("h3", "", finding.title)), node("p", "", finding.detail), finding.recommendation ? add(node("p", "project-recommendation"), node("strong", "", "建议："), document.createTextNode(finding.recommendation)) : null, evidenceLinks(finding.evidence_ids)));
    }
    result.append(findings);
    const hypotheses = panel("待验证的假设", "假设不等于根因结论");
    if (!array(run.hypotheses).length) hypotheses.append(empty("暂无结构化假设。当前证据不足以确定事故根因。"));
    for (const hypothesis of array(run.hypotheses)) add(hypotheses, add(node("article", "project-finding"), add(node("div", "project-finding-title"), badge(hypothesis.status), node("h3", "", hypothesis.title)), node("p", "", hypothesis.assessment), evidenceLinks(hypothesis.evidence_ids)));
    result.append(hypotheses);
    const evidencePanel = panel("证据", "点击展开脱敏内容和来源"); evidencePanel.id = "local-project-evidence";
    for (const evidence of array(run.evidence)) {
      const detail = node("details", "project-evidence"); detail.dataset.evidenceId = evidence.id;
      const summary = add(node("summary", ""), node("span", "evidence-id", evidence.id), node("span", "", evidence.summary || evidence.kind));
      const source = evidence.path ? `${evidence.path}${evidence.line ? `:${evidence.line}` : ""}` : evidence.source;
      add(detail, summary, add(node("div", "evidence-detail"), node("p", "evidence-source", `${evidence.kind || "证据"} · ${source || "来源未提供"}`), evidence.path && evidence.source && evidence.source !== evidence.path ? node("p", "evidence-source", evidence.source) : null, node("pre", "", evidence.content || "（无内容）")));
      evidencePanel.append(detail);
    }
    if (!array(run.evidence).length) evidencePanel.append(empty("未采集到可展示的证据。"));
    result.append(evidencePanel);
    const limits = panel("检查边界与不足");
    const list = node("ul", "project-limits");
    for (const limit of array(run.limitations)) list.append(node("li", "", limit));
    if (!list.childElementCount) list.append(node("li", "", "这不是完整的线上事故调查。仅凭静态代码和有限日志不能保证定位所有问题。"));
    limits.append(list); result.append(limits, renderModelReview());
    return result;
  }
  function renderModelReview() {
    const result = panel("可选：模型复核", "与本地检查分开授权");
    result.append(node("p", "project-body-text", "默认检查不调用模型。需要模型分析时，先预览将发送的确切内容，再单独授权；模型建议不会执行任何项目操作。"));
    if (state.review) {
      result.append(node("p", "project-review-model", `已获得模型复核 · ${state.review.model || "已配置模型"}`), node("p", "project-body-text", state.review.summary));
      for (const finding of array(state.review.findings)) add(result, add(node("article", "project-finding"), node("h3", "", finding.title), node("p", "", finding.detail), evidenceLinks(finding.evidence_ids)));
      const limits = node("ul", "project-limits"); for (const limit of array(state.review.limitations)) limits.append(node("li", "", limit)); if (limits.childElementCount) result.append(limits);
      if (state.review.usage) { const usage = node("details", "json-details"); add(usage, node("summary", "", "查看本次模型用量"), node("pre", "", json(state.review.usage))); result.append(usage); }
    } else result.append(node("p", "context-note", "未调用模型。没有任何调查内容因打开此页面而发送给外部模型。"));
    const previewButton = button(state.busy === "preview" ? "正在准备预览…" : "预览外发内容", "button secondary", previewModel, "preview-project-model"); previewButton.disabled = !!state.busy;
    result.append(previewButton); return result;
  }
  function render() {
    const target = $("#projects-view"); if (!target) return; target.replaceChildren();
    const heading = node("header", "page-heading");
    add(heading, add(node("div", ""), node("p", "eyebrow", "LOCAL PROJECTS"), node("h1", "", "调查你的本地项目"), node("p", "", "读取真实仓库、脱敏日志和健康响应，保留可追溯的证据。无需先接 MCP，也不会自动修改项目或执行恢复。")), button("＋ 添加本地项目", "button primary", openCreate, "new-local-project"));
    target.append(heading, node("p", "project-safety-note", "本地只读检查 · 不运行目标项目的测试、构建或脚本 · 外部模型调用需另行确认"));
    if (state.error) { const banner = node("div", "incident-error project-inline-error"); banner.setAttribute("role", "alert"); add(banner, node("span", "", state.error), button("关闭", "text-button", () => { state.error = ""; render(); })); target.append(banner); }
    if (state.loading && !state.loaded) { target.append(node("p", "loading-state", "正在读取本地项目…")); return; }
    const layout = node("div", "project-layout");
    const sidebar = node("aside", "project-sidebar"); sidebar.setAttribute("aria-label", "本地项目列表");
    const refreshButton = button("刷新", "text-button", refresh, "refresh-local-projects"); refreshButton.disabled = state.loading || !!state.busy;
    add(sidebar, add(node("div", "section-label"), node("h2", "", `已接入项目 · ${state.projects.length}`), refreshButton));
    for (const project of state.projects) {
      const row = button("", `project-row ${project.id === state.selected ? "selected" : ""}`, () => selectProject(project.id)); row.disabled = !!state.busy; row.dataset.projectId = project.id;
      if (project.id === state.selected) row.setAttribute("aria-current", "true");
      add(row, node("strong", "", project.name), node("span", "mono", project.repository_path)); sidebar.append(row);
    }
    if (!state.projects.length) sidebar.append(empty("还没有接入项目。添加本机目录后开始检查。"));
    const content = node("div", "project-content"), project = state.projects.find(item => item.id === state.selected);
    if (!project) {
      const welcome = node("section", "empty-page project-empty"); add(welcome, node("h2", "", "先接入一个真实项目"), node("p", "", "例如 DeepSearch：填写仓库目录；日志目录和健康地址可以稍后接入。"), button("添加本地项目", "button primary", openCreate)); content.append(welcome);
    } else {
      const details = panel(project.name, "仅保存接入配置");
      const paths = node("dl", "project-paths");
      for (const [label, value] of [["仓库目录", project.repository_path], ["日志路径", project.log_path || "未配置 · 不采集日志"], ["健康地址", project.health_url || "未配置 · 不发起健康请求"]]) add(paths, node("dt", "", label), node("dd", "mono", value));
      const validateButton = button(state.busy === "validate" ? "正在验证…" : "验证连接", "button secondary", validate, "validate-project"); validateButton.disabled = !!state.busy;
      add(details, paths, validateButton); content.append(details);
      if (state.validation) content.append(renderChecks(state.validation.checks, state.validation.valid ? "连接验证通过" : "连接验证：需要处理以下项目"));
      const form = node("form", "panel project-investigation-form"); form.id = "project-investigation-form";
      const label = node("label", "form-label", "想调查什么？"); label.htmlFor = "project-question";
      const question = node("textarea", ""); question.id = "project-question"; question.rows = 3; question.maxLength = 2000; question.required = true; question.value = state.question; question.disabled = !!state.busy; question.placeholder = "例如：研究任务偶尔中断，请检查相关代码和日志。"; question.addEventListener("input", () => { state.question = question.value; });
      const start = node("button", "button primary", state.busy === "investigate" ? "正在采集本地证据…" : "开始只读调查 ↗"); start.type = "submit"; start.id = "start-project-investigation"; start.disabled = !!state.busy;
      form.addEventListener("submit", event => { event.preventDefault(); investigate(); });
      add(form, label, question, node("p", "form-hint", "读取范围受限，并进行凭据脱敏。不会运行项目代码，也不会调用付费模型。问题用于记录调查目标，本地检查项是固定的，不代表已穷尽问题。"), start); content.append(form);
      if (state.busy === "investigate") { const progress = node("p", "progress-note", "正在读取仓库、日志和健康状态。完成后会保存调查记录；不会执行测试、构建、重启或回滚。"); progress.setAttribute("role", "status"); content.append(progress); }
      const history = panel("调查历史", `${state.history.length} 次`); history.id = "project-history";
      if (!state.history.length) history.append(empty("暂无历史调查。结果会保存在 IncidentAI 中，下次打开可继续查看。"));
      for (const run of state.history) {
        const row = button("", `project-history-row ${run.id === state.selectedRun ? "selected" : ""}`, () => selectRun(run.id)); row.disabled = !!state.busy; row.dataset.runId = run.id;
        add(row, node("span", "", run.question || run.id), node("time", "", date(run.started_at)), badge(run.status)); history.append(row);
      }
      content.append(history); if (state.run) content.append(renderInvestigation(state.run));
    }
    add(layout, sidebar, content); target.append(layout);
  }
  function ensureDialogs() {
    if ($("#project-dialog")) return;
    const dialog = node("dialog", "create-dialog project-dialog"); dialog.id = "project-dialog"; dialog.setAttribute("aria-labelledby", "project-dialog-title");
    const form = node("form", ""); form.id = "project-form";
    add(form, add(node("div", "dialog-heading"), add(node("div", ""), node("p", "eyebrow", "CONNECT A LOCAL PROJECT"), node("h2", "", "添加本地项目")), button("×", "icon-button", () => dialog.close()))); form.querySelector("h2").id = "project-dialog-title"; form.querySelector("button").setAttribute("aria-label", "关闭添加项目");
    add(form, node("p", "muted", "填写 IncidentAI 所在这台电脑上的绝对路径。仅保存读取范围；添加项目本身不会启动调查。"), button("填入 DeepSearch", "button secondary project-suggestion", fillSuggestion, "fill-deepsearch"));
    const fields = [["project-name", "name", "项目名称", "例如 DeepSearch", true], ["project-repository", "repository_path", "仓库目录（绝对路径）", "C:\\Users\\...\\GolandProjects\\DeepSearch", true], ["project-logs", "log_path", "仓库内日志文件或目录（可选）", "必须位于上方仓库目录内部", false], ["project-health", "health_url", "健康检查地址（可选）", "http://127.0.0.1:8080/health", false]];
    for (const [id, name, labelText, placeholder, required] of fields) { const label = node("label", "form-label", labelText); label.htmlFor = id; const input = node("input", ""); input.id = id; input.name = name; input.type = "text"; input.autocomplete = "off"; input.required = required; input.placeholder = placeholder; input.maxLength = name === "name" ? 120 : 1024; add(form, label, input); }
    const logHint = node("p", "form-hint", "日志路径必须在仓库目录内部；当前仅采集 .log、.txt、.jsonl、.ndjson 文件。"); logHint.id = "project-log-hint"; form.querySelector("#project-logs").after(logHint); form.querySelector("#project-logs").setAttribute("aria-describedby", logHint.id);
    add(form, node("p", "form-hint", "不使用浏览器上传文件，也不会复制整个仓库。健康地址只接受服务端允许的本机地址；不要填有副作用的操作接口。"));
    const error = node("p", "incident-error"); error.id = "project-form-error"; error.hidden = true; error.setAttribute("role", "alert"); form.append(error);
    const submit = node("button", "button primary", "保存项目"); submit.type = "submit"; submit.id = "save-project";
    add(form, add(node("div", "dialog-actions"), button("取消", "button secondary", () => dialog.close()), submit));
    form.addEventListener("submit", saveProject); dialog.append(form); document.body.append(dialog);
    const modelDialog = node("dialog", "create-dialog project-model-dialog"); modelDialog.id = "project-model-dialog"; modelDialog.setAttribute("aria-labelledby", "project-model-title"); document.body.append(modelDialog);
  }
  function openCreate() {
    ensureDialogs(); $("#project-form").reset(); $("#project-form-error").hidden = true;
    $("#project-dialog").showModal();
  }
  async function fillSuggestion() {
    const trigger = $("#fill-deepsearch"); trigger.disabled = true; $("#project-form-error").hidden = true;
    try {
      const result = await api("/projects/suggestions"), suggestions = array(result?.suggestions || result);
      const suggestion = suggestions.find(item => /deep\s*search/i.test(item.name)) || suggestions[0];
      if (!suggestion) throw new Error("未在本机找到 DeepSearch，请手动填写项目路径。");
      for (const [id, key] of [["project-name", "name"], ["project-repository", "repository_path"], ["project-logs", "log_path"], ["project-health", "health_url"]]) $(`#${id}`).value = suggestion[key] || "";
    } catch (error) { $("#project-form-error").textContent = error.message; $("#project-form-error").hidden = false; }
    finally { trigger.disabled = false; }
  }
  async function saveProject(event) {
    event.preventDefault(); const submit = $("#save-project"); if (submit.disabled) return; submit.disabled = true; submit.textContent = "正在保存…"; $("#project-form-error").hidden = true;
    const data = new FormData($("#project-form")), body = {}; for (const key of ["name", "repository_path", "log_path", "health_url"]) body[key] = text(data.get(key)).trim();
    try { const project = await api("/projects", body); state.projects = [project, ...state.projects.filter(item => item.id !== project.id)]; state.loaded = true; $("#project-dialog").close(); await selectProject(project.id); }
    catch (error) { $("#project-form-error").textContent = error.message; $("#project-form-error").hidden = false; }
    finally { submit.disabled = false; submit.textContent = "保存项目"; }
  }
  async function previewModel() {
    if (state.busy || !state.run) return;
    state.busy = "preview"; state.error = ""; const route = runPath(); render();
    try {
      const preview = await api(`${route}/model-preview`, {}); state.preview = { ...preview, route }; ensureDialogs(); renderPreview(); $("#project-model-dialog").showModal();
    } catch (error) { state.error = error.message; }
    finally { state.busy = ""; render(); }
  }
  function renderPreview() {
    const target = $("#project-model-dialog"), preview = state.preview; target.replaceChildren();
    const heading = node("h2", "", "确认向外部模型发送内容"); heading.id = "project-model-title";
    const close = button("×", "icon-button", () => target.close()); close.setAttribute("aria-label", "关闭模型授权");
    add(target, add(node("div", "dialog-heading"), add(node("div", ""), node("p", "eyebrow", "EXTERNAL MODEL REVIEW"), heading), close));
    add(target, node("p", "project-warning", preview.warning || "内容将发送至配置的外部模型服务，可能产生 API 费用。请检查脱敏结果，不要发送未获授权的代码或个人信息。"));
    const metadata = node("dl", "project-paths"); for (const [label, value] of [["模型", preview.model], ["接收地址", preview.endpoint], ["数据大小", `${preview.bytes ?? "—"} bytes`], ["内容摘要", preview.digest]]) add(metadata, node("dt", "", label), node("dd", "mono", value)); target.append(metadata);
    target.append(node("p", "form-hint", "以下为本次将发送的确切内容。凭据脱敏是尽力处理，不能替代你的人工检查。"));
    const payload = node("pre", "project-model-payload", json(preview.payload)); payload.id = "project-model-payload"; payload.tabIndex = 0; target.append(payload);
    const consent = node("input", ""); consent.type = "checkbox"; consent.id = "project-model-consent"; consent.checked = false;
    const label = add(node("label", "project-consent"), consent, node("span", "", `我已检查上述内容，授权将它发送到 ${preview.endpoint || "外部 B.AI 服务"}，并接受可能产生的 API 费用。此授权仅用于这次复核。`));
    const send = button("授权并调用模型", "button primary", submitModel, "approve-project-model"); send.disabled = true;
    consent.addEventListener("change", () => { send.disabled = !consent.checked; });
    const error = node("p", "incident-error"); error.id = "project-model-error"; error.hidden = true; error.setAttribute("role", "alert");
    add(target, label, error, add(node("div", "dialog-actions"), button("取消，不发送", "button secondary", () => target.close()), send));
  }
  async function submitModel() {
    const preview = state.preview, consent = $("#project-model-consent"), submit = $("#approve-project-model");
    if (!preview || !consent.checked || submit.disabled || state.busy) return;
    submit.disabled = true; consent.disabled = true; state.busy = "model"; submit.textContent = "等待模型复核…"; $("#project-model-error").hidden = true;
    try { const review = await api(`${preview.route}/model-review`, { approved: true, digest: preview.digest }); if (state.run && runPath() === preview.route) state.review = review; $("#project-model-dialog").close(); state.preview = null; }
    catch (error) { $("#project-model-error").textContent = `${error.message} 请关闭窗口并重新预览后再决定是否重试。`; $("#project-model-error").hidden = false; }
    finally { state.busy = ""; render(); submit.textContent = "授权并调用模型"; /* Do not blindly retry a possibly billed request. */ }
  }
  window.IncidentLocalProjects = { show() { render(); if (!state.loaded && !state.loading) refresh(); }, openCreate };
})();
