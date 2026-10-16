# Investigate database overload

Check database latency and pool utilization together with ConnectionPoolExhausted logs. Verify whether there was a recent deployment. A high pool percentage alone is not sufficient proof. Prefer reversible pool-pressure mitigation for this simulation; database mutation remains blocked.

The executable plan is in plan.json. The coordinator supplies scenario_id and incident_id to every backend call. Empty search_code arguments require the observed dominant error as query. search_knowledge, plan_recovery and write_postmortem run inside the platform coordinator, not the external MCP server.
