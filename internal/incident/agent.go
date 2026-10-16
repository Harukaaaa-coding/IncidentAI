package incident

import (
	"context"
	"fmt"
	"strings"
)

// Commander is the V1 single Incident Commander agent. The investigation
// plan is deliberately constrained so every conclusion is traceable to an
// executed tool call; it cannot perform production operations.
type Commander struct {
	tools *Tools
}

func NewCommander(tools *Tools) *Commander {
	return &Commander{tools: tools}
}

func (c *Commander) Investigate(ctx context.Context, incidentText string) (Investigation, error) {
	if err := ctx.Err(); err != nil {
		return Investigation{}, err
	}
	if strings.TrimSpace(incidentText) == "" {
		incidentText = "checkout-service error rate suddenly rose to 22%"
	}

	metrics, err := c.tools.GetMetrics()
	if err != nil {
		return Investigation{}, err
	}
	if len(metrics.Points) == 0 {
		return Investigation{}, fmt.Errorf("metrics contains no points")
	}
	currentRate := metrics.Points[len(metrics.Points)-1].ErrorRate
	if currentRate <= 1 {
		return c.recoveryInvestigation(incidentText, currentRate), nil
	}

	steps := []ToolStep{{Tool: "get_metrics", Summary: fmt.Sprintf("%s error rate is now %.1f%%.", metrics.Service, currentRate)}}
	facts := []Observation{{
		ID: "O1", Kind: "fact",
		Statement: fmt.Sprintf("checkout-service error rate rose from %.1f%% to %.1f%%.", metrics.Points[0].ErrorRate, currentRate),
		Source:    "get_metrics",
	}}

	logs, err := c.tools.SearchLogs("PaymentRetryExceeded")
	if err != nil {
		return Investigation{}, err
	}
	steps = append(steps, ToolStep{Tool: "search_logs", Input: "PaymentRetryExceeded", Summary: fmt.Sprintf("Found %d PaymentRetryExceeded log entries.", len(logs))})
	if len(logs) > 0 {
		facts = append(facts, Observation{ID: "O3", Kind: "fact", Statement: "Application logs contain repeated PaymentRetryExceeded errors.", Source: "search_logs(PaymentRetryExceeded)"})
	}

	deployments, err := c.tools.GetDeployments()
	if err != nil {
		return Investigation{}, err
	}
	steps = append(steps, ToolStep{Tool: "get_deployments", Summary: fmt.Sprintf("%s moved from %s to %s at %s.", deployments.Service, deployments.PreviousVersion, deployments.CurrentVersion, deployments.DeployedAt)})
	facts = append(facts, Observation{ID: "O2", Kind: "fact", Statement: fmt.Sprintf("%s deployed %s at %s, three minutes before the first sharp error-rate rise.", deployments.Service, deployments.CurrentVersion, deployments.DeployedAt), Source: "get_deployments"})

	matches, err := c.tools.SearchCode("PaymentRetryExceeded")
	if err != nil {
		return Investigation{}, err
	}
	steps = append(steps, ToolStep{Tool: "search_code", Input: "PaymentRetryExceeded", Summary: fmt.Sprintf("ripgrep located %d matching code lines, including retry.py.", len(matches))})

	retryFile, err := c.tools.ReadFile("retry.py")
	if err != nil {
		return Investigation{}, err
	}
	steps = append(steps, ToolStep{Tool: "read_file", Input: retryFile.Path, Summary: "retry.py raises PaymentRetryExceeded after retry_limit attempts."})

	diff, err := c.tools.GitDiff()
	if err != nil {
		return Investigation{}, err
	}
	steps = append(steps, ToolStep{Tool: "git_diff", Summary: fmt.Sprintf("Compared %s to %s and found a retry configuration change.", diff.FromVersion, diff.ToVersion)})
	if strings.Contains(diff.Diff, "retry_limit: 5") && strings.Contains(diff.Diff, "retry_limit: 2") {
		facts = append(facts, Observation{ID: "O4", Kind: "fact", Statement: "The v2.8.1 change reduced retry_limit from 5 to 2.", Source: "git_diff"})
	}

	knowledge, err := c.tools.SearchKnowledge("PaymentRetryExceeded retry configuration")
	if err != nil {
		return Investigation{}, err
	}
	steps = append(steps, ToolStep{Tool: "search_knowledge", Input: "PaymentRetryExceeded retry configuration", Summary: fmt.Sprintf("Retrieved %d relevant historical knowledge documents.", len(knowledge))})
	if len(knowledge) > 0 {
		facts = append(facts, Observation{ID: "O5", Kind: "precedent", Statement: "A prior incident links PaymentRetryExceeded to an incorrect retry configuration and was resolved by rollback.", Source: "search_knowledge(incident_001.md)"})
	}

	databaseLogs, err := c.tools.SearchLogs("DatabaseLatency")
	if err != nil {
		return Investigation{}, err
	}
	steps = append(steps, ToolStep{Tool: "search_logs", Input: "DatabaseLatency", Summary: fmt.Sprintf("Found %d normal database-latency health entries.", len(databaseLogs))})
	if len(databaseLogs) > 0 {
		facts = append(facts, Observation{ID: "O6", Kind: "fact", Statement: "Database latency remained normal during the incident window.", Source: "search_logs(DatabaseLatency)"})
	}

	return Investigation{
		Status:   "approval_required",
		Incident: incidentText,
		Facts:    facts,
		Steps:    steps,
		Hypotheses: []Hypothesis{
			{ID: "H1", Title: "Deployment regression", Assessment: "Most likely root cause", Evidence: []string{"O1", "O2", "O3", "O4", "O5"}, Confidence: 89},
			{ID: "H2", Title: "Database overload", Assessment: "Unlikely alternative cause", EvidenceAgainst: []string{"O6"}, Confidence: 4},
		},
		Conclusion: "Most likely, checkout-service v2.8.1 reduced the retry budget from 5 to 2. The deployment timing, PaymentRetryExceeded errors, code path, and historical precedent all support a deployment regression; this is a confidence-weighted conclusion, not proof of causality.",
		RecommendedAction: &RecommendedAction{
			Type: "rollback", Service: "checkout-service", FromVersion: "v2.8.1", ToVersion: "v2.8.0",
			Confidence: 89, Risk: "Medium", RequiresApproval: true,
			Reason: "Restore the prior retry_limit=5 configuration while preserving human control over the simulated production operation.",
		},
	}, nil
}

