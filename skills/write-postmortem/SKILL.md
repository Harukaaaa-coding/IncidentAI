# Write evidence-linked postmortem

After approval and recovery verification, record summary, impact, timeline, root cause with uncertainty, evidence, resolution and preventive actions. Separate observed facts from hypotheses. Draft a candidate episodic lesson; do not publish it as confirmed memory until a human verifies it. The write_postmortem coordinator step is a local artifact operation.

The executable plan is in plan.json. The coordinator supplies scenario_id and incident_id to every backend call. Empty search_code arguments require the observed dominant error as query. search_knowledge, plan_recovery and write_postmortem run inside the platform coordinator, not the external MCP server.
