"""Configuration management for Data Agent SDK.

Author: Tinker
Created: 2026-03-01
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import Optional

from dotenv import load_dotenv

from data_agent.exceptions import ConfigurationError


@dataclass
class DataAgentConfig:
    """Configuration for Data Agent client.

    Attributes:
        access_key_id: Alibaba Cloud Access Key ID
        access_key_secret: Alibaba Cloud Access Key Secret
        security_token: Alibaba Cloud Security Token (STS Token, optional)
        region: Region for DMS endpoint (default: cn-hangzhou)
        endpoint: Custom endpoint (auto-generated if not set)
        timeout: API timeout in seconds (default: 300)
        max_retry: Maximum retry attempts (default: 3)
        poll_interval: Interval between polls in seconds (default: 2)
        max_poll_count: Maximum poll attempts (default: 60)
    """

    access_key_id: str
    access_key_secret: str
    security_token: Optional[str] = None
    region: str = "cn-hangzhou"
    endpoint: Optional[str] = None
    timeout: int = 300
    max_retry: int = 3
    poll_interval: int = 2
    max_poll_count: int = 60

    def __post_init__(self) -> None:
        """Generate endpoint if not provided and validate config."""
        if not self.endpoint:
            self.endpoint = f"dms.{self.region}.aliyuncs.com"
        self.validate()

    def validate(self) -> None:
        """Validate configuration values.

        Raises:
            ConfigurationError: If required configuration is missing or invalid.
        """
        if not self.access_key_id:
            raise ConfigurationError(
                "Missing ALIBABA_CLOUD_ACCESS_KEY_ID. "
                "Set it via environment variable or pass it explicitly."
            )
        if not self.access_key_secret:
            raise ConfigurationError(
                "Missing ALIBABA_CLOUD_ACCESS_KEY_SECRET. "
                "Set it via environment variable or pass it explicitly."
            )
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

        Raises:
            ConfigurationError: If required environment variables are missing.
        """
        if dotenv_path:
            load_dotenv(dotenv_path)
        else:
            load_dotenv()

        return cls(
            access_key_id=os.environ.get("ALIBABA_CLOUD_ACCESS_KEY_ID", ""),
            access_key_secret=os.environ.get("ALIBABA_CLOUD_ACCESS_KEY_SECRET", ""),
            security_token=os.environ.get("ALIBABA_CLOUD_SECURITY_TOKEN") or os.environ.get("SECURITY_TOKEN"),
            region=os.environ.get("DATA_AGENT_REGION", "cn-hangzhou"),
            endpoint=os.environ.get("DATA_AGENT_ENDPOINT"),
            timeout=int(os.environ.get("DATA_AGENT_TIMEOUT", "300")),
            max_retry=int(os.environ.get("DATA_AGENT_MAX_RETRY", "3")),
            poll_interval=int(os.environ.get("DATA_AGENT_POLL_INTERVAL", "2")),
            max_poll_count=int(os.environ.get("DATA_AGENT_MAX_POLL_COUNT", "60")),
        )

    @classmethod
    def from_dict(cls, config_dict: dict) -> DataAgentConfig:
        """Create configuration from a dictionary.

        Args:
            config_dict: Dictionary containing configuration values.

        Returns:
            DataAgentConfig instance.
        """
        return cls(
            access_key_id=config_dict.get("access_key_id", ""),
            access_key_secret=config_dict.get("access_key_secret", ""),
            security_token=config_dict.get("security_token"),
            region=config_dict.get("region", "cn-hangzhou"),
            endpoint=config_dict.get("endpoint"),
            timeout=config_dict.get("timeout", 300),
            max_retry=config_dict.get("max_retry", 3),
            poll_interval=config_dict.get("poll_interval", 2),
            max_poll_count=config_dict.get("max_poll_count", 60),
        )

    def to_dict(self) -> dict:
        """Convert configuration to dictionary (excluding secrets).

        Returns:
            Dictionary with non-sensitive configuration values.
        """
        return {
            "region": self.region,
            "endpoint": self.endpoint,
            "timeout": self.timeout,
            "max_retry": self.max_retry,
            "poll_interval": self.poll_interval,
            "max_poll_count": self.max_poll_count,
        }

    def __repr__(self) -> str:
        """String representation (hides secrets)."""
        return (
            f"DataAgentConfig(region='{self.region}', endpoint='{self.endpoint}', "
            f"timeout={self.timeout}, max_retry={self.max_retry})"
        )
