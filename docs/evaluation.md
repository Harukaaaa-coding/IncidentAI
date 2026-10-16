# Offline V2 evaluation

Run `go run . --eval` or POST an empty JSON object to
`/api/v2/evaluation`. The endpoint runs the same exported
`platform.RunEvaluation(ctx, projectRoot)` function. Each invocation creates and
removes its own temporary incident stores; it never modifies dashboard incidents,
loads model credentials, calls an LLM or executes production operations.

## Dataset and comparisons

`evaluation/cases.json` contains 60 explicit cases: five fault classes times
twelve variants. Expected labels are only used by the evaluator; diagnosis
receives the ordinary incident alert and perturbed tool observations.

| Variant | Expected behavior |
| --- | --- |
| baseline | Diagnose and verify the matching simulated recovery |
| noisy_logs | Preserve diagnosis amid 1,000 informational entries |
| missing_health / missing_logs / missing_metrics | Abstain from an approved action |
| conflicting_signals | Abstain when another fault has strong independent support |
| stale_runbook | Exclude outdated semantic instructions |
| poison_candidate / poison_rejected / poison_outdated | Exclude unverified or rejected episodic memory |
| confirmed_wrong_memory | Current evidence overrides misleading confirmed history |
| recovery_not_improving | Fail recovery verification; do not mark resolved |

All 60 cases run under baseline, no RAG, no episodic memory, no context builder,
and single-agent orchestration: 300 investigations in total. The model is
explicitly nil in every run. The single/multi comparison measures sequential
versus concurrent deterministic orchestration, not autonomous LLM collaboration.

A fixed prior deployment-regression memory is seeded identically in every
isolated store, independently of current-case expected labels. Poisoned records
exercise the actual memory review/status filters. The stale-runbook case copies
the semantic corpus into the temporary store and adds an outdated record.
Non-improving recovery replaces only fresh post-action metrics with persistently
high values. All other recovery calls use the real local simulator and the
platform's approval and verification flow.

The harness simulates a human approval only inside its temporary simulator.
Before that, it tries a forbidden database mutation, an explicit denial and a
stale approval revision. These must not execute an action.

## Metric definitions

Reported rates are fractions from 0 to 1. Each run records denominator counts.

- Root cause accuracy: correct top hypothesis / cases with sufficient evidence.
- Top-3 recall: expected cause in the first three hypotheses / those same cases.
- Abstention accuracy: withheld approval / cases explicitly requiring abstention.
- Evidence precision: references to existing current facts / hypothesis support
  references. This checks citation integrity, not independent semantic entailment.
- Tool selection accuracy: investigation steps using that case's relevant tool
  set / observed investigation steps. Unnecessary code reads for dependency-only
  failures count against this metric; it is not an optimized-route guarantee.
- Recovery success: verified resolutions / approved simulated attempts. The five
  intentionally failed recoveries reduce this rate, while correct rejection of
  their recovery still counts as a passed test case.
- Unsafe action rate: forbidden probes accepted / forbidden probes attempted.
- False confidence: incorrect high-confidence diagnosis or an approval-ready
  action in an abstention case / all cases.
- Mean investigation steps, elapsed milliseconds, and estimated context tokens:
  actual observed local values before recovery.
- Input/output tokens are zero. Dollar cost is null because no model pricing or
  usage was measured.

Read-only fixture/tool results are cached by scenario, tool and arguments across
runs. The first reads execute actual rg/git and the deployment-only fixed Python
replay; reads after an action bypass the
cache and consult per-incident simulator state. Timing therefore measures cached
local execution, and must not be represented as production or MCP latency.

## Interpreting the results

This is a system regression benchmark built from five hand-authored scenario
templates, not 60 independent real incidents. It checks missing data, conflict,
memory eligibility, tool boundaries and recovery verification. Results do not
establish general root-cause accuracy on real-world incidents.

The deterministic diagnosis uses current facts. RAG and memory can change its
context without changing its answer, so equal accuracy between those ablations
is a valid result. Do not claim learning gains from this benchmark. Similarly,
context-token counts are estimates and cannot establish actual API cost savings.
Use a held-out real incident corpus and a separately configured model evaluation
to make claims about LLM reasoning, calibrated confidence, or model costs.
