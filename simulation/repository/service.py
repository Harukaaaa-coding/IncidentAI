"""checkout-service entry point used by the IncidentAI simulation."""

from retry import PaymentRetryExceeded, retry_payment


def capture_payment(gateway, config):
    """Capture payment through a gateway that can transiently fail."""
    try:
        return retry_payment(gateway.capture, config["retry_limit"])
    except PaymentRetryExceeded:
        # The production service reports this event to structured logs.
        raise
