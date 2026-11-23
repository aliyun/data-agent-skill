#!/usr/bin/env python3
"""Offline unit verifier for the header-auth layering (POP / custom-header).

Purpose:
    Verify the custom-header auth profile and the shared header transport
    without any network access: config resolution and precedence, wire shape
    (camelCase body, injected headers, route URL), per-action endpoint
    overrides (routing + capability gating), single-attempt semantics, error
    mapping, secret redaction, and POP-profile wire invariance (API_KEY
    behavior must not drift).

Usage:
    cd alibabacloud-data-agent-skill
    python3 scripts/verify_header_auth_unit.py

Exit codes:
    0 - all checks passed.
    1 - at least one check failed.
"""

from __future__ import annotations

import argparse
import io
import json
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import requests  # noqa: E402

from data_agent.config import DataAgentConfig  # noqa: E402
from data_agent.client import DataAgentClient  # noqa: E402
from data_agent.exceptions import (  # noqa: E402
    ApiError,
    AuthenticationError,
    ConfigurationError,
    SessionCreationError,
)
from data_agent.models import SessionInfo, SessionStatus  # noqa: E402
from cli import cmd_send  # noqa: E402


SEND_URL = "http://send.test/chat/send_message"
DESCRIBE_URL = "http://query.test/session/describe"
CREATE_URL = "http://query.test/session/create"
HEADER_NAME = "X-Custom-Auth"
HEADER_VALUE = "secret-1"

SUCCESS_ENVELOPE = {
    "success": True,
    "error_code": "Success",
    "error_message": "",
    "status_code": 200,
    "request_id": "REQ-1",
    "data": {
        "session_id": "s-1",
        "agent_id": "a-1",
        "message_id": "m-1",
        "status": "SENT",
    },
}


class HermeticEnv(unittest.TestCase):
    """Isolate from the machine's real credentials file / DATA_AGENT_* env."""

    def setUp(self):
        super().setUp()
        home = tempfile.TemporaryDirectory()
        self.addCleanup(home.cleanup)
        for target, kwargs in (
            ("data_agent.config.Path.home", {"return_value": Path(home.name)}),
            ("data_agent.config.load_dotenv", {}),
        ):
            patcher = mock.patch(target, **kwargs)
            patcher.start()
            self.addCleanup(patcher.stop)
        clean_env = {k: v for k, v in os.environ.items() if not k.startswith("DATA_AGENT_")}
        env_patch = mock.patch.dict(os.environ, clean_env, clear=True)
        env_patch.start()
        self.addCleanup(env_patch.stop)


def custom_config(**overrides) -> DataAgentConfig:
    base = dict(
        custom_headers={HEADER_NAME: HEADER_VALUE},
        action_endpoints={"SendChatMessage": SEND_URL},
    )
    base.update(overrides)
    return DataAgentConfig.from_dict(base)


def ok_response(envelope: dict) -> mock.Mock:
    resp = mock.Mock()
    resp.raise_for_status = mock.Mock()
    resp.json = mock.Mock(return_value=envelope)
    return resp


def http_error_response(status: int) -> mock.Mock:
    resp = mock.Mock()
    resp.status_code = status
    resp.text = "denied"
    resp.raise_for_status = mock.Mock(
        side_effect=requests.HTTPError(response=resp)
    )
    return resp


