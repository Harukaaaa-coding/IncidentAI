const $ = (id) => document.getElementById(id);
let latestIncident = $("incident").value;

function escapeHtml(value) {
  return String(value).replace(/[&<>'"]/g, (char) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;"
  })[char]);
}

function setStatus(message, isError = false) {
  const target = $("status");
  target.textContent = message;
  target.classList.toggle("error", isError);
}

async function request(url, options = {}) {
  const response = await fetch(url, options);
  const body = await response.json();
  if (!response.ok) throw new Error(body.error || "Request failed");
  return body;
}

function list(target, values, render = (value) => escapeHtml(value)) {
  $(target).innerHTML = values.map((value) => `<li>${render(value)}</li>`).join("");
}

function renderInvestigation(result) {
  $("result").hidden = false;
  $("result-status").textContent = result.status.replaceAll("_", " ");
  $("conclusion").textContent = result.conclusion;
  list("steps", result.steps, (step) => `<strong>${escapeHtml(step.tool)}</strong>${step.input ? ` · ${escapeHtml(step.input)}` : ""}<br><span>${escapeHtml(step.summary)}</span>`);
  list("facts", result.facts, (fact) => `<strong>${escapeHtml(fact.id)} · ${escapeHtml(fact.kind)}</strong><br>${escapeHtml(fact.statement)}<br><span>${escapeHtml(fact.source)}</span>`);
  $("hypotheses").innerHTML = result.hypotheses.map((hypothesis) => `
    <div class="hypothesis">
      <div><strong>${escapeHtml(hypothesis.id)} · ${escapeHtml(hypothesis.title)}</strong><span>${escapeHtml(hypothesis.assessment)}</span></div>
      <b>${escapeHtml(hypothesis.confidence)}%</b>
      <small>${hypothesis.evidence?.length ? `Evidence: ${escapeHtml(hypothesis.evidence.join(", "))}` : ""}${hypothesis.evidence_against?.length ? ` Against: ${escapeHtml(hypothesis.evidence_against.join(", "))}` : ""}</small>
    </div>`).join("");

  const action = result.recommended_action;
  $("recommendation").hidden = !action;
  if (action) {
    $("version").textContent = `current ${action.from_version}`;
    $("action-title").textContent = `Rollback ${action.service}: ${action.from_version} → ${action.to_version}`;
    $("action-reason").textContent = action.reason;
    $("confidence").textContent = `Confidence: ${action.confidence}%`;
    $("risk").textContent = `Risk: ${action.risk}`;
  }
  if (result.recovery_verification) {
    $("version").textContent = "current v2.8.0";
    setStatus(`${result.recovery_verification.statement}: error rate is ${result.recovery_verification.error_rate}%.`);
  }
}

function renderPostmortem(postmortem) {
  $("postmortem").hidden = false;
  $("postmortem-title").textContent = postmortem.title;
  $("postmortem-root").textContent = postmortem.root_cause;
  list("timeline", postmortem.timeline);
  list("preventive", postmortem.preventive_actions);
}

async function refreshTools() {
  const calls = [
    ["metrics", "/api/tools/metrics"],
    ["logs", "/api/tools/logs?q=PaymentRetryExceeded"],
    ["deployments", "/api/tools/deployments"],
    ["code", "/api/tools/code?q=PaymentRetryExceeded"],
    ["diff", "/api/tools/diff"],
    ["knowledge", "/api/tools/knowledge?q=PaymentRetryExceeded%20retry%20configuration"]
  ];
  try {
    const results = await Promise.all(calls.map(async ([id, url]) => [id, await request(url)]));
    results.forEach(([id, result]) => { $(id).textContent = JSON.stringify(result, null, 2); });
  } catch (error) {
    setStatus(error.message, true);
  }
}

$("investigate").addEventListener("click", async () => {
  latestIncident = $("incident").value;
  setStatus("Collecting metrics, logs, deployments, code, diff, and historical evidence…");
  $("investigate").disabled = true;
  try {
    const result = await request("/api/investigate", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ incident: latestIncident }) });
    renderInvestigation(result);
    setStatus("Investigation complete. Rollback is awaiting human approval.");
    await refreshTools();
  } catch (error) {
    setStatus(error.message, true);
  } finally {
    $("investigate").disabled = false;
  }
});

$("approve").addEventListener("click", async () => {
  if (!confirm("Approve the simulated rollback from v2.8.1 to v2.8.0?")) return;
  $("approve").disabled = true;
  setStatus("Human approval received. Running simulated rollback and verifying recovery…");
  try {
    const result = await request("/api/rollback/approve", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ incident: latestIncident }) });
    renderInvestigation(result.investigation);
    renderPostmortem(result.postmortem);
    await refreshTools();
  } catch (error) {
    setStatus(error.message, true);
  } finally {
    $("approve").disabled = false;
  }
});

$("reset").addEventListener("click", async () => {
  try {
    await request("/api/reset", { method: "POST" });
    $("result").hidden = true;
    $("postmortem").hidden = true;
    setStatus("Simulation reset. checkout-service is back on v2.8.1 at 22% error rate.");
    await refreshTools();
  } catch (error) {
    setStatus(error.message, true);
  }
});

$("refresh-tools").addEventListener("click", refreshTools);
refreshTools();
