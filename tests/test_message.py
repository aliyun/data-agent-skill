"""Tests for Data Agent message module."""

import pytest
from unittest.mock import Mock, patch, MagicMock

from data_agent.message import MessageHandler, AsyncMessageHandler
from data_agent.client import DataAgentClient
from data_agent.sse_client import SSEClient, SSEEvent
from data_agent.models import SessionInfo, SessionStatus, ContentBlock, ContentType
from data_agent.exceptions import MessageSendError, ContentFetchError


class TestMessageHandler:
    """Test cases for MessageHandler."""

    @pytest.fixture
    def mock_client(self, mock_config):
        """Create a mock client."""
        client = Mock(spec=DataAgentClient)
        client.config = mock_config
        return client

    @pytest.fixture
    def mock_sse_client(self):
        """Create a mock SSE client."""
        return Mock(spec=SSEClient)

    @pytest.fixture
    def message_handler(self, mock_client, mock_sse_client):
        """Create a message handler with mock client and SSE client."""
        handler = MessageHandler(mock_client)
        handler._sse_client = mock_sse_client
        return handler

    @pytest.fixture
    def running_session(self):
        """Create a running session."""
        return SessionInfo(
            agent_id="agent-1",
            session_id="session-1",
            status=SessionStatus.RUNNING,
        )

    def test_send_query_success(self, message_handler, mock_client, mock_sse_client, running_session):
        """Test sending a query and receiving response."""
        mock_client.send_message.return_value = {"MessageId": "msg-1"}
        mock_sse_client.get_full_response.return_value = "Analysis result"

        result = message_handler.send_query(running_session, "Test query")

        assert result == "Analysis result"
        mock_client.send_message.assert_called_once_with(
            agent_id="agent-1",
            session_id="session-1",
            message="Test query",
            data_source=None,
        )
        mock_sse_client.get_full_response.assert_called_once()

    def test_send_query_send_failure(self, message_handler, mock_client, running_session):
        """Test error handling when message send fails."""
        mock_client.send_message.side_effect = Exception("Send failed")

        with pytest.raises(MessageSendError):
            message_handler.send_query(running_session, "Test query")

    def test_send_query_with_timeout(self, message_handler, mock_client, mock_sse_client, running_session):
        """Test query with custom timeout."""
        mock_client.send_message.return_value = {}
        mock_sse_client.get_full_response.return_value = "Result"

        result = message_handler.send_query(running_session, "Test", timeout=60)

        assert result == "Result"
        mock_sse_client.get_full_response.assert_called_with(
            agent_id="agent-1",
            session_id="session-1",
            timeout=60,
        )

    def test_send_query_sse_failure(self, message_handler, mock_client, mock_sse_client, running_session):
        """Test error handling when SSE streaming fails."""
        mock_client.send_message.return_value = {}
        mock_sse_client.get_full_response.side_effect = Exception("SSE failed")

        with pytest.raises(ContentFetchError):
            message_handler.send_query(running_session, "Test query")

    def test_send_query_with_result(self, message_handler, mock_client, mock_sse_client, running_session):
        """Test getting detailed result with content blocks."""
        mock_client.send_message.return_value = {}
        mock_sse_client.stream_chat_content.return_value = iter([
            SSEEvent(event_type="delta", data={}, category="llm", content="Hello"),
            SSEEvent(event_type="delta", data={}, category="llm", content=" World"),
            SSEEvent(event_type="SSE_FINISH", data={}, category="content", content=""),
        ])

        result = message_handler.send_query_with_result(running_session, "Test")

        assert result.query == "Test"
        assert "Hello" in result.response
        assert result.duration_ms is not None

    def test_stream_content(self, message_handler, mock_client, mock_sse_client, running_session):
        """Test streaming content blocks."""
        mock_client.send_message.return_value = {}
        mock_sse_client.stream_chat_content.return_value = iter([
            SSEEvent(event_type="delta", data={}, category="llm", content="Part 1", checkpoint=1),
            SSEEvent(event_type="delta", data={}, category="llm", content="Part 2", checkpoint=2),
            SSEEvent(event_type="SSE_FINISH", data={}, category="content", content=""),
        ])

        blocks = list(message_handler.stream_content(running_session, "Test"))

        assert len(blocks) == 2
        assert blocks[0].content == "Part 1"
        assert blocks[1].content == "Part 2"

    def test_stream_events(self, message_handler, mock_client, mock_sse_client, running_session):
        """Test streaming raw SSE events."""
        mock_client.send_message.return_value = {}
        events = [
            SSEEvent(event_type="HEARTBEAT", data={}, category="content", content=""),
            SSEEvent(event_type="delta", data={}, category="llm", content="Hello"),
            SSEEvent(event_type="SSE_FINISH", data={}, category="content", content=""),
        ]
        mock_sse_client.stream_chat_content.return_value = iter(events)

        result_events = list(message_handler.stream_events(running_session, "Test"))

        assert len(result_events) == 3
        assert result_events[0].event_type == "HEARTBEAT"
        assert result_events[1].event_type == "delta"

    def test_event_to_content_block_delta(self, message_handler):
        """Test converting delta event to content block."""
        event = SSEEvent(
            event_type="delta",
            data={},
            category="llm",
            content="Hello",
            checkpoint=1,
        )

        block = message_handler._event_to_content_block(event)

        assert block is not None
        assert block.content_type == ContentType.TEXT
        assert block.content == "Hello"
        assert block.is_final is False

    def test_event_to_content_block_heartbeat(self, message_handler):
        """Test heartbeat events are ignored."""
        event = SSEEvent(
            event_type="HEARTBEAT",
            data={},
            category="content",
            content="",
        )

        block = message_handler._event_to_content_block(event)

        assert block is None

    def test_event_to_content_block_empty_content(self, message_handler):
        """Test events with empty content are ignored."""
        event = SSEEvent(
            event_type="delta",
            data={},
            category="llm",
            content="",
        )

        block = message_handler._event_to_content_block(event)

        assert block is None

    def test_event_to_content_block_non_llm_category(self, message_handler):
        """Test non-llm category events are ignored for delta."""
        event = SSEEvent(
            event_type="delta",
            data={},
            category="status",
            content="some content",
        )

        block = message_handler._event_to_content_block(event)

        assert block is None


