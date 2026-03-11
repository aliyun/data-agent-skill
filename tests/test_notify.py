"""Tests for cli.notify module.

Author: Tinker
Created: 2026-03-11
"""

import json
import os
import subprocess
from http.server import HTTPServer, BaseHTTPRequestHandler
from threading import Thread
from unittest import mock

import pytest

from cli.notify import get_active_session, push_notification


# ---------------------------------------------------------------------------
# get_active_session
# ---------------------------------------------------------------------------

class TestGetActiveSession:
    """Tests for get_active_session()."""

    def test_returns_env_openclaw_session(self):
        with mock.patch.dict(os.environ, {"OPENCLAW_SESSION": "sess-abc"}):
            assert get_active_session() == "sess-abc"

    def test_returns_env_clawdbot_push_session(self):
        env = {"CLAWDBOT_PUSH_SESSION": "sess-xyz"}
        with mock.patch.dict(os.environ, env, clear=False):
            # Make sure OPENCLAW_SESSION is not set
            with mock.patch.dict(os.environ, {"OPENCLAW_SESSION": ""}, clear=False):
                assert get_active_session() == "sess-xyz"

    def test_openclaw_takes_priority(self):
        env = {"OPENCLAW_SESSION": "oc-1", "CLAWDBOT_PUSH_SESSION": "cb-2"}
        with mock.patch.dict(os.environ, env, clear=False):
            assert get_active_session() == "oc-1"

    def test_returns_empty_when_no_env_no_cli(self):
        env = {"OPENCLAW_SESSION": "", "CLAWDBOT_PUSH_SESSION": ""}
        with mock.patch.dict(os.environ, env, clear=False):
            # Mock 'which' to fail for both CLIs
            with mock.patch("subprocess.run", side_effect=subprocess.CalledProcessError(1, "which")):
                assert get_active_session() == ""

    def test_cli_fallback_parses_sessions(self):
        env = {"OPENCLAW_SESSION": "", "CLAWDBOT_PUSH_SESSION": ""}
        cli_output = json.dumps({"sessions": [{"key": "webchat:active-sess-123"}]})

        def mock_run(cmd, **kwargs):
            if cmd[0] == "which":
                if cmd[1] == "openclaw":
                    return mock.Mock(returncode=0)
                raise subprocess.CalledProcessError(1, "which")
            # The sessions list command
            return mock.Mock(returncode=0, stdout=cli_output)

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch("subprocess.run", side_effect=mock_run):
                assert get_active_session() == "active-sess-123"

    def test_cli_fallback_simple_key(self):
        env = {"OPENCLAW_SESSION": "", "CLAWDBOT_PUSH_SESSION": ""}
        cli_output = json.dumps({"sessions": [{"key": "simple-key"}]})

        def mock_run(cmd, **kwargs):
            if cmd[0] == "which":
                if cmd[1] == "openclaw":
                    raise subprocess.CalledProcessError(1, "which")
                return mock.Mock(returncode=0)  # clawdbot found
            return mock.Mock(returncode=0, stdout=cli_output)

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch("subprocess.run", side_effect=mock_run):
                assert get_active_session() == "simple-key"


# ---------------------------------------------------------------------------
# push_notification — HTTP path
# ---------------------------------------------------------------------------

