"""Payment retry policy for checkout-service."""


class PaymentRetryExceeded(RuntimeError):
    pass


def retry_payment(operation, retry_limit):
    """Try a transient payment operation up to retry_limit times."""
    for _ in range(retry_limit):
        try:
            return operation()
        except TimeoutError:
            continue
    raise PaymentRetryExceeded("payment retries exhausted")
