# Checkout architecture

checkout-service calls the payment gateway. Transient `TimeoutError` failures pass through the retry policy in `retry.py`. The policy reads `retry_limit` from `config.yaml`. Exhausting that budget emits `PaymentRetryExceeded`.
