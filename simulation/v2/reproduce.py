"""Read-only deterministic retry replay; no external requests or arbitrary input."""

import hashlib
import importlib.util
import json
from pathlib import Path


ROOT = Path(__file__).resolve().parent
COHORT = ((1, 600), (2, 180), (3, 170), (4, 30), (5, 12), (6, 8))
RETRY_PATH = ROOT / "repository" / "retry.py"


def load_retry():
    spec = importlib.util.spec_from_file_location("incidentai_fixture_retry", RETRY_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def config_limit(version):
    path = ROOT / "snapshots" / version / "config.yaml"
    for line in path.read_text(encoding="utf-8").splitlines():
        key, separator, value = line.partition(":")
        if separator and key.strip() == "retry_limit":
            limit = int(value.strip())
            if not 1 <= limit <= 100:
                raise ValueError("fixture retry_limit must be between 1 and 100")
            return limit
    raise ValueError("fixture is missing retry_limit")


def run_cohort(module, version):
    retry_limit = config_limit(version)
    failures = 0
    successes = 0
    total_attempts = 0
    observed_exceptions = {}
    for required_attempts, count in COHORT:
        for _ in range(count):
            attempted = 0

            def operation():
                nonlocal attempted
                attempted += 1
                if attempted < required_attempts:
                    raise TimeoutError("fixed synthetic transient timeout")
                return "payment-captured"

            try:
                module.retry_payment(operation, retry_limit)
                successes += 1
            except module.PaymentRetryExceeded as error:
                failures += 1
                name = type(error).__name__
                observed_exceptions[name] = observed_exceptions.get(name, 0) + 1
            total_attempts += attempted
    total = failures + successes
    return {
        "version": version,
        "retry_limit": retry_limit,
        "operations": total,
        "successes": successes,
        "failures": failures,
        "error_rate": failures / total * 100,
        "error_rate_unit": "percentage points (0-100)",
        "total_attempts": total_attempts,
        "observed_exceptions": observed_exceptions,
    }


def main():
    module = load_retry()
    current_bytes = RETRY_PATH.read_bytes()
    same_code = all(
        (ROOT / "snapshots" / version / "retry.py").read_bytes() == current_bytes
        for version in ("v2.8.0", "v2.8.1")
    )
    if not same_code:
        raise ValueError("retry.py is not identical in current repository and both snapshots")
    workload = [{"required_attempts": attempts, "operations": count} for attempts, count in COHORT]
    result = {
        "label": "synthetic replay",
        "scope": "Controlled simulation evidence; not a replay of measured production traffic",
        "source": "simulation/v2/repository/retry.py",
        "retry_code_sha256": hashlib.sha256(current_bytes).hexdigest(),
        "identical_retry_code_in_both_versions": same_code,
        "same_workload_for_both_versions": True,
        "workload": workload,
        "results": [run_cohort(module, "v2.8.0"), run_cohort(module, "v2.8.1")],
        "interpretation": "Only retry_limit changes in this controlled cohort. A successful replay establishes the simulated retry-budget mechanism; it does not establish real production timeout incidence.",
    }
    print(json.dumps(result, separators=(",", ":")))


if __name__ == "__main__":
    main()
