"""Tests for Data Agent configuration module."""

import os
import pytest
from unittest.mock import patch

from data_agent.config import DataAgentConfig
from data_agent.exceptions import ConfigurationError


class TestDataAgentConfig:
    """Test cases for DataAgentConfig."""

    def test_create_config_with_valid_credentials(self):
        """Test creating config with valid credentials."""
        config = DataAgentConfig(
            access_key_id="test_key_id",
            access_key_secret="test_key_secret",
        )

        assert config.access_key_id == "test_key_id"
        assert config.access_key_secret == "test_key_secret"
        assert config.region == "cn-hangzhou"
        assert config.endpoint == "dms.cn-hangzhou.aliyuncs.com"

    def test_create_config_with_custom_region(self):
        """Test creating config with custom region."""
        config = DataAgentConfig(
            access_key_id="test_key_id",
            access_key_secret="test_key_secret",
            region="cn-shanghai",
        )

        assert config.region == "cn-shanghai"
        assert config.endpoint == "dms.cn-shanghai.aliyuncs.com"

    def test_create_config_with_custom_endpoint(self):
        """Test creating config with custom endpoint."""
        config = DataAgentConfig(
            access_key_id="test_key_id",
            access_key_secret="test_key_secret",
            endpoint="custom.endpoint.com",
        )

        assert config.endpoint == "custom.endpoint.com"

    def test_create_config_missing_access_key_id(self):
        """Test that missing access_key_id raises error."""
        with pytest.raises(ConfigurationError) as exc_info:
            DataAgentConfig(
                access_key_id="",
                access_key_secret="test_key_secret",
            )

        assert "ALIBABA_CLOUD_ACCESS_KEY_ID" in str(exc_info.value)

    def test_create_config_missing_access_key_secret(self):
        """Test that missing access_key_secret raises error."""
        with pytest.raises(ConfigurationError) as exc_info:
            DataAgentConfig(
                access_key_id="test_key_id",
                access_key_secret="",
            )

        assert "ALIBABA_CLOUD_ACCESS_KEY_SECRET" in str(exc_info.value)

    def test_create_config_invalid_timeout(self):
        """Test that invalid timeout raises error."""
        with pytest.raises(ConfigurationError) as exc_info:
            DataAgentConfig(
                access_key_id="test_key_id",
                access_key_secret="test_key_secret",
                timeout=0,
            )

        assert "timeout" in str(exc_info.value).lower()

    def test_create_config_invalid_max_retry(self):
        """Test that negative max_retry raises error."""
        with pytest.raises(ConfigurationError) as exc_info:
            DataAgentConfig(
                access_key_id="test_key_id",
                access_key_secret="test_key_secret",
                max_retry=-1,
            )

        assert "max_retry" in str(exc_info.value).lower()

    def test_from_env_with_valid_env_vars(self):
        """Test loading config from environment variables."""
        env_vars = {
            "ALIBABA_CLOUD_ACCESS_KEY_ID": "env_key_id",
            "ALIBABA_CLOUD_ACCESS_KEY_SECRET": "env_key_secret",
            "DATA_AGENT_REGION": "cn-beijing",
            "DATA_AGENT_TIMEOUT": "600",
        }

        with patch.dict(os.environ, env_vars, clear=False):
            config = DataAgentConfig.from_env()

        assert config.access_key_id == "env_key_id"
        assert config.access_key_secret == "env_key_secret"
        assert config.region == "cn-beijing"
        assert config.timeout == 600

    def test_from_env_with_missing_credentials(self):
        """Test that missing env vars raises error."""
        with patch.dict(os.environ, {}, clear=True):
            with patch("data_agent.config.load_dotenv"):
                with pytest.raises(ConfigurationError):
                    DataAgentConfig.from_env()

    def test_from_dict(self):
        """Test creating config from dictionary."""
        config_dict = {
            "access_key_id": "dict_key_id",
            "access_key_secret": "dict_key_secret",
            "region": "cn-shenzhen",
            "timeout": 120,
        }

        config = DataAgentConfig.from_dict(config_dict)

        assert config.access_key_id == "dict_key_id"
        assert config.region == "cn-shenzhen"
        assert config.timeout == 120

    def test_to_dict_excludes_secrets(self):
        """Test that to_dict excludes sensitive information."""
        config = DataAgentConfig(
            access_key_id="secret_id",
            access_key_secret="secret_key",
        )

        result = config.to_dict()

        assert "access_key_id" not in result
        assert "access_key_secret" not in result
        assert "region" in result
        assert "endpoint" in result

    def test_repr_hides_secrets(self):
        """Test that repr doesn't expose secrets."""
        config = DataAgentConfig(
            access_key_id="secret_id",
            access_key_secret="secret_key",
        )

        repr_str = repr(config)

        assert "secret_id" not in repr_str
        assert "secret_key" not in repr_str
        assert "region" in repr_str
