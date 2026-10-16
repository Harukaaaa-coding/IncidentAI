# Initial incident triage

Establish impact before naming a cause. Collect metrics, logs, recent deployments, dependency health and topology. Record source tool, timestamp and incident identity for every observation. A recent release is correlation only. Do not execute recovery. Hand evidence to the relevant specialist and request at least two competing hypotheses.

The executable plan is in plan.json. The coordinator supplies scenario_id and incident_id to every backend call. Empty search_code arguments require the observed dominant error as query. search_knowledge, plan_recovery and write_postmortem run inside the platform coordinator, not the external MCP server.