class ConfigResolution(HermeticEnv):
    def test_custom_headers_mode_and_normalization(self):
        cfg = custom_config()
        self.assertEqual(cfg.action_endpoints, {"SendChatMessage": SEND_URL})
        self.assertEqual(cfg.to_dict()["auth_type"], "custom_headers")
        self.assertNotIn("custom_headers", cfg.to_dict())
        self.assertNotIn(HEADER_VALUE, json.dumps(cfg.to_dict()))

    def test_custom_headers_require_action_endpoints(self):
        with self.assertRaises(ConfigurationError) as ctx:
            DataAgentConfig.from_dict(dict(custom_headers={HEADER_NAME: HEADER_VALUE}))
        self.assertIn("action_endpoints", str(ctx.exception))

    def test_custom_headers_blank_action_endpoints_fails_fast(self):
        with self.assertRaises(ConfigurationError):
            DataAgentConfig.from_dict(dict(
                custom_headers={HEADER_NAME: HEADER_VALUE},
                action_endpoints={"SendChatMessage": "   "},
            ))

    def test_blank_header_entries_drop_to_default_chain(self):
        cfg = DataAgentConfig.from_dict(dict(custom_headers={"X-A": "   "}))
        self.assertIsNone(cfg.custom_headers)
        self.assertEqual(cfg.to_dict()["auth_type"], "default_credential_chain")

    def test_non_mapping_headers_rejected(self):
        with self.assertRaises(ConfigurationError) as ctx:
            DataAgentConfig.from_dict(dict(custom_headers="not-a-mapping"))
        self.assertIn("mapping", str(ctx.exception))

    def test_precedence_custom_headers_over_api_key(self):
        cfg = custom_config(api_key="key-1")
        self.assertEqual(cfg.to_dict()["auth_type"], "custom_headers")

    def test_api_key_mode_unchanged(self):
        cfg = DataAgentConfig.from_dict(dict(api_key="key-1"))
        self.assertEqual(cfg.to_dict()["auth_type"], "api_key")
        self.assertEqual(cfg.endpoint, "dataagent-cn-hangzhou.aliyuncs.com/apikey")

    def test_default_chain_unchanged(self):
        cfg = DataAgentConfig.from_dict({})
        self.assertEqual(cfg.to_dict()["auth_type"], "default_credential_chain")
        self.assertEqual(cfg.endpoint, "dms.cn-hangzhou.aliyuncs.com")

    def test_from_env_reads_new_vars(self):
        env = {
            "DATA_AGENT_CUSTOM_HEADERS": json.dumps({HEADER_NAME: HEADER_VALUE}),
            "DATA_AGENT_ACTION_ENDPOINTS": json.dumps({"SendChatMessage": SEND_URL}),
        }
        with mock.patch.dict(os.environ, env, clear=False):
            cfg = DataAgentConfig.from_env()
        self.assertEqual(cfg.custom_headers, {HEADER_NAME: HEADER_VALUE})
        self.assertEqual(cfg.action_endpoints, {"SendChatMessage": SEND_URL})
        self.assertEqual(cfg.to_dict()["auth_type"], "custom_headers")

    def test_from_env_invalid_json_fails_fast(self):
        env = {"DATA_AGENT_CUSTOM_HEADERS": "{not json"}
        with mock.patch.dict(os.environ, env, clear=False):
            with self.assertRaises(ConfigurationError) as ctx:
                DataAgentConfig.from_env()
        self.assertIn("DATA_AGENT_CUSTOM_HEADERS", str(ctx.exception))

    def test_credentials_file_custom_headers_entry(self):
        data = {"env1": {"prof1": {
            "custom_headers": {HEADER_NAME: "tok-file"},
            "action_endpoints": {"SendChatMessage": "send.test/send"},
        }}}
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump(data, f)
            path = f.name
        try:
            cfg = DataAgentConfig.from_dict(
                dict(
                    credentials_file=path,
                    credentials_env="env1",
                    credentials_profile="prof1",
                )
            )
            self.assertEqual(cfg.custom_headers, {HEADER_NAME: "tok-file"})
            self.assertEqual(cfg.action_endpoints, {"SendChatMessage": "http://send.test/send"})
            self.assertEqual(cfg.to_dict()["auth_type"], "custom_headers")
        finally:
            os.unlink(path)

    def test_credentials_file_custom_headers_normalized(self):
        data = {"env1": {"prof1": {
            "custom_headers": {f" {HEADER_NAME} ": " tok-file ", "X-Blank": "  ", "X-Num": 7},
            "action_endpoints": {"SendChatMessage": "send.test/send"},
        }}}
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump(data, f)
            path = f.name
        try:
            cfg = DataAgentConfig.from_dict(
                dict(credentials_file=path, credentials_env="env1", credentials_profile="prof1")
            )
            self.assertEqual(cfg.custom_headers, {HEADER_NAME: "tok-file", "X-Num": "7"})
        finally:
            os.unlink(path)

    def test_credentials_file_all_blank_headers_rejected(self):
        data = {"env1": {"prof1": {
            "custom_headers": {"X-Blank": "   "},
            "action_endpoints": {"SendChatMessage": "send.test/send"},
        }}}
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump(data, f)
            path = f.name
        try:
            with self.assertRaises(ConfigurationError) as ctx:
                DataAgentConfig.from_dict(
                    dict(credentials_file=path, credentials_env="env1", credentials_profile="prof1")
                )
            self.assertIn("access_key_id", str(ctx.exception))
        finally:
            os.unlink(path)

    def test_credentials_file_empty_entry_rejected(self):
        data = {"env1": {"prof1": {"note": "nothing"}}}
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump(data, f)
            path = f.name
        try:
            with self.assertRaises(ConfigurationError) as ctx:
                DataAgentConfig.from_dict(
                    dict(credentials_file=path, credentials_env="env1", credentials_profile="prof1")
                )
            self.assertIn("custom_headers", str(ctx.exception))
        finally:
            os.unlink(path)

    def test_action_endpoints_normalization(self):
        cfg = custom_config(action_endpoints={
            "DescribeDataAgentSession": "query.test/session/describe/",
            "  ": "http://drop.test",
            "Blank": "   ",
        })
        self.assertEqual(cfg.action_endpoints, {"DescribeDataAgentSession": DESCRIBE_URL})
        self.assertEqual(cfg.to_dict()["action_endpoints"], cfg.action_endpoints)

    def test_action_endpoints_non_mapping_rejected(self):
        with self.assertRaises(ConfigurationError) as ctx:
            custom_config(action_endpoints="not-a-mapping")
        self.assertIn("mapping", str(ctx.exception))

    def test_action_endpoints_blank_entries_drop_to_none(self):
        cfg = DataAgentConfig.from_dict(
            dict(api_key="key-1", action_endpoints={"DescribeDataAgentSession": "  "})
        )
        self.assertIsNone(cfg.action_endpoints)
        self.assertNotIn("action_endpoints", cfg.to_dict())

    def test_action_endpoints_do_not_change_auth_mode(self):
        cfg = DataAgentConfig.from_dict(
            dict(api_key="key-1", action_endpoints={"SendChatMessage": "https://other.test/apikey"})
        )
        self.assertEqual(cfg.to_dict()["auth_type"], "api_key")

    def test_from_env_action_endpoints_json(self):
        env = {
            "DATA_AGENT_CUSTOM_HEADERS": json.dumps({HEADER_NAME: HEADER_VALUE}),
            "DATA_AGENT_ACTION_ENDPOINTS": json.dumps({
                "SendChatMessage": SEND_URL,
                "DescribeDataAgentSession": DESCRIBE_URL,
            }),
        }
        with mock.patch.dict(os.environ, env, clear=False):
            cfg = DataAgentConfig.from_env()
        self.assertEqual(cfg.action_endpoints["DescribeDataAgentSession"], DESCRIBE_URL)

    def test_from_env_action_endpoints_invalid_json_fails_fast(self):
        env = {"DATA_AGENT_ACTION_ENDPOINTS": "{not json"}
        with mock.patch.dict(os.environ, env, clear=False):
            with self.assertRaises(ConfigurationError) as ctx:
                DataAgentConfig.from_env()
        self.assertIn("DATA_AGENT_ACTION_ENDPOINTS", str(ctx.exception))

    def test_credentials_file_action_endpoints_entry(self):
        data = {"env1": {"prof1": {
            "custom_headers": {HEADER_NAME: "tok-file"},
            "action_endpoints": {"DescribeDataAgentSession": "query.test/session/describe"},
        }}}
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump(data, f)
            path = f.name
        try:
            cfg = DataAgentConfig.from_dict(
                dict(
                    credentials_file=path,
                    credentials_env="env1",
                    credentials_profile="prof1",
                )
            )
            self.assertEqual(cfg.action_endpoints, {"DescribeDataAgentSession": DESCRIBE_URL})
        finally:
            os.unlink(path)


