"""Tests for Data Agent session module."""

import pytest
from unittest.mock import Mock, patch
from datetime import datetime, timedelta

from data_agent.session import SessionManager, AsyncSessionManager
from data_agent.client import DataAgentClient
from data_agent.models import SessionInfo, SessionStatus
from data_agent.exceptions import SessionTimeoutError, SessionNotFoundError


class TestSessionManager:
    """Test cases for SessionManager."""

    @pytest.fixture
    def mock_client(self, mock_config):
        """Create a mock client."""
        client = Mock(spec=DataAgentClient)
        client.config = mock_config
        return client

    @pytest.fixture
    def session_manager(self, mock_client):
        """Create a session manager with mock client."""
        return SessionManager(mock_client)

    def test_create_new_session(self, session_manager, mock_client, mock_session_info):
        """Test creating a new session."""
        mock_client.create_session.return_value = mock_session_info
        mock_client.describe_session.return_value = mock_session_info

        session = session_manager.create_or_reuse(wait_for_running=False)

        assert session.agent_id == mock_session_info.agent_id
        assert session.session_id == mock_session_info.session_id
        mock_client.create_session.assert_called_once()

    def test_reuse_existing_session(self, session_manager, mock_client, mock_session_info):
        """Test reusing an existing session."""
        # First create a session
        mock_client.create_session.return_value = mock_session_info
        mock_client.describe_session.return_value = mock_session_info

        session1 = session_manager.create_or_reuse(wait_for_running=False)

        # Then reuse it
        session2 = session_manager.create_or_reuse(
            session_id=session1.session_id,
            wait_for_running=False,
        )

        assert session2.session_id == session1.session_id

    def test_wait_until_running_success(self, session_manager, mock_client):
        """Test waiting for session to reach RUNNING state."""
        creating_session = SessionInfo(
            agent_id="agent-1",
            session_id="session-1",
            status=SessionStatus.CREATING,
        )
        running_session = SessionInfo(
            agent_id="agent-1",
            session_id="session-1",
            status=SessionStatus.RUNNING,
        )

        # Return CREATING first, then RUNNING
        mock_client.describe_session.side_effect = [
            creating_session,
            creating_session,
            running_session,
        ]

        with patch("time.sleep"):  # Skip actual sleep
            session = session_manager.wait_until_running("session-1", "agent-1")

        assert session.status == SessionStatus.RUNNING
        assert mock_client.describe_session.call_count == 3

    def test_wait_until_running_timeout(self, session_manager, mock_client):
        """Test timeout when session doesn't reach RUNNING state."""
        creating_session = SessionInfo(
            agent_id="agent-1",
            session_id="session-1",
            status=SessionStatus.CREATING,
        )

        mock_client.describe_session.return_value = creating_session

        with patch("time.sleep"):
            with patch("time.time") as mock_time:
                # Simulate timeout
                mock_time.side_effect = [0, 0, 150, 150]  # Start, check, elapsed > max_wait

                with pytest.raises(SessionTimeoutError) as exc_info:
                    session_manager.wait_until_running("session-1", "agent-1", max_wait=120)

        assert "session-1" in str(exc_info.value)

    def test_wait_until_running_failed_state(self, session_manager, mock_client):
        """Test error when session fails."""
        failed_session = SessionInfo(
            agent_id="agent-1",
            session_id="session-1",
            status=SessionStatus.FAILED,
        )

        mock_client.describe_session.return_value = failed_session

        with patch("time.sleep"):
            with patch("time.time", return_value=0):
                with pytest.raises(SessionTimeoutError) as exc_info:
                    session_manager.wait_until_running("session-1", "agent-1")

        assert "failed" in str(exc_info.value).lower()

    def test_is_session_active_true(self, session_manager, mock_client, mock_session_info):
        """Test checking if session is active."""
        # Add session to cache
        session_manager._active_sessions[mock_session_info.session_id] = mock_session_info
        mock_client.describe_session.return_value = mock_session_info

        result = session_manager.is_session_active(mock_session_info.session_id)

        assert result is True

    def test_is_session_active_stale(self, session_manager, mock_client):
        """Test that stale sessions are not considered active."""
        stale_session = SessionInfo(
            agent_id="agent-1",
            session_id="session-1",
            status=SessionStatus.RUNNING,
            last_used_at=datetime.now() - timedelta(hours=1),
        )

        session_manager._active_sessions["session-1"] = stale_session

        result = session_manager.is_session_active("session-1")

        assert result is False

    def test_get_session(self, session_manager, mock_session_info):
        """Test getting session from cache."""
        session_manager._active_sessions[mock_session_info.session_id] = mock_session_info

        result = session_manager.get_session(mock_session_info.session_id)

        assert result == mock_session_info

    def test_get_session_not_found(self, session_manager):
        """Test getting non-existent session."""
        result = session_manager.get_session("non-existent")

        assert result is None

    def test_refresh_session(self, session_manager, mock_client, mock_session_info):
        """Test refreshing session status."""
        session_manager._active_sessions[mock_session_info.session_id] = mock_session_info

        updated_session = SessionInfo(
            agent_id=mock_session_info.agent_id,
            session_id=mock_session_info.session_id,
            status=SessionStatus.STOPPED,
        )
        mock_client.describe_session.return_value = updated_session

        result = session_manager.refresh_session(mock_session_info.session_id)

        assert result.status == SessionStatus.STOPPED

    def test_refresh_session_not_found(self, session_manager):
        """Test refreshing non-existent session."""
        with pytest.raises(SessionNotFoundError):
            session_manager.refresh_session("non-existent")

    def test_remove_session(self, session_manager, mock_session_info):
        """Test removing session from cache."""
        session_manager._active_sessions[mock_session_info.session_id] = mock_session_info

        session_manager.remove_session(mock_session_info.session_id)

        assert mock_session_info.session_id not in session_manager._active_sessions

    def test_list_sessions(self, session_manager, mock_session_info):
        """Test listing all cached sessions."""
        session_manager._active_sessions[mock_session_info.session_id] = mock_session_info

        sessions = session_manager.list_sessions()

        assert len(sessions) == 1
        assert sessions[0] == mock_session_info

    def test_clear_stale_sessions(self, session_manager):
        """Test clearing stale sessions."""
        fresh_session = SessionInfo(
            agent_id="agent-1",
            session_id="session-1",
            status=SessionStatus.RUNNING,
            last_used_at=datetime.now(),
        )
        stale_session = SessionInfo(
            agent_id="agent-2",
            session_id="session-2",
            status=SessionStatus.RUNNING,
            last_used_at=datetime.now() - timedelta(hours=1),
        )

        session_manager._active_sessions["session-1"] = fresh_session
        session_manager._active_sessions["session-2"] = stale_session

        removed = session_manager.clear_stale_sessions(max_age_minutes=30)

        assert removed == 1
        assert "session-1" in session_manager._active_sessions
        assert "session-2" not in session_manager._active_sessions
