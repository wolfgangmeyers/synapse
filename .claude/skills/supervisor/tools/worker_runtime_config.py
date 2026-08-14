"""Machine-local managed-worker runtime configuration."""

from __future__ import annotations

import json
import os
from typing import Mapping

ALLOWED_AGENT_RUNTIMES = ("pi", "claude", "codex", "cursor")
DEFAULT_AGENT_RUNTIME = "pi"
WORKER_RUNTIME_CONFIG_ENV = "MECHA_WORKER_RUNTIME_CONFIG"
DEFAULT_WORKER_RUNTIME_CONFIG_PATH = os.path.expanduser(
    "~/.mecha-developer/worker-runtime.json"
)


class WorkerRuntimeConfigError(ValueError):
    """Raised when worker runtime configuration is invalid."""


def _validate_runtime(value: object, *, source: str) -> str:
    if not isinstance(value, str) or value not in ALLOWED_AGENT_RUNTIMES:
        allowed = ", ".join(ALLOWED_AGENT_RUNTIMES)
        raise WorkerRuntimeConfigError(
            f"{source} default_agent must be one of: {allowed}"
        )
    return value


def worker_runtime_config_path(environment: Mapping[str, str] | None = None) -> str:
    environment = os.environ if environment is None else environment
    return os.path.abspath(
        os.path.expanduser(
            environment.get(WORKER_RUNTIME_CONFIG_ENV, DEFAULT_WORKER_RUNTIME_CONFIG_PATH)
        )
    )


def _load_config(
    *, environment: Mapping[str, str] | None = None, path: str | None = None
) -> tuple[dict, str] | tuple[None, str]:
    environment = os.environ if environment is None else environment
    config_path = (
        os.path.abspath(os.path.expanduser(path))
        if path
        else worker_runtime_config_path(environment)
    )
    try:
        with open(config_path, "r", encoding="utf-8") as handle:
            payload = json.load(handle)
    except FileNotFoundError:
        return None, config_path
    except json.JSONDecodeError as error:
        raise WorkerRuntimeConfigError(
            f"worker runtime config is not valid JSON: {config_path}"
        ) from error

    if not isinstance(payload, dict):
        raise WorkerRuntimeConfigError(
            f"worker runtime config must be a JSON object: {config_path}"
        )
    return payload, config_path


def _validate_model(value: object, *, source: str, key: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise WorkerRuntimeConfigError(
            f"{source} {key} must be a non-empty string"
        )
    return value.strip()


def _validate_cursor_model(value: object, *, source: str) -> str:
    return _validate_model(value, source=source, key="cursor_model")


def _validate_pi_model(value: object, *, source: str) -> str:
    return _validate_model(value, source=source, key="pi_model")


def load_default_agent_runtime(
    *,
    environment: Mapping[str, str] | None = None,
    path: str | None = None,
) -> str:
    """Return the machine-local default managed-worker runtime.

    Missing config preserves the historical managed-worker default: Pi with the
    launcher's pinned provider/model. Explicit ``worker_launch.py --agent`` still
    overrides this value.
    """
    payload, config_path = _load_config(environment=environment, path=path)
    if payload is None:
        return DEFAULT_AGENT_RUNTIME
    if "default_agent" not in payload:
        raise WorkerRuntimeConfigError(
            f"worker runtime config must define default_agent: {config_path}"
        )
    return _validate_runtime(payload["default_agent"], source=config_path)


def load_cursor_model(
    *,
    environment: Mapping[str, str] | None = None,
    path: str | None = None,
) -> str | None:
    """Return the optional Cursor model, or None when it is not configured."""
    payload, config_path = _load_config(environment=environment, path=path)
    if payload is None or "cursor_model" not in payload:
        return None
    return _validate_cursor_model(payload["cursor_model"], source=config_path)


def load_pi_model(
    *,
    environment: Mapping[str, str] | None = None,
    path: str | None = None,
) -> str | None:
    """Return the optional machine-local Pi model, or None when it is not configured."""
    payload, config_path = _load_config(environment=environment, path=path)
    if payload is None or "pi_model" not in payload:
        return None
    return _validate_pi_model(payload["pi_model"], source=config_path)


def resolve_agent_runtime(
    explicit_agent: str | None,
    *,
    environment: Mapping[str, str] | None = None,
) -> str:
    """Resolve the worker runtime from explicit CLI value or machine config."""
    if explicit_agent is not None:
        return _validate_runtime(explicit_agent, source="--agent")
    return load_default_agent_runtime(environment=environment)