class CustomHeaderProfile(HermeticEnv):
    def setUp(self):
        super().setUp()
        self.client = DataAgentClient(custom_config())

    def test_init_skips_sdk_and_credential_chain(self):
        self.assertEqual(self.client._auth_type, "custom_headers")
        self.assertIsNone(self.client._sdk_client)

    def test_wire_shape(self):
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(SUCCESS_ENVELOPE)) as post:
            resp = self.client.send_message(
                message="hello", session_id="s-1", workspace_id="w-1", mode="pro"
            )
        self.assertEqual(post.call_count, 1)
        args, kwargs = post.call_args
        self.assertEqual(args[0], SEND_URL)
        headers = kwargs["headers"]
        self.assertEqual(headers[HEADER_NAME], HEADER_VALUE)
        self.assertTrue(headers["X-Request-Id"])
        body = kwargs["json"]
        self.assertEqual(body["sessionId"], "s-1")
        self.assertEqual(body["message"], "hello")
        self.assertEqual(body["messageType"], "primary")
        self.assertEqual(body["workspaceId"], "w-1")
        self.assertIsInstance(body["sessionConfig"], dict)
        self.assertEqual(body["sessionConfig"]["Mode"], "pro")
        for banned in ("Action", "Version", "RegionId", "DMSUnit", "AgentId",
                       "SessionId", "Message", "MessageType"):
            self.assertNotIn(banned, body)
        self.assertEqual(resp["data"]["sessionId"], "s-1")
        self.assertEqual(resp["data"]["status"], "SENT")
        self.assertEqual(resp["requestId"], "REQ-1")

    def test_dms_unit_never_on_wire(self):
        with mock.patch.object(
            DataAgentClient, "_resolve_dms_unit", return_value="cn-hangzhou"
        ), mock.patch("data_agent.client.requests.post", return_value=ok_response(SUCCESS_ENVELOPE)) as post:
            self.client.send_message(message="hello", session_id="s-1")
        body = post.call_args.kwargs["json"]
        self.assertNotIn("dmsUnit", body)
        self.assertNotIn("DMSUnit", body)

    def test_pending_is_success(self):
        envelope = json.loads(json.dumps(SUCCESS_ENVELOPE))
        envelope["data"]["status"] = "PENDING"
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(envelope)):
            resp = self.client.send_message(message="hello", session_id="s-1")
        self.assertEqual(resp["data"]["status"], "PENDING")

    def test_unconfigured_action_rejected(self):
        with self.assertRaises(ApiError) as ctx:
            self.client._call_api("GetChatContent", "2025-04-14", {"SessionId": "s-1"})
        self.assertEqual(ctx.exception.code, "UnsupportedAuthMode")
        self.assertIn("no endpoint configured", str(ctx.exception))

    def test_business_error_envelope_raises_with_request_id(self):
        envelope = {
            "success": True,
            "error_code": "DMS.DataAgent.SessionUnavailable",
            "error_message": "session not found",
            "status_code": 200,
            "request_id": "REQ-ERR",
        }
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(envelope)):
            with self.assertRaises(ApiError) as ctx:
                self.client.send_message(message="hello", session_id="s-1")
        self.assertEqual(ctx.exception.code, "DMS.DataAgent.SessionUnavailable")
        self.assertEqual(ctx.exception.request_id, "REQ-ERR")

    def test_framework_error_envelope_raises(self):
        envelope = {
            "success": False,
            "error_code": "INTERNAL_DATA_AGENT_ERROR",
            "error_message": "boom",
            "status_code": 500,
            "request_id": "REQ-500",
        }
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(envelope)):
            with self.assertRaises(ApiError) as ctx:
                self.client.send_message(message="hello", session_id="s-1")
        self.assertEqual(ctx.exception.request_id, "REQ-500")

    def test_http_401_maps_to_authentication_error(self):
        with mock.patch("data_agent.client.requests.post", return_value=http_error_response(401)):
            with self.assertRaises(AuthenticationError):
                self.client.send_message(message="hello", session_id="s-1")

    def test_http_403_maps_to_authentication_error(self):
        with mock.patch("data_agent.client.requests.post", return_value=http_error_response(403)):
            with self.assertRaises(AuthenticationError):
                self.client.send_message(message="hello", session_id="s-1")

    def test_http_500_maps_to_api_error(self):
        with mock.patch("data_agent.client.requests.post", return_value=http_error_response(500)):
            with self.assertRaises(ApiError) as ctx:
                self.client.send_message(message="hello", session_id="s-1")
        self.assertEqual(ctx.exception.code, "HTTPRequestError")

    def test_network_failure_single_attempt(self):
        with mock.patch(
            "data_agent.client.requests.post", side_effect=requests.ConnectionError("refused")
        ) as post:
            with self.assertRaises(ApiError) as ctx:
                self.client.send_message(message="hello", session_id="s-1")
        self.assertEqual(post.call_count, 1)
        self.assertEqual(ctx.exception.code, "HTTPRequestError")

    def test_debug_log_redacts_injected_header_values(self):
        buf = io.StringIO()
        with mock.patch.dict(os.environ, {"DATA_AGENT_DEBUG_API": "1"}), \
                mock.patch("data_agent.client.requests.post", return_value=ok_response(SUCCESS_ENVELOPE)), \
                redirect_stdout(buf):
            self.client.send_message(message="hello", session_id="s-1")
        output = buf.getvalue()
        self.assertIn("<redacted>", output)
        self.assertNotIn(HEADER_VALUE, output)

    def test_describe_via_override(self):
        client = DataAgentClient(custom_config(
            action_endpoints={"DescribeDataAgentSession": DESCRIBE_URL}
        ))
        envelope = {
            "success": True,
            "data": {"sessionId": "s-1", "agentId": "a-1", "sessionStatus": "IDLE"},
        }
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(envelope)) as post:
            session = client.describe_session(session_id="s-1")
        args, kwargs = post.call_args
        self.assertEqual(args[0], DESCRIBE_URL)
        self.assertEqual(kwargs["json"]["sessionId"], "s-1")
        self.assertNotIn("DMSUnit", kwargs["json"])
        self.assertNotIn("AgentId", kwargs["json"])
        self.assertEqual(session.agent_id, "a-1")
        self.assertEqual(session.status, SessionStatus.IDLE)

    def test_describe_without_override_still_guarded(self):
        with self.assertRaises(ApiError) as ctx:
            self.client.describe_session(session_id="s-1")
        self.assertEqual(ctx.exception.code, "UnsupportedAuthMode")

    def test_create_without_override_guarded(self):
        with self.assertRaises(SessionCreationError) as ctx:
            self.client.create_session(workspace_id="w-1")
        self.assertIn("no endpoint configured", str(ctx.exception))

    def test_create_via_override(self):
        client = DataAgentClient(custom_config(
            action_endpoints={"CreateDataAgentSession": CREATE_URL}
        ))
        envelope = {
            "success": True,
            "data": {"sessionId": "s-new", "agentId": "a-new", "agentStatus": "RUNNING"},
        }
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(envelope)) as post:
            session = client.create_session(workspace_id="w-1")
        args, kwargs = post.call_args
        self.assertEqual(args[0], CREATE_URL)
        self.assertEqual(kwargs["json"]["workspaceId"], "w-1")
        self.assertIsInstance(kwargs["json"]["sessionConfig"], dict)
        self.assertNotIn("DMSUnit", kwargs["json"])
        self.assertEqual(session.session_id, "s-new")
        self.assertEqual(session.status, SessionStatus.RUNNING)

    def test_send_override_replaces_configured_url(self):
        client = DataAgentClient(custom_config(
            action_endpoints={"SendChatMessage": "http://other.test/send"}
        ))
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(SUCCESS_ENVELOPE)) as post:
            client.send_message(message="hello", session_id="s-1")
        self.assertEqual(post.call_args[0][0], "http://other.test/send")

    def test_camelcase_error_envelope_raises(self):
        client = DataAgentClient(custom_config(
            action_endpoints={"DescribeDataAgentSession": DESCRIBE_URL}
        ))
        envelope = {
            "success": True,
            "errorCode": "DMS.DataAgent.SessionUnavailable",
            "errorMessage": "session not found",
            "requestId": "REQ-C",
        }
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(envelope)):
            with self.assertRaises(ApiError) as ctx:
                client.describe_session(session_id="s-1")
        self.assertEqual(ctx.exception.code, "DMS.DataAgent.SessionUnavailable")
        self.assertEqual(ctx.exception.request_id, "REQ-C")

    def test_override_http_401_maps_to_authentication_error(self):
        client = DataAgentClient(custom_config(
            action_endpoints={"DescribeDataAgentSession": DESCRIBE_URL}
        ))
        with mock.patch("data_agent.client.requests.post", return_value=http_error_response(401)):
            with self.assertRaises(AuthenticationError):
                client.describe_session(session_id="s-1")


