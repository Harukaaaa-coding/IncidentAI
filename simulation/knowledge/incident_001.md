# Incident #001 — payment retry exhaustion

## Symptom

Large numbers of `PaymentRetryExceeded` errors appeared immediately after a checkout deployment.

## Root Cause

The retry configuration was changed incorrectly, reducing the available retry budget during transient gateway failures.

## Resolution

Rollback the deployment and add a configuration-change review.
