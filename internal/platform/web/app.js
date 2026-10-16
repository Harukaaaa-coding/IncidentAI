"use strict";

(() => {
  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));
  const arr = value => Array.isArray(value) ? value : [];
  const record = value => value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const str = value => value == null ? "" : typeof value === "object" ? JSON.stringify(value) : String(value);
  const pretty = value => typeof value === "string" ? value : JSON.stringify(value ?? {}, null, 2);
  const number = value => typeof value === "number" && Number.isFinite(value) ? value : null;
  const compact = value => number(value) == null ? "—" : Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1, notation: value >= 10000 ? "compact" : "standard" }).format(value);
  const state = { platform: {}, incidents: [], current: null, selected: null, selectedEvidence: null, view: "workbench", memory: [], memoryFilter: "all", memoryReview: "confirmed", evaluation: null, busy: new Set(), evaluationBusy: false, loading: true, toastTimer: null };
  const labels = {
    new: "待调查", open: "待调查", created: "待调查", pending: "待执行", queued: "等待中", investigating: "调查中", running: "执行中", in_progress: "执行中", complete: "已完成", completed: "已完成", done: "已完成", succeeded: "已完成", success: "已完成", passed: "已通过", failed: "失败", error: "出错", approval_required: "待人工批准", awaiting_approval: "待人工批准", proposed: "待批准", approved: "已批准", executing: "执行中", recovering: "恢复中", resolved: "已恢复", blocked: "已阻止", denied: "已阻止", skipped: "已跳过", pending_review: "待复核", confirmed: "已确认", probable: "可能正确", rejected: "已拒绝", outdated: "已过时", candidate: "候选经验", read: "只读", low: "低风险", medium: "中风险", high: "高风险", critical: "极高风险", auto: "自动允许", approval: "人工批准", admin_only: "仅管理员", healthy: "健康", normal: "正常", degraded: "异常", unhealthy: "异常", down: "不可用", unknown: "未知", offline: "离线规则基线", llm: "LLM", multi: "多 Agent 协作", single: "单 Agent", semantic: "系统知识", episodic: "历史事故", runbook: "Runbook", incident: "历史事故", working: "工作记忆"
  };
  Object.assign(labels, { needs_evidence: "待补充证据", recovery_failed: "恢复未通过", executed: "已执行" });
  const label = value => labels[value] || str(value) || "—";
  const goodStates = new Set(["resolved", "done", "complete", "completed", "succeeded", "success", "passed", "confirmed", "healthy", "normal", "auto", "executed"]);
  const badStates = new Set(["failed", "error", "blocked", "denied", "rejected", "down", "unhealthy", "recovery_failed"]);
  const warnStates = new Set(["approval_required", "awaiting_approval", "proposed", "probable", "degraded", "outdated", "approval", "needs_evidence"]);
  const infoStates = new Set(["investigating", "running", "in_progress", "executing", "recovering", "approved"]);
  const tone = value => goodStates.has(value) ? "good" : badStates.has(value) ? "bad" : warnStates.has(value) ? "warn" : infoStates.has(value) ? "info" : "";
  function el(tag, className, text) { const node = document.createElement(tag); if (className) node.className = className; if (text != null) node.textContent = str(text); return node; }
  function add(parent, ...nodes) { nodes.flat().filter(Boolean).forEach(node => parent.append(node)); return parent; }
  function button(text, className, onClick, options = {}) { const node = el("button", className, text); node.type = "button"; node.disabled = !!options.disabled; if (options.focus) node.dataset.focus = options.focus; node.addEventListener("click", onClick); return node; }
  function pill(value) { return el("span", `status-pill ${tone(value)}`, label(value)); }
  function severity(value) { return el("span", `severity ${str(value).replace(/[^0-9]/g, "") === "2" ? "sev2" : str(value).replace(/[^0-9]/g, "") !== "1" ? "sev3" : ""}`, value || "SEV-?"); }
  function section(title, subtitle, className = "panel") { const node = el("section", className); const heading = el("div", "section-label"); add(heading, el("h2", "", title), subtitle ? el("span", "sub-label", subtitle) : null); node.append(heading); return node; }
  function empty(text) { return el("p", "empty-note", text); }
  function time(value, date = false) {
    if (!value) return "—";
    if (/^\d{2}:\d{2}(:\d{2})?$/.test(str(value))) return value;
    const parsed = new Date(value);
    if (Number.isNaN(parsed.getTime())) return str(value);
    return new Intl.DateTimeFormat("zh-CN", date ? { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false } : { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(parsed);
  }
  function listText(value) { if (typeof value === "string") return value; return str(value?.event || value?.summary || value?.title || value?.text || value?.content || value); }
  function showError(error) { $("#error-text").textContent = error?.message || str(error); $("#error-banner").hidden = false; }
  function toast(message) { clearTimeout(state.toastTimer); $("#toast").textContent = message; $("#toast").hidden = false; state.toastTimer = setTimeout(() => { $("#toast").hidden = true; }, 4500); }
  async function api(path, options = {}) {
    const response = await fetch(`/api/v2${path}`, { headers: { "Content-Type": "application/json", "Accept": "application/json" }, ...options, body: options.body === undefined ? undefined : JSON.stringify(options.body) });
    let result;
    const body = await response.text();
    try { result = body ? JSON.parse(body) : null; } catch { throw new Error(`服务返回了无效响应（HTTP ${response.status}）。请确认 V2 服务正在运行。`); }
    if (!response.ok) throw new Error(result?.error || result?.message || `请求失败：HTTP ${response.status}`);
    return result;
  }
  function isBusy(id = state.selected) { return state.busy.has(id); }
  function rememberFocus(fn) {
    const focus = document.activeElement?.dataset?.focus;
    fn();
    if (focus) { const candidate = $$("[data-focus]").find(node => node.dataset.focus === focus); if (candidate && !candidate.disabled) candidate.focus({ preventScroll: true }); }
  }
  function updateIncident(incident) {
    if (!incident?.id) return;
    const index = state.incidents.findIndex(item => item.id === incident.id);
    if (index === -1) state.incidents.unshift(incident); else state.incidents[index] = incident;
    if (state.selected === incident.id) state.current = incident;
  }
  async function loadIncidents() { const response = await api("/incidents"); state.incidents = arr(response?.incidents || response); renderList(); return state.incidents; }
  async function loadMemory() { const response = await api("/memory"); state.memory = arr(response?.items || response?.memories || response); if (state.view === "memory") renderMemory(); }
  async function selectIncident(id) {
    state.selected = id; state.selectedEvidence = null; state.current = state.incidents.find(item => item.id === id) || null;
    changeView("workbench"); renderIncident(); renderList();
    try { const incident = await api(`/incidents/${encodeURIComponent(id)}`); updateIncident(incident); if (state.selected === id) renderIncident(); renderList(); } catch (error) { showError(error); }
  }
  async function mutateIncident(path, body, success) {
    const id = state.selected;
    if (!id || isBusy(id)) return;
    state.busy.add(id); renderIncident();
    let pollActive = false;
    const poll = setInterval(async () => {
      if (pollActive || !state.busy.has(id)) return;
      pollActive = true;
      try { const result = await api(`/incidents/${encodeURIComponent(id)}`); if (state.busy.has(id)) { updateIncident(result); if (state.selected === id) renderIncident(); renderList(); } } catch { /* The mutation response remains authoritative for errors. */ }
      finally { pollActive = false; }
    }, 1000);
    try { const result = await api(`/incidents/${encodeURIComponent(id)}${path}`, { method: "POST", body }); updateIncident(result); if (success) toast(success); }
    catch (error) { showError(error); try { updateIncident(await api(`/incidents/${encodeURIComponent(id)}`)); } catch { /* Preserve the last available snapshot. */ } }
    finally { clearInterval(poll); state.busy.delete(id); if (state.selected === id) renderIncident(); renderList(); }
    try { await loadMemory(); } catch { /* Memory may be unavailable while the incident view remains usable. */ }
  }
  function renderList() {
    $("#incident-count").textContent = state.incidents.length;
    const query = $("#incident-search").value.trim().toLocaleLowerCase();
    const incidents = state.incidents.filter(item => `${item.title} ${item.service} ${item.id}`.toLocaleLowerCase().includes(query));
    const target = $("#incident-list"); target.replaceChildren();
    if (!incidents.length) target.append(empty(query ? "没有匹配的事故" : "还没有事故。点击 ＋ 创建一个。"));
    for (const item of incidents) {
      const node = button("", `incident-row ${item.id === state.selected ? "selected" : ""}`, () => selectIncident(item.id), { focus: `incident-${item.id}` });
      node.setAttribute("aria-label", `${item.service || "事故"}，${item.title || item.id}，${label(item.status)}`);
      if (item.id === state.selected) node.setAttribute("aria-current", "true");
      add(node, add(el("div", "incident-row-head"), severity(item.severity), el("span", "incident-row-service", item.service || item.title || item.id)), el("p", "incident-row-title", item.title), add(el("div", "incident-row-meta"), pill(item.status), el("time", "", time(item.created_at, true))));
      target.append(node);
    }
  }
  function renderWelcome() {
    const target = $("#main-content"); target.replaceChildren();
    const welcome = el("section", "welcome");
    add(welcome, el("p", "eyebrow", "INCIDENT CONTROL CENTER"), el("h1", "", "从一个信号，\n开始调查。"), el("p", "muted", "接入本地项目，读取代码与脱敏日志，获得带证据的只读建议；也可以选择模拟场景，体验事故响应闭环。"), button("＋ 添加本地项目", "button primary welcome-project-cta", () => { changeView("projects"); window.IncidentLocalProjects?.openCreate(); }), el("div", "welcome-rule"), el("h2", "", "选择模拟故障场景"));
    const grid = el("div", "scenario-grid");
    arr(state.platform.scenarios).forEach((scenario, index) => {
      const card = button("", "scenario-card", () => openCreate(scenario.id));
      add(card, add(el("div", "scenario-index"), el("span", "", `0${index + 1} / ${scenario.service || "SCENARIO"}`), el("span", "", "↗")), el("h3", "", scenario.name || scenario.title || scenario.id), el("p", "", scenario.description));
      grid.append(card);
    });
    if (!grid.childElementCount) grid.append(empty("暂无可用场景，请检查平台连接。"));
    add(welcome, grid, add(el("div", "welcome-footer"), el("span", "", "有来源的证据"), el("span", "", "可审阅的恢复建议"), el("span", "", "人工批准后执行")));
    target.append(welcome);
    const placeholder = el("div", "inspector-empty");
    add(placeholder, el("span", "empty-icon", "⊞"), el("h2", "", "调查上下文"), el("p", "", "证据、历史经验与恢复建议将在调查开始后显示。"));
    $("#inspector").replaceChildren(placeholder);
  }
  function renderIncident() {
    if (!state.current) { renderWelcome(); return; }
    rememberFocus(() => {
      const incident = state.current;
      const target = $("#main-content"); target.replaceChildren();
      const header = el("header", "incident-header");
      add(header, add(el("div", "breadcrumb"), el("span", "", "事故调查"), el("span", "", "/"), el("span", "mono", incident.id)));
      const heading = add(el("div", "incident-heading"), el("h1", "", incident.title || incident.service || "未命名事故"));
      const retry = ["failed", "error", "needs_evidence", "recovery_failed"].includes(incident.status);
      const canInvestigate = !isBusy() && (retry || ["new", "open", "created", ""].includes(incident.status || ""));
      if (canInvestigate || isBusy() || incident.status === "investigating") heading.append(button(isBusy() || incident.status === "investigating" ? "处理进行中…" : retry ? "重新调查 ↗" : "开始调查 ↗", "button primary", () => mutateIncident("/investigate", {}, "调查状态已更新"), { disabled: !canInvestigate, focus: "investigate" }));
      add(header, heading, add(el("div", "incident-subtitle"), severity(incident.severity), el("span", "mono", incident.service), el("span", "divider-dot", "·"), pill(incident.status), el("span", "divider-dot", "·"), el("span", "", label(incident.mode)), el("span", "divider-dot", "·"), el("span", "", label(incident.engine || state.platform.mode))));
      target.append(header);
      if (incident.status === "resolved") target.append(el("p", "resolved-notice", "恢复验证已通过。查看复盘并审阅候选经验，决定是否供后续调查使用。"));
      if (incident.error) target.append(el("p", "incident-error", incident.error));
      if (isBusy()) target.append(el("p", "progress-note", "正在执行调查或恢复步骤，证据与计划每秒刷新。"));
      target.append(renderMetrics(incident));
      target.append(renderPlan(incident));
      target.append(renderHypotheses(incident));
      if (arr(incident.topology?.nodes).length) target.append(renderTopology(incident));
      target.append(renderTimeline(incident));
      if (incident.postmortem && Object.keys(incident.postmortem).length) target.append(renderPostmortem(incident));
      renderInspector(incident);
    });
  }
  function renderMetrics(incident) {
    const grid = el("section", "metric-grid"); grid.setAttribute("aria-label", "事故指标");
    const points = arr(incident.metrics?.points);
    for (const metric of [{ key: "error_rate", title: "错误率", unit: "%", className: incident.status === "resolved" ? "recovered" : "", color: incident.status === "resolved" ? "#4a987e" : "#c66f64" }, { key: "latency_ms", title: "请求延迟", unit: "ms", className: "latency", color: "#6d8ebd" }]) {
      const card = el("article", `metric-card ${metric.className}`);
      add(card, add(el("div", "metric-title"), el("span", "", metric.title), el("span", "metric-dot")));
      const valid = points.filter(point => number(point[metric.key]) != null);
      if (!valid.length) { add(card, el("div", "metric-value", "—"), el("p", "metric-caption", "等待指标工具返回数据"), el("div", "metric-empty", "暂无时间序列")); grid.append(card); continue; }
      const last = valid[valid.length - 1], first = valid[0];
      add(card, add(el("div", "metric-value"), el("span", "", compact(last[metric.key])), el("span", "unit", metric.unit)), el("p", "metric-caption", `首个样本 ${compact(first[metric.key])}${metric.unit} · ${valid.length} 个采样点`), sparkline(valid, metric.key, metric.color, metric.unit), add(el("div", "chart-times"), el("span", "", time(first.time)), el("span", "", time(last.time))));
      grid.append(card);
    }
    return grid;
  }
  function sparkline(points, key, color, unit) {
    const ns = "http://www.w3.org/2000/svg", svg = document.createElementNS(ns, "svg");
    svg.setAttribute("class", "metric-chart"); svg.setAttribute("viewBox", "0 0 300 64"); svg.setAttribute("preserveAspectRatio", "none"); svg.setAttribute("role", "img"); svg.setAttribute("aria-label", points.map(point => `${time(point.time)}：${point[key]}${unit}`).join("；"));
    const max = Math.max(...points.map(point => point[key]), 1) * 1.15;
    const positions = points.map((point, index) => [points.length === 1 ? 150 : index / (points.length - 1) * 296 + 2, 60 - point[key] / max * 54]);
    const linePath = positions.map(([x, y], index) => `${index ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`).join(" ");
    const baseline = document.createElementNS(ns, "path"); baseline.setAttribute("d", "M0,60 L300,60"); baseline.setAttribute("stroke", "#edf0f4"); baseline.setAttribute("fill", "none"); svg.append(baseline);
    const area = document.createElementNS(ns, "path"); area.setAttribute("d", `${linePath} L${positions.at(-1)[0]},62 L${positions[0][0]},62 Z`); area.setAttribute("fill", color); area.setAttribute("fill-opacity", "0.06"); svg.append(area);
    const line = document.createElementNS(ns, "path"); line.setAttribute("d", linePath); line.setAttribute("fill", "none"); line.setAttribute("stroke", color); line.setAttribute("stroke-width", "1.8"); line.setAttribute("vector-effect", "non-scaling-stroke"); svg.append(line);
    const dot = document.createElementNS(ns, "circle"); dot.setAttribute("cx", positions.at(-1)[0]); dot.setAttribute("cy", positions.at(-1)[1]); dot.setAttribute("r", "2.7"); dot.setAttribute("fill", color); svg.append(dot);
    return svg;
  }
  function renderPlan(incident) {
    const plan = arr(incident.plan), completed = plan.filter(step => goodStates.has(step.status)).length;
    const panel = section("调查计划", plan.length ? `${completed} / ${plan.length} 步已完成` : "RUNBOOK EXECUTION");
    const agents = el("div", "agent-strip");
    for (const agent of arr(incident.agents)) { const chip = add(el("div", `agent-chip ${tone(agent.status)}`), el("span", "agent-dot"), el("span", "", agent.name)); chip.title = `${label(agent.status)}${agent.summary ? ` · ${agent.summary}` : ""}`; agents.append(chip); }
    if (agents.childElementCount) panel.append(agents);
    if (!plan.length) { panel.append(empty("开始调查后，Commander 将加载相关 Skill 并执行工具计划。")); return panel; }
    const list = el("ol", "plan-list");
    for (const step of plan) {
      const item = el("li", "plan-step");
      const body = add(el("div", ""), el("p", "step-title", step.title), add(el("div", "step-meta"), el("span", "", step.agent), el("code", "", step.tool)));
      if (step.error) body.append(el("p", "step-error", step.error));
      if (arr(step.evidence_ids).length) body.append(evidenceLinks(step.evidence_ids));
      add(item, el("span", `step-icon ${tone(step.status)}`, goodStates.has(step.status) ? "✓" : badStates.has(step.status) ? "!" : infoStates.has(step.status) ? "●" : ""), body, el("span", `step-state ${tone(step.status)}`, label(step.status)));
      list.append(item);
    }
    panel.append(list); return panel;
  }
  function confidenceText(value) { if (number(value) == null) return "—"; const score = value > 0 && value < 1 ? value * 100 : value; return `${Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format(score)}%`; }
  function evidenceLinks(ids, title, against = false) {
    const row = el("div", "evidence-links");
    if (title) row.append(el("span", "", title));
    for (const id of arr(ids)) row.append(button(str(id), `evidence-link ${against ? "against" : ""}`, () => showEvidence(str(id)), { focus: `ref-${title || ""}-${str(id)}` }));
    return row;
  }
  function renderHypotheses(incident) {
    const panel = section("根因假设", "ROOT CAUSE CANDIDATES");
    const hypotheses = arr(incident.hypotheses);
    if (!hypotheses.length) panel.append(empty("先收集证据，再建立与验证多个根因假设。"));
    hypotheses.forEach((hypothesis, index) => {
      const card = el("article", "hypothesis-card");
      add(card, add(el("div", "hypothesis-heading"), el("span", "hypothesis-rank", `0${index + 1}`), el("h3", "", hypothesis.title || label(hypothesis.type)), add(el("span", "confidence"), el("span", "confidence-label", "支持度"), el("span", "", confidenceText(hypothesis.confidence)))), el("p", "hypothesis-assessment", hypothesis.assessment));
      if (arr(hypothesis.evidence).length) card.append(evidenceLinks(hypothesis.evidence, "支持证据"));
      if (arr(hypothesis.evidence_against).length) card.append(evidenceLinks(hypothesis.evidence_against, "反向证据", true));
      panel.append(card);
    });
    if (hypotheses.length) panel.append(el("p", "method-note", "支持度是当前引擎对证据的评估值，未经概率校准；请结合原始证据与复核意见判断。"));
    if (incident.review?.status || arr(incident.review?.findings).length) {
      const review = add(el("div", "review-box"), add(el("div", "review-title"), el("span", "", "Reviewer · 结论复核"), pill(incident.review.status)));
      const findings = el("ul", ""); for (const finding of arr(incident.review.findings)) findings.append(el("li", "", listText(finding)));
      if (findings.childElementCount) review.append(findings); panel.append(review);
    }
    return panel;
  }
  function renderTopology(incident) {
    const panel = section("服务依赖", "SERVICE TOPOLOGY"), nodes = el("div", "topology-wrap");
    for (const node of arr(incident.topology?.nodes)) { const item = add(el("div", "topology-node"), el("span", `health-dot ${tone(node.health)}`), el("span", "", node.id), el("small", "", number(node.latency_ms) != null ? `${compact(node.latency_ms)} ms` : label(node.health))); item.title = `${node.id}：${label(node.health)}`; nodes.append(item); }
    panel.append(nodes);
    if (arr(incident.topology?.edges).length) panel.append(el("p", "topology-edges", arr(incident.topology.edges).map(edge => `${edge.from} → ${edge.to}`).join("   /   ")));
    return panel;
  }
  function renderTimeline(incident) {
    const events = arr(incident.timeline), panel = section("事故时间线", `${events.length} 条记录`), list = el("ol", "timeline");
    for (const event of events) add(list, add(el("li", "timeline-item"), el("time", "timeline-time", time(event.time)), el("span", "timeline-dot"), add(el("div", ""), el("p", "timeline-event", event.event), el("p", "timeline-actor", event.actor))));
    panel.append(events.length ? list : empty("工具调用、判断和审批记录将在这里保留。")); return panel;
  }
  function renderPostmortem(incident) {
    const panel = section("事故复盘", "POSTMORTEM"), pm = incident.postmortem;
    const fields = [["summary", "事故摘要"], ["impact", "影响范围"], ["timeline", "关键时间线"], ["root_cause", "根因"], ["evidence", "证据"], ["resolution", "处置结果"], ["preventive_actions", "预防措施"]];
    for (const [key, title] of fields) {
      const block = add(el("div", "postmortem-block"), el("h3", "", title));
      if (key === "evidence" && Array.isArray(pm[key])) {
        for (const entry of pm[key]) { if (arr(incident.evidence).some(evidence => evidence.id === entry)) block.append(evidenceLinks([entry])); else block.append(el("p", "", listText(entry))); }
        if (!pm[key].length) block.append(el("p", "", "暂无记录"));
      } else if (Array.isArray(pm[key])) { const list = el("ul", ""); for (const entry of pm[key]) list.append(el("li", "", listText(entry))); block.append(list.childElementCount ? list : el("p", "", "暂无记录")); }
      else block.append(el("p", "", pm[key] || "暂无记录"));
      panel.append(block);
    }
    if (incident.status === "resolved") panel.append(renderMemoryReview(incident));
    return panel;
  }
  function renderMemoryReview(incident) {
    const existing = state.memory.find(item => item.id === incident.memory_id), review = el("div", "memory-review");
    add(review, el("h3", "", "审阅事故经验"), el("p", "", incident.memory_id ? `记忆 ${incident.memory_id} · ${label(incident.memory_status || existing?.status || "candidate")}。更新质量标记后，后续调查将按状态选择经验。` : "确认复盘事实后，将经验标记为已确认或待验证。拒绝与过时的经验不会作为有效事实召回。"));
    const actions = el("div", "memory-review-actions"), select = el("select", ""); select.setAttribute("aria-label", "经验质量状态"); select.dataset.focus = "memory-quality";
    for (const option of ["confirmed", "probable", "rejected", "outdated"]) { const node = el("option", "", label(option)); node.value = option; select.append(node); }
    select.value = state.memoryReview; select.disabled = isBusy(); select.addEventListener("change", () => { state.memoryReview = select.value; });
    add(actions, select, button("保存审阅", "button secondary small", () => mutateIncident("/memory/review", { status: state.memoryReview, reviewer: "local-user" }, "经验质量标记已保存"), { disabled: isBusy(), focus: "memory-save" }));
    review.append(actions); return review;
  }
  function renderInspector(incident) {
    const target = $("#inspector"); target.replaceChildren();
    target.append(renderActions(incident));
    const evidenceSection = section("证据", `${arr(incident.evidence).length} 条 · 可追溯来源`, "inspector-section");
    if (!arr(incident.evidence).length) evidenceSection.append(empty("工具产生的观测事实将显示在这里。"));
    for (const evidence of arr(incident.evidence)) {
      const selected = state.selectedEvidence === evidence.id, item = el("article", `evidence-item ${selected ? "selected" : ""}`); item.dataset.evidenceId = evidence.id;
      const toggle = button("", "evidence-toggle", () => { state.selectedEvidence = selected ? null : evidence.id; renderInspector(incident); const current = $$("[data-focus]", target).find(node => node.dataset.focus === `evidence-${evidence.id}`); current?.focus({ preventScroll: true }); }, { focus: `evidence-${evidence.id}` });
      toggle.setAttribute("aria-expanded", selected ? "true" : "false");
      add(toggle, el("span", "evidence-id", evidence.id), add(el("span", ""), el("span", "evidence-summary", evidence.summary), el("span", "evidence-source", `${evidence.tool || evidence.kind || "observation"} · ${evidence.agent || "Commander"}`)), el("span", "evidence-chevron", selected ? "−" : "+")); item.append(toggle);
      if (selected) add(item, add(el("div", "evidence-detail"), el("p", "evidence-source", `来源：${evidence.source || "未提供"}`), el("p", "evidence-source", `采集时间：${time(evidence.time, true)}`), el("pre", "", pretty(evidence.content))));
      evidenceSection.append(item);
    }
    target.append(evidenceSection);
    const history = section("相似事故与经验", "MEMORY", "inspector-section");
    const matches = arr(incident.similar_incidents);
    if (!matches.length) history.append(empty("暂无召回结果。历史经验只作为线索，仍需用当前证据验证。"));
    for (const match of matches) {
      const item = el("article", "memory-match");
      add(item, add(el("div", "memory-match-head"), el("span", "", `${label(match.kind)} · ${match.id || ""}`), el("span", `memory-status ${match.status || ""}`, label(match.status))), el("h3", "", match.title), el("p", "", match.text || match.summary || match.excerpt), el("span", "evidence-source", match.source));
      history.append(item);
    }
    target.append(history);
    const runbooks = section("调查方法", "SKILLS & RUNBOOKS", "inspector-section");
    const sources = arr(incident.context?.sources);
    const relevant = sources.filter(source => /skill|runbook/i.test(str(source)));
    if (relevant.length) for (const source of relevant) runbooks.append(add(el("div", "runbook-chip"), el("span", "", "≡"), el("span", "", typeof source === "string" ? source : source.title || source.source || source.id || str(source))));
    else if (arr(incident.plan).length) { const tools = [...new Set(arr(incident.plan).map(step => step.tool).filter(Boolean))]; runbooks.append(empty("当前调查使用以下可执行工具步骤；完整执行状态见调查计划。")); const tags = el("div", "tag-row"); for (const tool of tools) tags.append(el("span", "soft-tag mono", tool)); runbooks.append(tags); }
    else runbooks.append(empty("Commander 将根据故障类型加载调查 Skill。"));
    target.append(runbooks);
    target.append(renderContext(incident));
  }
  function renderActions(incident) {
    const actions = section("恢复行动", "HUMAN IN THE LOOP", "inspector-section");
    if (!arr(incident.actions).length) { actions.append(empty("确认根因并完成复核后，生成有证据支持的恢复建议。")); return actions; }
    for (const action of arr(incident.actions)) {
      const card = el("article", "action-card");
      add(card, add(el("div", "action-heading"), el("h3", "", action.title || action.type), el("span", `risk-badge ${action.risk || ""}`, label(action.risk))), pill(action.status), el("p", "action-reason", action.reason));
      if (action.from_version || action.to_version) card.append(add(el("div", "action-versions mono"), el("span", "", action.from_version || "当前版本"), el("span", "muted", "→"), el("span", "", action.to_version || "目标版本")));
      if (arr(action.evidence).length) card.append(evidenceLinks(action.evidence, "依据"));
      const permitted = action.decision === "approval" && action.status === "proposed" && incident.status === "approval_required" && state.platform.simulation_actions !== false && !isBusy();
      if (action.status === "proposed" || action.decision === "blocked" || action.status === "blocked") card.append(button(action.decision === "blocked" || action.status === "blocked" ? "策略已阻止此操作" : isBusy() ? "正在执行…" : "批准并执行恢复", "button approve", () => mutateIncident(`/actions/${encodeURIComponent(action.id)}/approve`, { approved: true, revision: incident.revision }, "恢复操作状态已更新"), { disabled: !permitted, focus: `approve-${action.id}` }));
      add(card, el("p", "action-footnote", `策略：${label(action.decision)} · ${state.platform.simulation_actions === false ? "外部工具模式，平台禁止恢复写操作" : "操作作用于本地模拟场景"}`));
      actions.append(card);
    }
    return actions;
  }
  function renderContext(incident) {
    const context = record(incident.context), usage = record(incident.usage), panel = section("上下文预算", "CONTEXT BUILDER", "inspector-section");
    const stats = el("div", "context-stats");
    for (const [name, value] of [["原始字符", context.raw_chars], ["选取字符", context.selected_chars], ["预估上下文 tokens", context.estimated_tokens ?? usage.estimated_context_tokens], ["工具调用", usage.tool_calls]]) add(stats, add(el("div", "context-stat"), el("div", "value", compact(value)), el("div", "label", name)));
    panel.append(stats);
    if (number(context.raw_chars) > 0 && number(context.selected_chars) != null) { const bar = el("div", "context-bar"), fill = el("span", ""); fill.style.width = `${Math.max(0, Math.min(100, context.selected_chars / context.raw_chars * 100))}%`; bar.append(fill); panel.append(bar); }
    const parts = [];
    if (number(usage.input_tokens) != null) parts.push(`输入 ${compact(usage.input_tokens)} tokens`);
    if (number(usage.output_tokens) != null) parts.push(`输出 ${compact(usage.output_tokens)} tokens`);
    if (number(usage.duration_ms) != null) parts.push(`用时 ${compact(usage.duration_ms / 1000)} s`);
    panel.append(el("p", "context-note", parts.join(" · ") || "检索、筛选与压缩后构建工作上下文。"));
    if (arr(context.sources).length) { const details = el("details", "json-details"); add(details, el("summary", "", `${context.sources.length} 个上下文来源`), el("pre", "", pretty(context.sources))); panel.append(details); }
    return panel;
  }
  function showEvidence(id) {
    const exists = arr(state.current?.evidence).some(evidence => evidence.id === id);
    if (!exists) { toast(`当前事故未提供证据 ${id} 的详情`); return; }
    state.selectedEvidence = id; renderInspector(state.current);
    const node = $$("[data-evidence-id]").find(item => item.dataset.evidenceId === id);
    node?.scrollIntoView({ behavior: "auto", block: "nearest" });
    $("button", node)?.focus({ preventScroll: true });
  }
  function pageHeading(eyebrow, title, description, action) { return add(el("header", "page-heading"), add(el("div", ""), el("p", "eyebrow", eyebrow), el("h1", "", title), el("p", "", description)), action); }
  function summaryCard(name, value, description) { return add(el("article", "summary-card"), el("p", "label", name), el("p", "value", value), description ? el("p", "description", description) : null); }
  function renderMemory() {
    const target = $("#memory-view"); target.replaceChildren();
    add(target, pageHeading("INCIDENT MEMORY", "经验库", "事故复盘经过人工审阅后沉淀为经验。每条记录保留来源和质量状态，在后续调查中作为可验证的线索。", button("刷新经验", "button secondary", async () => { try { await loadMemory(); } catch (error) { showError(error); } })));
    const stats = el("div", "summary-grid");
    for (const status of ["confirmed", "probable", "rejected", "outdated"]) stats.append(summaryCard(label(status), state.memory.filter(item => item.status === status).length, status === "confirmed" ? "人工验证后的有效经验" : status === "probable" ? "仍需更多证据验证" : status === "rejected" ? "不作为有效事实召回" : "已不适用于当前系统"));
    target.append(stats);
    const filters = el("div", "filter-row");
    for (const status of ["all", "confirmed", "probable", "rejected", "outdated", "candidate"]) filters.append(button(status === "all" ? "全部" : label(status), `filter-button ${state.memoryFilter === status ? "active" : ""}`, () => { state.memoryFilter = status; renderMemory(); }));
    target.append(filters);
    const items = state.memory.filter(item => state.memoryFilter === "all" || item.status === state.memoryFilter), grid = el("div", "library-grid");
    for (const item of items) {
      const card = el("article", "library-card");
      add(card, add(el("div", "memory-match-head"), el("span", "mono", `${item.id || ""} · ${label(item.kind || "episodic")}`), el("span", `memory-status ${item.status || ""}`, label(item.status))), el("h2", "", item.title || item.pattern || item.root_cause || "事故经验"), el("p", "", item.text || item.content || item.summary || item.pattern || ""));
      const tags = el("div", "tag-row"); for (const value of [item.service, item.incident_type, item.environment]) if (value) tags.append(el("span", "soft-tag", value)); if (tags.childElementCount) card.append(tags);
      if (item.root_cause) add(card, el("p", "source", "根因"), el("p", "", item.root_cause));
      if (item.resolution) add(card, el("p", "source", "恢复方法"), el("p", "", item.resolution));
      add(card, el("p", "source", `${item.source || item.incident_id || "来源未提供"}${item.updated_at || item.created_at || item.date ? ` · ${time(item.updated_at || item.created_at || item.date, true)}` : ""}`), jsonDetails("完整记录", item));
      if (item.incident_id && state.incidents.some(incident => incident.id === item.incident_id)) card.append(button("查看原始事故 ↗", "button secondary small", () => selectIncident(item.incident_id)));
      grid.append(card);
    }
    if (items.length) target.append(grid); else add(target, add(el("div", "empty-page"), el("h2", "", "暂无符合条件的经验"), el("p", "", "完成事故恢复后，在复盘中审阅经验，即可将其加入后续检索。")));
  }
  function jsonDetails(title, data) { return add(el("details", "json-details"), el("summary", "", title), el("pre", "", pretty(data))); }
  function humanKey(key) {
    const names = { root_cause_accuracy: "根因准确率", top3_recall: "Top-3 召回率", top_3_recall: "Top-3 召回率", top3_root_cause_recall: "Top-3 根因召回率", evidence_precision: "证据精确率", tool_selection_accuracy: "工具选择准确率", recovery_success_rate: "恢复成功率", unsafe_action_rate: "不安全动作率", false_confidence_rate: "虚高信心率", mean_investigation_steps: "平均调查步骤", mean_steps: "平均调查步骤", token_cost: "Token 用量", mean_tokens: "平均 Tokens", mean_duration_ms: "平均耗时（ms）", time_to_diagnosis: "诊断耗时", cases: "案例数", total_cases: "案例总数", passed: "通过数", failed: "失败数", accuracy: "准确率", variant: "实验配置", name: "名称", status: "状态", id: "编号", scenario_id: "场景", predicted: "预测结果", expected: "预期结果", correct: "根因匹配", engine: "引擎", mode: "模式", tool_calls: "工具调用", estimated_tokens: "预估 Tokens", duration_ms: "耗时（ms）", with_rag: "启用 RAG", without_rag: "关闭 RAG", with_memory: "启用 Memory", without_memory: "关闭 Memory", with_context: "启用上下文处理", without_context: "关闭上下文处理", single: "单 Agent", multi: "多 Agent" };
    const additional = { baseline: "完整基线", no_rag: "关闭 RAG", no_memory: "关闭 Memory", no_context: "关闭上下文处理", single_agent: "单 Agent", metric: "评测指标", case_id: "案例", expected_cause: "预期根因", actual_cause: "实际判断", abstention_accuracy: "证据不足时的保留判断准确率", mean_estimated_context_tokens: "平均预估上下文 Tokens", input_tokens: "模型输入 Tokens", output_tokens: "模型输出 Tokens", cost_usd: "模型费用（USD）" };
    return names[key] || additional[key] || key.replaceAll("_", " ");
  }
  function formatCell(value, key = "") {
    if (value == null) return "—";
    if (typeof value === "boolean") return value ? "是" : "否";
    if (number(value) != null) { if (/accuracy|precision|recall|_rate$/.test(key)) return `${Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format(value <= 1 ? value * 100 : value)}%`; return compact(value); }
    if (typeof value === "object") return JSON.stringify(value);
    return label(value);
  }
  function dataTable(items) {
    const rows = arr(items).map(item => typeof item === "object" && item ? item : { value: item });
    const keys = [...new Set(rows.flatMap(Object.keys))];
    const table = el("table", "data-table"), head = el("thead", ""), header = el("tr", ""), body = el("tbody", "");
    for (const key of keys) { const cell = el("th", "", humanKey(key)); cell.scope = "col"; header.append(cell); } head.append(header);
    for (const row of rows) { const tr = el("tr", ""); for (const key of keys) { const cell = el("td", "", formatCell(row[key], key)); tr.append(cell); } body.append(tr); }
    add(table, head, body); return add(el("div", "table-wrap"), table);
  }
  function reportSection(title, data) {
    const panel = section(title);
    if (Array.isArray(data)) panel.append(data.length ? dataTable(data) : empty("暂无记录"));
    else if (data && typeof data === "object") { const entries = Object.entries(data); if (entries.every(([, value]) => typeof value !== "object" || value == null)) panel.append(dataTable(entries.map(([key, value]) => ({ name: humanKey(key), value: formatCell(value, key) })))); else panel.append(dataTable(entries.map(([name, value]) => typeof value === "object" && value && !Array.isArray(value) ? { name: humanKey(name), ...value } : { name: humanKey(name), value }))); }
    else panel.append(el("p", "muted", data));
    return panel;
  }
  function renderEvaluation() {
    const target = $("#evaluation-view"); target.replaceChildren();
    add(target, pageHeading("BENCHMARK & ABLATION", "系统评测", "比较根因判断、证据质量、恢复结果与调查成本。通过消融实验检验 RAG、Memory、上下文处理和 Agent 编排的实际效果。", button(state.evaluationBusy ? "评测运行中…" : "运行基准评测 ↗", "button primary", runEvaluation, { disabled: state.evaluationBusy })));
    if (state.evaluationBusy) target.append(el("p", "progress-note", "正在运行基准与消融实验。结果由实际执行统计生成，完成后自动更新。"));
    const report = state.evaluation;
    if (!report || report.status === "not_run") { add(target, add(el("div", "empty-page"), el("h2", "", "尚未运行基准评测"), el("p", "", "运行后可查看各类事故、噪声、缺失数据和冲突证据下的表现。"))); return; }
    const meta = el("p", "context-note", `引擎：${label(report.engine || report.mode || "offline")} · ${report.generated_at || report.created_at ? time(report.generated_at || report.created_at, true) : "已保存结果"}`); target.append(meta);
    if (arr(report.runs).length) {
      const runs = report.runs, baseline = runs.find(run => run.name === "baseline") || runs[0], metrics = record(baseline.metrics);
      const cards = el("div", "summary-grid");
      add(cards, summaryCard("事故案例", compact(report.case_count ?? baseline.cases), `${runs.length} 组实验配置`), summaryCard("根因准确率", formatCell(metrics.root_cause_accuracy, "root_cause_accuracy"), "完整基线"), summaryCard("证据精确率", formatCell(metrics.evidence_precision, "evidence_precision"), "完整基线"), summaryCard("不安全动作率", formatCell(metrics.unsafe_action_rate, "unsafe_action_rate"), "完整基线")); target.append(cards);
      const metricNames = [...new Set(runs.flatMap(run => Object.keys(record(run.metrics))))];
      const rows = metricNames.map(key => { const row = { metric: humanKey(key) }; for (const run of runs) row[run.name] = formatCell(run.metrics?.[key], key); return row; });
      const comparison = section("基线与消融对比", `${report.case_count ?? baseline.cases ?? "—"} 个案例 / 组`); comparison.append(dataTable(rows)); target.append(comparison);
      for (const run of runs) {
        const details = el("details", "panel json-details");
        add(details, el("summary", "", `${humanKey(run.name)} · ${arr(run.results).length} 条案例明细`));
        details.append(arr(run.results).length ? dataTable(run.results) : empty("暂无案例明细")); target.append(details);
      }
      if (arr(report.notes).length) { const notes = section("评测说明"); for (const note of report.notes) notes.append(el("p", "context-note", listText(note))); target.append(notes); }
      target.append(jsonDetails("查看完整评测数据", report));
      return;
    }
    const metrics = record(report.metrics || report.summary || report.aggregate);
    if (Object.keys(metrics).length) { const cards = el("div", "summary-grid"); for (const [key, value] of Object.entries(metrics)) if (typeof value !== "object" && value != null) cards.append(summaryCard(humanKey(key), formatCell(value, key))); target.append(cards); }
    if (report.ablations || report.ablation) target.append(reportSection("消融实验", report.ablations || report.ablation));
    if (report.cases || report.results) target.append(reportSection("事故案例明细", report.cases || report.results));
    const omit = new Set(["metrics", "summary", "aggregate", "ablations", "ablation", "cases", "results", "generated_at", "created_at", "engine", "mode"]);
    for (const [key, value] of Object.entries(report)) if (!omit.has(key) && value && typeof value === "object") target.append(reportSection(humanKey(key), value));
    target.append(jsonDetails("查看完整评测数据", report));
  }
  async function runEvaluation() {
    if (state.evaluationBusy) return;
    state.evaluationBusy = true; renderEvaluation();
    try { state.evaluation = await api("/evaluation", { method: "POST", body: {} }); toast("基准评测已完成"); } catch (error) { showError(error); }
    finally { state.evaluationBusy = false; renderEvaluation(); }
  }
  function renderPlatform() {
    const target = $("#platform-view"), platform = state.platform; target.replaceChildren();
    add(target, pageHeading("PLATFORM CAPABILITIES", "平台能力", "调查工具通过统一接口提供，Skills 定义具体调查步骤。风险策略决定动作自动执行、等待审批或被阻止。"));
    const stats = el("div", "summary-grid");
    add(stats, summaryCard("推理引擎", label(platform.mode), platform.model || "无需模型凭证的可复现规则基线"), summaryCard("已注册工具", arr(platform.tools).length, "调查与恢复工具"), summaryCard("调查 Skills", arr(platform.skills).length, "按故障类型加载"), summaryCard("模拟场景", arr(platform.scenarios).length, "独立事故状态与审批记录")); target.append(stats);
    const tools = section("工具目录", "MCP TOOL LAYER"), toolGrid = el("div", "capability-grid");
    for (const tool of arr(platform.tools)) add(toolGrid, add(el("article", "capability-card"), el("h3", "mono", tool.name), el("p", "", tool.description || ""), add(el("div", "tag-row"), el("span", "soft-tag", label(tool.risk)), tool.server ? el("span", "soft-tag", tool.server) : null)));
    tools.append(toolGrid.childElementCount ? toolGrid : empty("未返回工具注册信息。")); target.append(tools);
    const policy = section("动作风险策略", "APPROVAL POLICY"); policy.append(arr(platform.policy).length ? dataTable(platform.policy.map(item => ({ action: item.action, risk: label(item.risk), decision: label(item.decision) }))) : empty("未返回策略配置。")); target.append(policy);
    const skills = section("调查方法", "SKILLS"), skillGrid = el("div", "capability-grid");
    for (const skill of arr(platform.skills)) { const card = add(el("article", "capability-card"), el("h3", "", skill.title || skill.name || skill.id), el("p", "mono", skill.id)); if (skill.description) card.append(el("p", "", skill.description)); skillGrid.append(card); }
    skills.append(skillGrid.childElementCount ? skillGrid : empty("未返回 Skill 注册信息。")); target.append(skills);
    target.append(jsonDetails("平台状态原始数据", platform));
  }
  function changeView(view) {
    state.view = view;
    try { localStorage.setItem("incidentai-view", view); } catch { /* Browser storage is optional. */ }
    for (const name of ["workbench", "memory", "evaluation", "platform", "projects"]) $(`#${name}-view`).hidden = name !== view;
    for (const node of $$(".nav-button")) { node.classList.toggle("active", node.dataset.view === view); if (node.dataset.view === view) node.setAttribute("aria-current", "page"); else node.removeAttribute("aria-current"); }
    if (view === "memory") { renderMemory(); loadMemory().catch(showError); }
    if (view === "evaluation") { renderEvaluation(); if (!state.evaluationBusy) api("/evaluation").then(report => { state.evaluation = report; renderEvaluation(); }).catch(showError); }
    if (view === "platform") renderPlatform();
    if (view === "projects") window.IncidentLocalProjects?.show();
    $("#environment-label").textContent = view === "projects" ? "本地只读检查" : state.platform.simulation_actions === false ? "外部工具环境" : "本地模拟环境";
    $("#engine-label").textContent = view === "projects" ? "模型需单独授权" : label(state.platform.mode);
  }
  function openCreate(scenarioID) {
    const select = $("#scenario-select"); select.replaceChildren();
    for (const scenario of arr(state.platform.scenarios)) { const option = el("option", "", `${scenario.name || scenario.title || scenario.id} · ${scenario.service || "模拟场景"}`); option.value = scenario.id; select.append(option); }
    if (scenarioID) select.value = scenarioID;
    $("#incident-title").value = ""; updateScenarioDescription();
    $("#create-engine-hint").textContent = `当前引擎：${label(state.platform.mode)}${state.platform.model ? ` · ${state.platform.model}` : ""}。编排方式与推理引擎分别配置。`;
    $("#submit-create").disabled = !select.options.length;
    $("#create-dialog").showModal();
  }
  function updateScenarioDescription() { const scenario = arr(state.platform.scenarios).find(item => item.id === $("#scenario-select").value); $("#scenario-description").textContent = scenario?.description || ""; }
  async function createIncident(event) {
    event.preventDefault();
    const submit = $("#submit-create"); if (submit.disabled) return; submit.disabled = true; submit.textContent = "正在创建…";
    const form = new FormData($("#create-form"));
    try { const incident = await api("/incidents", { method: "POST", body: { scenario_id: form.get("scenario_id"), title: str(form.get("title")).trim(), mode: form.get("mode") } }); updateIncident(incident); $("#create-dialog").close(); state.selected = incident.id; state.current = incident; state.selectedEvidence = null; changeView("workbench"); renderList(); renderIncident(); toast("事故已创建，点击「开始调查」收集证据"); }
    catch (error) { showError(error); }
    finally { submit.disabled = false; submit.textContent = "创建事故 ↗"; }
  }
  async function init() {
    let savedView = "";
    try { savedView = localStorage.getItem("incidentai-view") || ""; } catch { /* Browser storage is optional. */ }
    $("#dismiss-error").addEventListener("click", () => { $("#error-banner").hidden = true; });
    $("#new-incident").addEventListener("click", () => openCreate());
    $("#close-create").addEventListener("click", () => $("#create-dialog").close()); $("#cancel-create").addEventListener("click", () => $("#create-dialog").close());
    $("#create-form").addEventListener("submit", createIncident); $("#scenario-select").addEventListener("change", updateScenarioDescription);
    $("#incident-search").addEventListener("input", renderList);
    $("#refresh-incidents").addEventListener("click", async () => { try { await loadIncidents(); if (state.selected) await selectIncident(state.selected); } catch (error) { showError(error); } });
    for (const nav of $$(".nav-button")) nav.addEventListener("click", () => changeView(nav.dataset.view));
    $("#add-local-project").addEventListener("click", () => { changeView("projects"); window.IncidentLocalProjects?.openCreate(); });
    const results = await Promise.allSettled([api("/status"), api("/incidents"), api("/memory")]);
    if (results[0].status === "fulfilled") {
      state.platform = results[0].value || {}; $("#engine-label").textContent = label(state.platform.mode); $("#engine-label").title = state.platform.model || label(state.platform.mode); $("#connection-label").textContent = "平台已连接"; $("#tool-count").textContent = `${arr(state.platform.tools).length} 个工具已注册`; $("#environment-label").textContent = state.platform.simulation_actions === false ? "外部工具环境" : "本地模拟环境";
    } else { $("#engine-label").textContent = "连接失败"; $("#connection-label").textContent = "服务不可用"; $("#tool-count").textContent = "请启动 V2 服务后刷新"; showError(results[0].reason); }
    if (results[1].status === "fulfilled") state.incidents = arr(results[1].value?.incidents || results[1].value); else showError(results[1].reason);
    if (results[2].status === "fulfilled") state.memory = arr(results[2].value?.items || results[2].value?.memories || results[2].value);
    state.loading = false; renderList();
    const restoreProjects = state.view === "projects" || savedView === "projects";
    if (state.incidents.length) await selectIncident(state.incidents[0].id); else renderWelcome();
    if (restoreProjects) changeView("projects");
  }
  init().catch(showError);
})();