class PopProfileInvariance(HermeticEnv):
    """API_KEY wire behavior must not drift through the transport refactor."""

    def setUp(self):
        super().setUp()
        self.client = DataAgentClient(DataAgentConfig.from_dict(dict(api_key="key-1")))

    def test_send_wire_shape(self):
        envelope = {"success": True, "data": {"SessionId": "s-1", "AgentId": "a-1"}}
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(envelope)) as post:
            self.client.send_message(message="hello", session_id="s-1", mode="pro")
        args, kwargs = post.call_args
        self.assertEqual(args[0], "https://dataagent-stream-cn-hangzhou.aliyuncs.com/apikey")
        self.assertEqual(kwargs["headers"]["x-api-key"], "key-1")
        body = kwargs["json"]
        self.assertEqual(body["Action"], "SendChatMessage")
        self.assertEqual(body["Version"], "2025-04-14")
        self.assertEqual(body["RegionId"], "cn-hangzhou")
        self.assertEqual(body["SessionId"], "s-1")
        self.assertIsInstance(body["SessionConfig"], dict)
        self.assertEqual(body["SessionConfig"]["Mode"], "pro")

    def test_control_plane_endpoint_override(self):
        cfg = DataAgentConfig.from_dict(
            dict(api_key="key-1", endpoint="pre-dataagent.aliyuncs.com/apikey")
        )
        client = DataAgentClient(cfg)
        envelope = {"success": True, "data": []}
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(envelope)) as post:
            client.list_sessions(workspace_id="w-1")
        self.assertEqual(post.call_args[0][0], "https://pre-dataagent.aliyuncs.com/apikey")

    def test_http_error_shape_unchanged(self):
        with mock.patch("data_agent.client.requests.post", return_value=http_error_response(503)):
            with self.assertRaises(ApiError) as ctx:
                self.client.send_message(message="hello", session_id="s-1")
        self.assertEqual(ctx.exception.code, "HTTPRequestError")
        self.assertIn("Status: 503", str(ctx.exception))

    def test_stream_endpoint_routing_without_overrides(self):
        cfg = DataAgentConfig.from_dict(
            dict(api_key="key-1", stream_endpoint="pre-stream.aliyuncs.com/apikey")
        )
        client = DataAgentClient(cfg)
        envelope = {"success": True, "data": {"SessionId": "s-1", "AgentId": "a-1"}}
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(envelope)) as post:
            client.send_message(message="hello", session_id="s-1")
        self.assertEqual(post.call_args[0][0], "https://pre-stream.aliyuncs.com/apikey")

    def test_action_override_wins_over_stream_endpoint(self):
        cfg = DataAgentConfig.from_dict(dict(
            api_key="key-1",
            stream_endpoint="pre-stream.aliyuncs.com/apikey",
            action_endpoints={"SendChatMessage": "https://other.test/apikey"},
        ))
        client = DataAgentClient(cfg)
        envelope = {"success": True, "data": {"SessionId": "s-1", "AgentId": "a-1"}}
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(envelope)) as post:
            client.send_message(message="hello", session_id="s-1")
        args, kwargs = post.call_args
        self.assertEqual(args[0], "https://other.test/apikey")
        # Wire shape stays POP: override changes routing only.
        self.assertEqual(kwargs["json"]["Action"], "SendChatMessage")
        self.assertEqual(kwargs["json"]["RegionId"], "cn-hangzhou")
        self.assertEqual(kwargs["headers"]["x-api-key"], "key-1")

    def test_action_override_control_plane(self):
        cfg = DataAgentConfig.from_dict(dict(
            api_key="key-1",
            action_endpoints={"ListDataAgentSession": "https://other.test/apikey"},
        ))
        client = DataAgentClient(cfg)
        envelope = {"success": True, "data": []}
        with mock.patch("data_agent.client.requests.post", return_value=ok_response(envelope)) as post:
            client.list_sessions(workspace_id="w-1")
        self.assertEqual(post.call_args[0][0], "https://other.test/apikey")


