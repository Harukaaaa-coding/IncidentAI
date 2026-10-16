# Investigate memory and CPU pressure

Correlate memory and CPU pressure with replica failures. Resource pressure does not establish an actual memory leak without growth profiles. Describe the observed resource exhaustion as probable, retain dependency alternatives, and recommend only a temporary replica restart while the leak diagnosis remains unconfirmed.

The executable plan is in plan.json. The coordinator supplies scenario_id and incident_id to every backend call. Empty search_code arguments require the observed dominant error as query. search_knowledge, plan_recovery and write_postmortem run inside the platform coordinator, not the external MCP server.
