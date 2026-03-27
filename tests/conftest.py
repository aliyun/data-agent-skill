"""Pytest fixtures for Data Agent tests.

Author: Tinker
Created: 2026-03-01
"""

import pytest
from unittest.mock import Mock, MagicMock, patch
from datetime import datetime

from data_agent.config import DataAgentConfig
from data_agent.models import SessionInfo, SessionStatus, ContentBlock, ContentType


@pytest.fixture
def mock_config():
    """Create a mock configuration using API_KEY auth (no credential chain needed)."""
    return DataAgentConfig(
        api_key="test_api_key_for_testing",
        region="cn-hangzhou",
        timeout=300,
        max_retry=3,
        poll_interval=1,
        max_poll_count=10,
    )


@pytest.fixture
def mock_session_info():
    """Create a mock session info."""
    return SessionInfo(
        agent_id="test-agent-id",
        session_id="test-session-id",
        status=SessionStatus.RUNNING,
        database_id=None,
        created_at=datetime.now(),
        last_used_at=datetime.now(),
    )


@pytest.fixture
def mock_content_blocks():
    """Create mock content blocks."""
    return [
        ContentBlock(
            content_type=ContentType.TEXT,
            content="This is the analysis result.",
            checkpoint="cp1",
            is_final=False,
        ),
        ContentBlock(
            content_type=ContentType.TABLE,
            content="| Column1 | Column2 |\n|---------|---------|",
            checkpoint="cp2",
            is_final=False,
        ),
        ContentBlock(
            content_type=ContentType.TEXT,
            content="Summary: The data shows positive trends.",
            checkpoint="cp3",
            is_final=True,
        ),
    ]


@pytest.fixture
def mock_sdk_client():
    """Create a mock SDK client."""
    with patch("data_agent.client.OpenApiClient") as mock:
        yield mock


@pytest.fixture
def mock_api_response():
    """Factory for creating mock API responses."""
    def _create_response(data: dict):
        return {"body": data}
    return _create_response