class CmdSendCustomBranch(HermeticEnv):
    """cmd_send capability gating in custom-header mode (mocked client)."""

    def setUp(self):
        super().setUp()
        env = {
            "DATA_AGENT_CUSTOM_HEADERS": json.dumps({HEADER_NAME: HEADER_VALUE}),
            "DATA_AGENT_ACTION_ENDPOINTS": json.dumps({"SendChatMessage": SEND_URL}),
        }
        env_patch = mock.patch.dict(os.environ, env, clear=False)
        env_patch.start()
        self.addCleanup(env_patch.stop)
        self.client = mock.Mock()
        self.client.describe_session.return_value = SessionInfo(
            agent_id="a-1", session_id="s-1", status=SessionStatus.IDLE
        )
        self.client.send_message.return_value = {
            "success": "true",
            "requestId": "R-1",
            "data": {"messageId": "m-1", "status": "SENT"},
        }
        for target, kwargs in (
            ("cli.cmd_send.DataAgentClient", {"return_value": self.client}),
            ("cli.cmd_send.build_send_options", {"return_value": {}}),
            ("cli.cmd_send.time.sleep", {}),
        ):
            patcher = mock.patch(target, **kwargs)
            patcher.start()
            self.addCleanup(patcher.stop)

    def _args(self, **kw):
        base = dict(
            message="hi", session_id="s-1", agent_id="", poll=False,
            timeout=30, workspace_id="", session_mode=None, custom_agent_id=None,
        )
        base.update(kw)
        return argparse.Namespace(**base)

    def _run(self, args):
        with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()) as err:
            with self.assertRaises(SystemExit) as ctx:
                cmd_send.cmd_send(args)
        return ctx.exception.code, err.getvalue()

    def test_reuse_without_describe_endpoint_errors(self):
        # No client-side capability check: the reuse path calls describe and
        # the channel layer reports the missing endpoint configuration.
        self.client.describe_session.side_effect = ApiError(
            "DescribeDataAgentSession has no endpoint configured in "
            "custom-header auth mode; add it to action_endpoints.",
            code="UnsupportedAuthMode", request_id=None,
        )
        code, err = self._run(self._args())
        self.assertEqual(code, 1)
        self.assertIn("Failed to describe session", err)
        self.client.send_message.assert_not_called()

    def test_reuse_with_describe_resolves_agent(self):
        code, _ = self._run(self._args())
        self.assertEqual(code, 0)
        self.client.describe_session.assert_called()
        self.assertEqual(self.client.send_message.call_args.kwargs["agent_id"], "a-1")

    def test_poll_with_describe_completes(self):
        code, _ = self._run(self._args(poll=True))
        self.assertEqual(code, 0)
        self.client.send_message.assert_called_once()
        # Agent resolution plus the two consecutive IDLE confirmations.
        self.assertGreaterEqual(self.client.describe_session.call_count, 3)

    def test_first_message_without_session_id_sends(self):
        # Creation capability is the backend's decision, not an auth-mode
        # rule: the first message goes to the configured send endpoint with
        # no SessionId, and a returned SessionId is adopted.
        self.client.send_message.return_value = {
            "success": "true",
            "requestId": "R-1",
            "sessionId": "s-new",
            "agentId": "a-new",
            "data": {"messageId": "m-1", "status": "SENT"},
        }
        code, _ = self._run(self._args(session_id=""))
        self.assertEqual(code, 0)
        self.assertEqual(self.client.send_message.call_args.kwargs["session_id"], "")


if __name__ == "__main__":
    unittest.main(verbosity=2)