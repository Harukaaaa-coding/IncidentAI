# Checkout service runbook

When `PaymentRetryExceeded` rises, check in this order:

1. Error-rate timeline.
2. Recent checkout-service deployments.
3. Retry configuration diff and gateway latency.
4. Database latency before attributing the problem to database overload.

Rollback requires human approval and post-rollback metric verification.
