"""Bounded payment retry policy in the checkout snapshot."""

class PaymentRetryExceeded(RuntimeError):
    pass

def retry_payment(operation, retry_limit):
    for _ in range(retry_limit):
        try:
            return operation()
        except TimeoutError:
            continue
    raise PaymentRetryExceeded("payment retries exhausted")