class TestAsyncMessageHandler:
    """Test cases for AsyncMessageHandler."""

    @pytest.fixture
    def mock_async_client(self, mock_config):
        """Create a mock async client."""
        from data_agent.client import AsyncDataAgentClient
        client = Mock(spec=AsyncDataAgentClient)
        client.config = mock_config
        return client

    @pytest.fixture
    def mock_async_sse_client(self):
        """Create a mock async SSE client."""
        from data_agent.sse_client import AsyncSSEClient
        return Mock(spec=AsyncSSEClient)

    @pytest.fixture
    def async_handler(self, mock_async_client, mock_async_sse_client):
        """Create an async message handler."""
        handler = AsyncMessageHandler(mock_async_client)
        handler._sse_client = mock_async_sse_client
        return handler

    @pytest.mark.asyncio
    async def test_send_query_async(self, async_handler, mock_async_client, mock_async_sse_client):
        """Test async query sending."""
        session = SessionInfo(
            agent_id="agent-1",
            session_id="session-1",
            status=SessionStatus.RUNNING,
        )

        mock_async_client.send_message.return_value = {}
        mock_async_sse_client.get_full_response.return_value = "Async result"

        result = await async_handler.send_query(session, "Async query")

        assert result == "Async result"
        mock_async_client.send_message.assert_called_once()
        mock_async_sse_client.get_full_response.assert_called_once()

    @pytest.mark.asyncio
    async def test_send_query_async_failure(self, async_handler, mock_async_client, mock_async_sse_client):
        """Test async query error handling."""
        session = SessionInfo(
            agent_id="agent-1",
            session_id="session-1",
            status=SessionStatus.RUNNING,
        )

        mock_async_client.send_message.return_value = {}
        mock_async_sse_client.get_full_response.side_effect = Exception("SSE failed")

        with pytest.raises(ContentFetchError):
            await async_handler.send_query(session, "Async query")
