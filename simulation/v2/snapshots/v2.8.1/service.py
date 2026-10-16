"""Reference checkout code, inspected by rg/read_file during investigation."""
from retry import retry_payment

def capture_payment(gateway, config):
    return retry_payment(gateway.capture, config["retry_limit"])

class ConnectionPoolExhausted(RuntimeError):
    """Raised when the database pool cannot allocate a connection."""

class RedisConnectionError(RuntimeError):
    """Raised when the cache dependency cannot be reached."""

class PaymentProviderUnavailable(RuntimeError):
    """Raised when the upstream payment provider rejects requests."""

class MemoryPressure(RuntimeError):
    """Raised when the replica exceeds its resource watermark."""