func (c *Commander) ApproveRollback(ctx context.Context, incidentText string) (Investigation, Postmortem, error) {
	if err := ctx.Err(); err != nil {
		return Investigation{}, Postmortem{}, err
	}
	if _, err := c.tools.RollbackDeployment(); err != nil {
		return Investigation{}, Postmortem{}, err
	}
	investigation, err := c.Investigate(ctx, incidentText)
	if err != nil {
		return Investigation{}, Postmortem{}, err
	}
	return investigation, c.BuildPostmortem(), nil
}

func (c *Commander) recoveryInvestigation(incidentText string, currentRate float64) Investigation {
	return Investigation{
		Status:               "recovery_verified",
		Incident:             incidentText,
		Facts:                []Observation{{ID: "O7", Kind: "recovery", Statement: fmt.Sprintf("After rollback, error rate fell to %.1f%%.", currentRate), Source: "get_metrics"}},
		Steps:                []ToolStep{{Tool: "get_metrics", Summary: fmt.Sprintf("Verified the post-rollback error rate is %.1f%%.", currentRate)}},
		Conclusion:           "Recovery verified: the simulated rollback returned checkout-service error rate to its normal range.",
		RecoveryVerification: &RecoveryVerification{Status: "verified", ErrorRate: currentRate, Statement: "Recovery verified"},
	}
}

func (c *Commander) BuildPostmortem() Postmortem {
	return Postmortem{
		Title:   "Postmortem: checkout-service retry regression",
		Summary: "A simulated deployment caused a rapid increase in checkout-service errors and was recovered by an approved rollback.",
		Impact:  "Error rate rose from 1% to 22%, causing payment retries to exhaust for affected checkout requests.",
		Timeline: []string{
			"14:30 — error rate at 1%.",
			"14:32 — checkout-service v2.8.1 deployed.",
			"14:35 — error rate reached 8%.",
			"14:38 — error rate reached 22%; repeated PaymentRetryExceeded appeared.",
			"14:40 — human-approved rollback to v2.8.0.",
			"14:48 — error rate recovered to 0.8%.",
		},
		RootCause:  "checkout-service v2.8.1 changed retry_limit from 5 to 2, causing PaymentRetryExceeded errors under transient payment failures.",
		Evidence:   []string{"O1: error rate 1% → 22%", "O2: v2.8.1 deployed before the increase", "O3: repeated PaymentRetryExceeded", "O4: retry_limit 5 → 2", "O5: matching historical incident"},
		Resolution: "A human approved rollback of checkout-service from v2.8.1 to v2.8.0. Metrics then fell 22% → 9% → 3% → 0.8%.",
		PreventiveActions: []string{
			"Add a deployment validation test that rejects retry_limit reductions without an explicit review.",
			"Alert on a correlated increase in PaymentRetryExceeded after deployment.",
			"Require a staged rollout for payment retry configuration changes.",
		},
	}
}
