"""Tests for Data Agent configuration module."""

import os
import pytest
from unittest.mock import patch

from data_agent.config import DataAgentConfig
from data_agent.exceptions import ConfigurationError


class TestDataAgentConfig:
    """Test cases for DataAgentConfig."""

    def test_create_config_default_credential_chain(self):
        """Test creating config without API key uses default credential chain."""
        config = DataAgentConfig()

        assert config.api_key is None
        assert config.region == "cn-hangzhou"
        assert config.endpoint == "dms.cn-hangzhou.aliyuncs.com"
        assert config.to_dict()["auth_type"] == "default_credential_chain"

    def test_create_config_with_api_key(self):
        """Test creating config with API key."""
        config = DataAgentConfig(
            api_key="test_api_key",
            region="cn-hangzhou",
        )

        assert config.api_key == "test_api_key"
        assert config.to_dict()["auth_type"] == "api_key"

    def test_create_config_with_custom_region(self):
        """Test creating config with custom region."""
        config = DataAgentConfig(region="cn-shanghai")

        assert config.region == "cn-shanghai"
        assert config.endpoint == "dms.cn-shanghai.aliyuncs.com"

    def test_create_config_with_custom_endpoint(self):
        """Test creating config with custom endpoint."""
        config = DataAgentConfig(endpoint="custom.endpoint.com")

        assert config.endpoint == "custom.endpoint.com"

    def test_create_config_api_key_endpoint(self):
        """Test that API key config generates correct endpoint."""
        config = DataAgentConfig(
            api_key="test_key",
            region="cn-hangzhou",
        )

        assert config.endpoint == "dataagent-cn-hangzhou.aliyuncs.com/apikey"

    def test_create_config_invalid_timeout(self):
        """Test that invalid timeout raises error."""
        with pytest.raises(ConfigurationError) as exc_info:
            DataAgentConfig(timeout=0)

        assert "timeout" in str(exc_info.value).lower()

    def test_create_config_invalid_max_retry(self):
        """Test that negative max_retry raises error."""
        with pytest.raises(ConfigurationError) as exc_info:
            DataAgentConfig(max_retry=-1)

        assert "max_retry" in str(exc_info.value).lower()

    def test_from_env_with_valid_env_vars(self):
        """Test loading config from environment variables."""
        env_vars = {
            "DATA_AGENT_REGION": "cn-beijing",
            "DATA_AGENT_TIMEOUT": "600",
        }

        with patch.dict(os.environ, env_vars, clear=False):
            config = DataAgentConfig.from_env()

        assert config.region == "cn-beijing"
        assert config.timeout == 600

    def test_from_env_with_api_key(self):
        """Test loading config with API key from env."""
        env_vars = {
            "DATA_AGENT_API_KEY": "env_api_key",
            "DATA_AGENT_REGION": "cn-beijing",
        }

        with patch.dict(os.environ, env_vars, clear=False):
            config = DataAgentConfig.from_env()

        assert config.api_key == "env_api_key"
        assert config.region == "cn-beijing"

    def test_from_dict(self):
        """Test creating config from dictionary."""
        config_dict = {
            "api_key": "dict_api_key",
            "region": "cn-shenzhen",
            "timeout": 120,
        }

        config = DataAgentConfig.from_dict(config_dict)

        assert config.api_key == "dict_api_key"
        assert config.region == "cn-shenzhen"
        assert config.timeout == 120

    def test_to_dict_excludes_secrets(self):
        """Test that to_dict excludes sensitive information."""
        config = DataAgentConfig(api_key="secret_key")

        result = config.to_dict()

        assert "api_key" not in result
        assert "region" in result
        assert "endpoint" in result
        assert "auth_type" in result

    def test_repr_hides_secrets(self):
        """Test that repr doesn't expose secrets."""
        config = DataAgentConfig(api_key="secret_key")

        repr_str = repr(config)

        assert "secret_key" not in repr_str
        assert "region" in repr_str
