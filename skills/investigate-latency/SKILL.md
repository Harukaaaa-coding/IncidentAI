# Investigate latency and dependency failures

Determine whether latency originates in the application, cache, database or external payment provider. Check healthy sibling dependencies as evidence against broad infrastructure failure. Use provider error rate and cache error rate to distinguish two separate recovery paths. Never suggest rollback solely because errors increased.

The executable plan is in plan.json. The coordinator supplies scenario_id and incident_id to every backend call. Empty search_code arguments require the observed dominant error as query. search_knowledge, plan_recovery and write_postmortem run inside the platform coordinator, not the external MCP server.
