"""Configuration management for Data Agent SDK.

Author: Tinker
Created: 2026-03-01
"""

from __future__ import annotations

import json
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional

from dotenv import load_dotenv

from data_agent.exceptions import ConfigurationError


DEFAULT_CREDENTIALS_FILENAME = ".data-agent-credentials.json"


def _none_if_blank(value: Optional[str]) -> Optional[str]:
    """Normalize blank optional strings to None."""
    if value is None:
        return None
    value = str(value).strip()
    return value or None


def _load_credentials_entry(
    credentials_file: Optional[str],
    env_name: str,
    profile_name: str,
) -> Optional[tuple[str, str, dict]]:
    """Load the (env, profile) entry from the plain credentials file.

    Resolution: ``credentials_file`` (DATA_AGENT_CREDENTIALS_FILE) when set,
    otherwise ``~/{DEFAULT_CREDENTIALS_FILENAME}``.

    Returns None when no file exists at the implicit default location so the
    client can silently fall back to the default credential chain. When the
    requested key is the implicit ``default`` and the file offers exactly one
    candidate (one environment, or one profile under the selected
    environment), that candidate is auto-selected; ambiguous or missing keys
    raise with the complete environment/profile listing in a single error.
    Returns the resolved ``(env, profile, entry)`` tuple.

    Raises:
        ConfigurationError: If the credentials file is missing (explicit path),
            unreadable, malformed, or lacks the requested environment/profile.
    """
    if credentials_file:
        path = Path(credentials_file).expanduser()
        if not path.is_file():
            raise ConfigurationError(f"DATA_AGENT_CREDENTIALS_FILE not found: {path}")
    else:
        path = Path.home() / DEFAULT_CREDENTIALS_FILENAME
        if not path.is_file():
            return None

    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as e:
        raise ConfigurationError(f"Failed to read credentials file {path}: {e}")
    if not isinstance(data, dict):
        raise ConfigurationError(f"Credentials file {path} is malformed: expected a JSON object.")

    envs = {
        key: sorted(name for name in value if isinstance(value.get(name), dict))
        for key, value in data.items()
        if isinstance(value, dict)
    }
    env_map = data.get(env_name)
    if not isinstance(env_map, dict):
        if env_name == "default" and len(envs) == 1:
            env_name = next(iter(envs))
            env_map = data[env_name]
        else:
            listing = ", ".join(
                f"{env} (profiles: {', '.join(profiles) or 'none'})"
                for env, profiles in sorted(envs.items())
            )
            raise ConfigurationError(
                f"Credentials file {path} has no environment '{env_name}'. "
                f"Available: {listing or 'none'}. "
                "Set DATA_AGENT_CREDENTIALS_ENV and DATA_AGENT_CREDENTIALS_PROFILE accordingly."
            )
    entry = env_map.get(profile_name)
    if not isinstance(entry, dict):
        profiles = sorted(name for name in env_map if isinstance(env_map.get(name), dict))
        if profile_name == "default" and len(profiles) == 1:
            profile_name = profiles[0]
            entry = env_map[profile_name]
        else:
            raise ConfigurationError(
                f"Credentials file {path} has no profile '{profile_name}' under '{env_name}'. "
                f"Available profiles: {', '.join(profiles) or 'none'}. "
                "Set DATA_AGENT_CREDENTIALS_PROFILE accordingly."
            )
    return env_name, profile_name, entry