class TestPushNotificationHTTP:
    """Tests for push_notification() via ASYNC_TASK_PUSH_URL."""

    def _start_server(self, handler_class):
        server = HTTPServer(("127.0.0.1", 0), handler_class)
        thread = Thread(target=server.handle_request, daemon=True)
        thread.start()
        return server, thread

    def test_http_push_success(self):
        """Verify HTTP push sends correct payload and returns True on 200."""
        received = {}

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers.get("Content-Length", 0))
                body = self.rfile.read(length)
                received["body"] = json.loads(body)
                received["content_type"] = self.headers.get("Content-Type")
                received["auth"] = self.headers.get("Authorization")
                self.send_response(200)
                self.end_headers()

            def log_message(self, *args):
                pass  # suppress logs

        server, thread = self._start_server(Handler)
        port = server.server_address[1]

        env = {
            "ASYNC_TASK_PUSH_URL": f"http://127.0.0.1:{port}/notify",
            "ASYNC_TASK_AUTH_TOKEN": "test-token-123",
        }
        with mock.patch.dict(os.environ, env, clear=False):
            result = push_notification("sess-001", "hello world")

        thread.join(timeout=5)
        server.server_close()

        assert result is True
        assert received["body"] == {
            "sessionId": "sess-001",
            "content": "hello world",
            "role": "assistant",
        }
        assert received["content_type"] == "application/json"
        assert received["auth"] == "Bearer test-token-123"

    def test_http_push_no_auth_token(self):
        """When ASYNC_TASK_AUTH_TOKEN is not set, no Authorization header."""
        received = {}

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                received["auth"] = self.headers.get("Authorization")
                self.send_response(200)
                self.end_headers()

            def log_message(self, *args):
                pass

        server, thread = self._start_server(Handler)
        port = server.server_address[1]

        env = {"ASYNC_TASK_PUSH_URL": f"http://127.0.0.1:{port}/notify"}
        with mock.patch.dict(os.environ, env, clear=False):
            # Ensure token is not set
            os.environ.pop("ASYNC_TASK_AUTH_TOKEN", None)
            result = push_notification("s1", "msg")

        thread.join(timeout=5)
        server.server_close()

        assert result is True
        assert received["auth"] is None

    def test_http_push_server_error_returns_false(self):
        """500 response → returns False."""

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                self.send_response(500)
                self.end_headers()

            def log_message(self, *args):
                pass

        server, thread = self._start_server(Handler)
        port = server.server_address[1]

        env = {"ASYNC_TASK_PUSH_URL": f"http://127.0.0.1:{port}/notify"}
        with mock.patch.dict(os.environ, env, clear=False):
            os.environ.pop("ASYNC_TASK_AUTH_TOKEN", None)
            result = push_notification("s1", "msg")

        thread.join(timeout=5)
        server.server_close()

        # urlopen raises on 500, caught as URLError → False
        assert result is False

    def test_http_push_connection_refused_returns_false(self):
        """Unreachable URL → returns False."""
        env = {"ASYNC_TASK_PUSH_URL": "http://127.0.0.1:1/unreachable"}
        with mock.patch.dict(os.environ, env, clear=False):
            result = push_notification("s1", "msg")
        assert result is False

    def test_http_push_multiline_message(self):
        """Verify multi-line waiting_input notification is sent correctly."""
        received = {}

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers.get("Content-Length", 0))
                body = self.rfile.read(length)
                received["body"] = json.loads(body)
                self.send_response(200)
                self.end_headers()

            def log_message(self, *args):
                pass

        server, thread = self._start_server(Handler)
        port = server.server_address[1]

        msg = (
            "⚠️ Data Agent session test-sess is waiting for your input (worker has exited).\n"
            "Please review the pending content: cat sessions/test-sess/output.md\n"
            "Then confirm: python3 data_agent_cli.py attach --session-id test-sess -q '确认执行'"
        )
        env = {"ASYNC_TASK_PUSH_URL": f"http://127.0.0.1:{port}/notify"}
        with mock.patch.dict(os.environ, env, clear=False):
            os.environ.pop("ASYNC_TASK_AUTH_TOKEN", None)
            result = push_notification("test-sess", msg)

        thread.join(timeout=5)
        server.server_close()

        assert result is True
        assert received["body"]["content"] == msg
        assert "\n" in received["body"]["content"]


# ---------------------------------------------------------------------------
# push_notification — CLI path
# ---------------------------------------------------------------------------

class TestPushNotificationCLI:
    """Tests for push_notification() via CLI fallback."""

    def test_cli_push_success(self):
        """CLI push returns True when command succeeds."""

        def mock_run(cmd, **kwargs):
            if cmd[0] == "which":
                return mock.Mock(returncode=0)
            # sessions send command
            assert cmd[1] == "sessions"
            assert cmd[2] == "send"
            assert "--session" in cmd
            return mock.Mock(returncode=0)

        env = {"ASYNC_TASK_PUSH_URL": ""}
        with mock.patch.dict(os.environ, env, clear=False):
            os.environ.pop("ASYNC_TASK_PUSH_URL", None)
            with mock.patch("subprocess.run", side_effect=mock_run):
                result = push_notification("sess-target", "test message")

        assert result is True

    def test_cli_push_failure(self):
        """CLI push returns False when command fails."""

        def mock_run(cmd, **kwargs):
            if cmd[0] == "which":
                return mock.Mock(returncode=0)
            return mock.Mock(returncode=1)

        env = {"ASYNC_TASK_PUSH_URL": ""}
        with mock.patch.dict(os.environ, env, clear=False):
            os.environ.pop("ASYNC_TASK_PUSH_URL", None)
            with mock.patch("subprocess.run", side_effect=mock_run):
                result = push_notification("sess-target", "msg")

        assert result is False

    def test_no_cli_no_url_returns_false(self):
        """No ASYNC_TASK_PUSH_URL and no CLI → returns False."""
        env = {"OPENCLAW_SESSION": "", "CLAWDBOT_PUSH_SESSION": ""}
        with mock.patch.dict(os.environ, env, clear=False):
            os.environ.pop("ASYNC_TASK_PUSH_URL", None)
            with mock.patch("subprocess.run", side_effect=subprocess.CalledProcessError(1, "which")):
                result = push_notification("", "msg")
        assert result is False

    def test_cli_uses_session_id_argument(self):
        """When session_id is provided, it's passed directly to CLI."""
        captured_cmd = {}

        def mock_run(cmd, **kwargs):
            if cmd[0] == "which":
                if cmd[1] == "openclaw":
                    return mock.Mock(returncode=0)
                raise subprocess.CalledProcessError(1, "which")
            captured_cmd["args"] = cmd
            return mock.Mock(returncode=0)

        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("ASYNC_TASK_PUSH_URL", None)
            with mock.patch("subprocess.run", side_effect=mock_run):
                push_notification("my-session", "hello")

        assert captured_cmd["args"] == [
            "openclaw", "sessions", "send", "--session", "my-session", "hello"
        ]
