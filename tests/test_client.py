"""Tests for Data Agent client module."""

import pytest
from unittest.mock import Mock, patch, MagicMock

from data_agent.client import DataAgentClient, AsyncDataAgentClient
from data_agent.config import DataAgentConfig
from data_agent.models import SessionInfo, SessionStatus
from data_agent.exceptions import (
    ApiError,
    AuthenticationError,
    SessionCreationError,
)


class TestDataAgentClient:
    """Test cases for DataAgentClient."""

    @pytest.fixture
    def client(self, mock_config, mock_sdk_client):
        """Create a client with mocked SDK."""
        return DataAgentClient(mock_config)

    def test_init_creates_sdk_client(self, mock_config, mock_sdk_client):
        """Test that initialization creates SDK client."""
        client = DataAgentClient(mock_config)
        assert client._sdk_client is not None

    def test_create_session_success(self, client):
        """Test successful session creation."""
        mock_response = {
            "AgentId": "agent-123",
            "SessionId": "session-456",
        }

        with patch.object(client, "_call_api", return_value=mock_response):
            session = client.create_session()

        assert session.agent_id == "agent-123"
        assert session.session_id == "session-456"
        assert session.status == SessionStatus.CREATING

    def test_create_session_with_database_id(self, client):
        """Test session creation with database ID."""
        mock_response = {
            "AgentId": "agent-123",
            "SessionId": "session-456",
        }

        with patch.object(client, "_call_api", return_value=mock_response) as mock_call:
            session = client.create_session(database_id="db-789")

        # Verify database_id was passed
        call_args = mock_call.call_args
        assert "DatabaseId" in call_args[1]["params"]
        assert session.database_id == "db-789"

    def test_create_session_invalid_response(self, client):
        """Test session creation with invalid response."""
        mock_response = {"SomeOtherField": "value"}

        with patch.object(client, "_call_api", return_value=mock_response):
            with pytest.raises(SessionCreationError):
                client.create_session()

    def test_describe_session_running(self, client):
        """Test describing a running session."""
        mock_response = {
            "Status": "RUNNING",
            "DatabaseId": "db-123",
        }

        with patch.object(client, "_call_api", return_value=mock_response):
            session = client.describe_session("agent-id", "session-id")

        assert session.status == SessionStatus.RUNNING
        assert session.database_id == "db-123"

    def test_describe_session_creating(self, client):
        """Test describing a creating session."""
        mock_response = {"Status": "CREATING"}

        with patch.object(client, "_call_api", return_value=mock_response):
            session = client.describe_session("agent-id", "session-id")

        assert session.status == SessionStatus.CREATING

    def test_send_message(self, client):
        """Test sending a message."""
        mock_response = {"MessageId": "msg-123"}

        with patch.object(client, "_call_api", return_value=mock_response) as mock_call:
            result = client.send_message(
                agent_id="agent-id",
                session_id="session-id",
                message="Test query",
            )

        assert result == mock_response
        call_args = mock_call.call_args
        assert call_args[1]["params"]["Message"] == "Test query"

    def test_get_chat_content_with_checkpoint(self, client):
        """Test getting chat content with checkpoint."""
        mock_response = {
            "Contents": [{"Content": "Result", "ContentType": "text"}],
            "Checkpoint": "cp-new",
            "Finished": False,
        }

        with patch.object(client, "_call_api", return_value=mock_response) as mock_call:
            result = client.get_chat_content(
                agent_id="agent-id",
                session_id="session-id",
                checkpoint="cp-old",
            )

        assert result["Contents"][0]["Content"] == "Result"
        call_args = mock_call.call_args
        assert call_args[1]["params"]["Checkpoint"] == "cp-old"

    def test_handle_authentication_error(self, client):
        """Test handling authentication errors."""
        from Tea.exceptions import TeaException

        tea_error = TeaException({
            "code": "InvalidAccessKeyId.NotFound",
            "message": "Access key not found",
            "data": {"RequestId": "req-123"},
        })

        with pytest.raises(AuthenticationError) as exc_info:
            client._handle_tea_exception(tea_error)

        assert exc_info.value.code == "InvalidAccessKeyId.NotFound"
        assert exc_info.value.request_id == "req-123"

    def test_handle_generic_api_error(self, client):
        """Test handling generic API errors."""
        from Tea.exceptions import TeaException

        tea_error = TeaException({
            "code": "ServiceUnavailable",
            "message": "Service is temporarily unavailable",
            "data": {},
        })

        with pytest.raises(ApiError) as exc_info:
            client._handle_tea_exception(tea_error)

        assert exc_info.value.code == "ServiceUnavailable"


class TestAsyncDataAgentClient:
    """Test cases for AsyncDataAgentClient."""

    @pytest.fixture
    def async_client(self, mock_config, mock_sdk_client):
        """Create an async client."""
        return AsyncDataAgentClient(mock_config)

    @pytest.mark.asyncio
    async def test_create_session_async(self, async_client):
        """Test async session creation."""
        mock_session = SessionInfo(
            agent_id="agent-async",
            session_id="session-async",
            status=SessionStatus.CREATING,
        )

        with patch.object(
            async_client._sync_client,
            "create_session",
            return_value=mock_session,
        ):
            session = await async_client.create_session()

        assert session.agent_id == "agent-async"

    @pytest.mark.asyncio
    async def test_send_message_async(self, async_client):
        """Test async message sending."""
        mock_response = {"MessageId": "msg-async"}

        with patch.object(
            async_client._sync_client,
            "send_message",
            return_value=mock_response,
        ):
            result = await async_client.send_message(
                agent_id="agent-id",
                session_id="session-id",
                message="Async query",
            )

        assert result["MessageId"] == "msg-async"