@dataclass
class DataAgentConfig:
    """Configuration for Data Agent client.

    Attributes:
        api_key: API Key for alternative authentication (optional)
        region: Region for DMS endpoint (default: cn-hangzhou)
        endpoint: Custom endpoint (auto-generated if not set)
        timeout: API timeout in seconds (default: 300)
        max_retry: Maximum retry attempts (default: 3)
        poll_interval: Interval between polls in seconds (default: 2)
        max_poll_count: Maximum poll attempts (default: 60)
        workspace_id: Optional workspace ID from DATA_AGENT_WORKSPACE_ID
        dms_unit: Optional DMSUnit override from DATA_AGENT_DMS_UNIT
        custom_agent_id: Optional custom agent ID from DATA_AGENT_CUSTOM_AGENT_ID
        dms_enterprise_endpoint: Optional dms-enterprise host for the 2018-11-01
            metadata APIs (database/table/instance discovery, GetActiveRouteUnit).
            Defaults to dms-enterprise.{region}.aliyuncs.com. Set it for VPC-only
            egress or non-public-cloud deployments. Note that ``endpoint`` only
            covers the Data Agent APIs, not these metadata calls.
        credentials_file: Optional path to a plain credentials JSON file
            (DATA_AGENT_CREDENTIALS_FILE; defaults to ~/.data-agent-credentials.json)
        credentials_env: Environment key inside the credentials file. The
            implicit ``default`` auto-selects the only environment when the
            file is unambiguous; after init this field holds the resolved key.
        credentials_profile: Profile (nickname) inside the environment. The
            implicit ``default`` auto-selects the only profile when the
            environment is unambiguous; after init this field holds the
            resolved key.

    Note:
        For AK/SK authentication, the SDK uses Alibaba Cloud default credential chain
        (environment variables, ~/.aliyun/config.json, instance role, etc.)
        Do NOT pass AK/SK explicitly to this config. To use specific credentials
        without touching the global chain, put them in the credentials file instead
        (see references/CREDENTIALS.md).
    """

    api_key: Optional[str] = None
    region: str = "cn-hangzhou"
    endpoint: Optional[str] = None
    timeout: int = 300
    max_retry: int = 3
    poll_interval: int = 2
    max_poll_count: int = 60
    workspace_id: Optional[str] = None
    dms_unit: Optional[str] = None
    custom_agent_id: Optional[str] = None
    dms_enterprise_endpoint: Optional[str] = None
    credentials_file: Optional[str] = None
    credentials_env: str = "default"
    credentials_profile: str = "default"

    def __post_init__(self) -> None:
        """Generate endpoint if not provided and validate config."""
        self.workspace_id = _none_if_blank(self.workspace_id)
        self.dms_unit = _none_if_blank(self.dms_unit)
        self.custom_agent_id = _none_if_blank(self.custom_agent_id)
        self.dms_enterprise_endpoint = _none_if_blank(self.dms_enterprise_endpoint)
        self.credentials_file = _none_if_blank(self.credentials_file)

        # Resolve the credentials-file entry before endpoint derivation: an
        # entry carrying "api_key" switches this config into API_KEY mode.
        # credentials_env / credentials_profile are updated to the resolved
        # keys (auto-selection may pick the only candidate when unambiguous).
        self._credentials_entry: Optional[dict] = None
        resolved = _load_credentials_entry(
            self.credentials_file, self.credentials_env, self.credentials_profile
        )
        if resolved is not None:
            self.credentials_env, self.credentials_profile, self._credentials_entry = resolved
        self.using_credentials_file = self._credentials_entry is not None
        entry_api_key = _none_if_blank(
            (self._credentials_entry or {}).get("api_key")
        )
        if not self.api_key and entry_api_key:
            self.api_key = entry_api_key
        if self._credentials_entry and not entry_api_key:
            for required in ("access_key_id", "access_key_secret"):
                if not _none_if_blank(self._credentials_entry.get(required)):
                    raise ConfigurationError(
                        f"Credentials profile '{self.credentials_env}/"
                        f"{self.credentials_profile}' must provide either 'api_key' "
                        f"or both 'access_key_id' and 'access_key_secret'."
                    )

        if not self.endpoint:
            if self.api_key:
                # For API key auth, use the dataagent domain format with /apikey suffix
                self.endpoint = f"dataagent-{self.region}.aliyuncs.com/apikey"
            else:
                # Standard DMS endpoint for AK/SK auth (default chain or credentials file)
                self.endpoint = f"dms.{self.region}.aliyuncs.com"
        self.validate()

    def resolve_dms_enterprise_endpoint(self, region_id: Optional[str] = None) -> str:
        """Return the effective dms-enterprise host for the 2018-11-01 APIs.

        The configured override wins; otherwise the host is derived from the
        region (``region_id`` when given, else the configured region).

        Args:
            region_id: Region to derive the default host from.

        Returns:
            dms-enterprise hostname.
        """
        if self.dms_enterprise_endpoint:
            return self.dms_enterprise_endpoint
        return f"dms-enterprise.{region_id or self.region}.aliyuncs.com"

    def validate(self) -> None:
        """Validate configuration values.

        Raises:
            ConfigurationError: If required configuration is missing or invalid.
        """
        # API key is optional - if not provided, SDK will use default credential chain
        pass

        if self.timeout <= 0:
            raise ConfigurationError(f"Invalid timeout value: {self.timeout}. Must be positive.")
        if self.max_retry < 0:
            raise ConfigurationError(f"Invalid max_retry value: {self.max_retry}. Must be non-negative.")
        if self.poll_interval <= 0:
            raise ConfigurationError(f"Invalid poll_interval value: {self.poll_interval}. Must be positive.")
        if self.max_poll_count <= 0:
            raise ConfigurationError(f"Invalid max_poll_count value: {self.max_poll_count}. Must be positive.")

    @classmethod
    def from_env(cls, dotenv_path: Optional[str] = None) -> DataAgentConfig:
        """Create configuration from environment variables.

        Args:
            dotenv_path: Optional path to .env file to load.

        Returns:
            DataAgentConfig instance.

        Note:
            AK/SK credentials are NOT read from environment variables here.
            The Alibaba Cloud SDK uses its default credential chain.
        """
        if dotenv_path:
            load_dotenv(dotenv_path)
        else:
            load_dotenv()

        return cls(
            api_key=os.environ.get("DATA_AGENT_API_KEY") or None,
            region=os.environ.get("DATA_AGENT_REGION", "cn-hangzhou"),
            endpoint=os.environ.get("DATA_AGENT_ENDPOINT"),
            timeout=int(os.environ.get("DATA_AGENT_TIMEOUT", "300")),
            max_retry=int(os.environ.get("DATA_AGENT_MAX_RETRY", "3")),
            poll_interval=int(os.environ.get("DATA_AGENT_POLL_INTERVAL", "2")),
            max_poll_count=int(os.environ.get("DATA_AGENT_MAX_POLL_COUNT", "60")),
            workspace_id=os.environ.get("DATA_AGENT_WORKSPACE_ID"),
            dms_unit=os.environ.get("DATA_AGENT_DMS_UNIT") or os.environ.get("DATA_AGENT_DMSUNIT"),
            custom_agent_id=os.environ.get("DATA_AGENT_CUSTOM_AGENT_ID"),
            dms_enterprise_endpoint=os.environ.get("DATA_AGENT_DMS_ENTERPRISE_ENDPOINT"),
            credentials_file=os.environ.get("DATA_AGENT_CREDENTIALS_FILE"),
            credentials_env=os.environ.get("DATA_AGENT_CREDENTIALS_ENV", "default"),
            credentials_profile=os.environ.get("DATA_AGENT_CREDENTIALS_PROFILE", "default"),
        )

    @classmethod
    def from_dict(cls, config_dict: dict) -> DataAgentConfig:
        """Create configuration from a dictionary.

        Args:
            config_dict: Dictionary containing configuration values.

        Returns:
            DataAgentConfig instance.
        
        Note:
            Do NOT pass AK/SK credentials in config_dict.
            Use Alibaba Cloud default credential chain instead.
        """
        return cls(
            api_key=config_dict.get("api_key"),
            region=config_dict.get("region", "cn-hangzhou"),
            endpoint=config_dict.get("endpoint"),
            timeout=config_dict.get("timeout", 300),
            max_retry=config_dict.get("max_retry", 3),
            poll_interval=config_dict.get("poll_interval", 2),
            max_poll_count=config_dict.get("max_poll_count", 60),
            workspace_id=config_dict.get("workspace_id") or config_dict.get("WorkspaceId"),
            dms_unit=config_dict.get("dms_unit") or config_dict.get("DMSUnit"),
            custom_agent_id=config_dict.get("custom_agent_id") or config_dict.get("CustomAgentId"),
            dms_enterprise_endpoint=config_dict.get("dms_enterprise_endpoint"),
            credentials_file=config_dict.get("credentials_file"),
            credentials_env=config_dict.get("credentials_env", "default"),
            credentials_profile=config_dict.get("credentials_profile", "default"),
        )

    def to_dict(self) -> dict:
        """Convert configuration to dictionary (excluding secrets).

        Returns:
            Dictionary with non-sensitive configuration values.
        """
        result = {
            "region": self.region,
            "endpoint": self.endpoint,
            "timeout": self.timeout,
            "max_retry": self.max_retry,
            "poll_interval": self.poll_interval,
            "max_poll_count": self.max_poll_count,
        }
        if self.workspace_id:
            result["workspace_id"] = self.workspace_id
        if self.dms_unit:
            result["dms_unit"] = self.dms_unit
        if self.custom_agent_id:
            result["custom_agent_id"] = self.custom_agent_id
        if self.dms_enterprise_endpoint:
            result["dms_enterprise_endpoint"] = self.dms_enterprise_endpoint
        if self.credentials_file:
            result["credentials_file"] = self.credentials_file
        if self.credentials_env != "default":
            result["credentials_env"] = self.credentials_env
        if self.credentials_profile != "default":
            result["credentials_profile"] = self.credentials_profile
        # Only indicate auth type
        if self.api_key:
            result["auth_type"] = "api_key"
        elif self._credentials_entry:
            result["auth_type"] = "credential_file"
        else:
            result["auth_type"] = "default_credential_chain"
        return result

    def __repr__(self) -> str:
        """String representation (hides secrets)."""
        return (
            f"DataAgentConfig(region='{self.region}', endpoint='{self.endpoint}', "
            f"timeout={self.timeout}, max_retry={self.max_retry}, "
            f"dms_unit={self.dms_unit!r})"
        )
