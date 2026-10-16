# Prepare governed recovery

Select the smallest action supported by current evidence. Reads are automatic; local notes are low risk; replica restart requires approval; production rollback is high risk and requires approval; database mutations are blocked. Produce the proposed action, risk, evidence IDs, expected result and verification criteria. The plan_recovery coordinator step only proposes an action and cannot execute it.

The executable plan is in plan.json. The coordinator supplies scenario_id and incident_id to every backend call. Empty search_code arguments require the observed dominant error as query. search_knowledge, plan_recovery and write_postmortem run inside the platform coordinator, not the external MCP server.
