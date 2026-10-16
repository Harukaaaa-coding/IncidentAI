# Incident #002 — payment gateway degradation

## Symptom

Checkout timeouts increased with elevated gateway latency.

## Root Cause

The external payment gateway was slow. Retry configuration was unchanged.

## Resolution

Fail over to the secondary gateway after confirming the provider incident.
