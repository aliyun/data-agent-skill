"""Data Agent client for API interactions.

Author: Tinker
Created: 2026-03-01
"""

from __future__ import annotations

import asyncio
import time
import functools
import json
import os
import inspect
import re
import uuid
from typing import Optional, Any, Callable, TypeVar
import requests

from alibabacloud_tea_openapi import models as open_api_models
from alibabacloud_tea_openapi.client import Client as OpenApiClient
from alibabacloud_tea_util import models as util_models
from alibabacloud_openapi_util.client import Client as OpenApiUtilClient
from Tea.exceptions import TeaException

from data_agent.config import DataAgentConfig
from data_agent.models import SessionInfo, SessionStatus, DataSource, normalize_session_status
from data_agent.api_adapter import APIAdapter
from data_agent.exceptions import (
    ApiError,
    AuthenticationError,
    SessionCreationError,
    ConfigurationError,
)


T = TypeVar("T")


_SENSITIVE_FIELD_MARKERS = (
    "api-key",
    "apikey",
    "authorization",
    "accesskey",
    "access-key",
    "access_key",
    "credential",
    "password",
    "secret",
    "token",
)


def _is_debug_api_enabled() -> bool:
    """Return whether verbose API debug logging is enabled."""
    return os.getenv("DATA_AGENT_DEBUG_API", "").lower() in ("true", "1", "yes")


def _is_sensitive_field(key: Any) -> bool:
    normalized = str(key).replace("_", "-").lower()
    return any(marker in normalized for marker in _SENSITIVE_FIELD_MARKERS)


# Matches credential-bearing query parameters embedded in URL/string values.
# Signed OSS/STS URLs carry the caller's AccessKeyId, temporary security token
# and request signature inside the value of a key like ``url``, so key-name
# redaction alone would leak them into debug logs.
_CREDENTIAL_IN_VALUE = re.compile(
    r"(?i)(AccessKeyId|OSSAccessKeyId|AccessKeySecret|SecurityToken|security-token|Signature)=[^&\"'\s]*"
)


def _redact_sensitive_values(value: Any) -> Any:
    """Return a copy of ``value`` with credentials redacted for debug logs."""
    if isinstance(value, dict):
        return {
            key: "<redacted>" if _is_sensitive_field(key) else _redact_sensitive_values(item)
            for key, item in value.items()
        }
    if isinstance(value, list):
        return [_redact_sensitive_values(item) for item in value]
    if isinstance(value, tuple):
        return tuple(_redact_sensitive_values(item) for item in value)
    if isinstance(value, str):
        return _CREDENTIAL_IN_VALUE.sub(
            lambda m: m.group(0)[: m.group(0).index("=") + 1] + "<redacted>", value
        )
    return value


def _new_client_token(operation: str) -> str:
    """Build an Alibaba Cloud ClientToken that is stable across retries."""
    operation_name = operation.replace("_", "-")[:22]
    return f"da-skill-{operation_name}-{uuid.uuid4().hex}"[:64]


def retry_on_error(max_retries: int = 3, retry_codes: tuple = ("Throttling", "ServiceUnavailable")):
    """Decorator to retry API calls on transient errors with exponential backoff."""

    def decorator(func: Callable[..., T]) -> Callable[..., T]:
        signature = inspect.signature(func)
        param_names = tuple(signature.parameters)
        if "client_token" in param_names:
            # ``args`` passed to wrapper excludes ``self``.
            client_token_pos = param_names.index("client_token") - 1
        else:
            client_token_pos = None

        @functools.wraps(func)
        def wrapper(self, *args, **kwargs) -> T:
            last_exception = None
            call_kwargs = dict(kwargs)
            if client_token_pos is not None and len(args) <= client_token_pos:
                if not call_kwargs.get("client_token"):
                    call_kwargs["client_token"] = _new_client_token(func.__name__)
            for attempt in range(max_retries + 1):
                try:
                    return func(self, *args, **call_kwargs)
                except ApiError as e:
                    last_exception = e
                    if e.code not in retry_codes or attempt == max_retries:
                        raise
                    wait_time = 2**attempt
                    time.sleep(wait_time)
            raise last_exception

        return wrapper

    return decorator


class DataAgentClient:
    """Synchronous client for Data Agent API.

    This client wraps the Alibaba Cloud DMS SDK and provides methods
    to interact with Data Agent sessions.
    """

    # Process-level cache: DMSUnit is tenant-scoped and rarely changes, so
    # we can safely cache it per (auth_fingerprint, region) for the whole run.
    _dms_unit_cache: dict = {}

    # Process-level cache for workspace ID resolution.
    _workspace_cache: dict = {}

    def __init__(self, config: DataAgentConfig):
        """Initialize the Data Agent client.

        Args:
            config: Configuration for the client.
        """
        self._config = config
        self._sdk_client: Optional[OpenApiClient] = None
        # Instance-level cached DMSUnit (None = not yet resolved).
        self._dms_unit: Optional[str] = None
        self._dms_unit_region: Optional[str] = None
        self._dms_unit_source: Optional[str] = None
        self._initialize_client()

    def _resolve_dms_unit(self, force_refresh: bool = False, region_id: Optional[str] = None) -> str:
        """Resolve the DMSUnit for the current tenant.

        Resolution priority:
          1. Explicit config/environment override (``DATA_AGENT_DMS_UNIT``).
          2. dms-enterprise ``GetActiveRouteUnit`` (2018-11-01).
          3. Region fallback.

        ``region_id`` lets callers resolve for a non-default RegionId while
        still honoring an explicit DMSUnit override.

        Falls back to the selected region when:
          - the response has no ``Route`` object (tenant belongs to the
            default unit, which equals the current region);
          - the call fails for any reason (network, auth, etc.).

        Result is cached at both instance and process level. Set
        ``force_refresh=True`` to bypass the cache.
        """
        if self._config.dms_unit:
            self._dms_unit = self._config.dms_unit
            self._dms_unit_region = region_id or self._config.region
            self._dms_unit_source = "config"
            return self._dms_unit

        region = region_id or self._config.region
        if not force_refresh and self._dms_unit and self._dms_unit_region == region:
            return self._dms_unit

        cache_key = (self._auth_type, region)
        if not force_refresh and cache_key in DataAgentClient._dms_unit_cache:
            self._dms_unit = DataAgentClient._dms_unit_cache[cache_key]
            self._dms_unit_source = "cache"
            return self._dms_unit

        resolved = region  # safe default
        source = "fallback"
        try:
            resp = self._call_dms_enterprise_route_unit(region)
            route = resp.get("Route") or resp.get("data") or resp.get("Data")
            if isinstance(route, dict):
                unit = route.get("RegionId") or route.get("regionId")
                if isinstance(unit, str) and unit:
                    resolved = unit
                    source = "route"
        except Exception:
            # Silent fallback: DMSUnit resolution must never block API calls.
            pass

        self._dms_unit = resolved
        self._dms_unit_region = region
        self._dms_unit_source = source
        DataAgentClient._dms_unit_cache[cache_key] = resolved
        return resolved

    def _call_dms_enterprise_route_unit(self, region_id: Optional[str] = None) -> dict:
        """Call dms-enterprise.GetActiveRouteUnit via a short-lived OpenApiClient.

        The dms-enterprise endpoint differs from the Data Agent endpoint, so
        we build a dedicated SDK client here (AK/SK mode only).
        """
        if self._auth_type == "api_key":
            # API_KEY auth does not map to dms-enterprise; keep the fallback.
            return {}

        region = region_id or self._config.region
        sdk_config = open_api_models.Config()
        sdk_config.endpoint = self._config.resolve_dms_enterprise_endpoint(region)
        sdk_config.user_agent = "AlibabaCloud-Agent-Skills/alibabacloud-data-agent-skill"
        sdk_config.credential = self._credential_client
        route_client = OpenApiClient(sdk_config)

        api_params = open_api_models.Params(
            action="GetActiveRouteUnit",
            version="2018-11-01",
            protocol="HTTPS",
            method="POST",
            auth_type="AK",
            style="RPC",
            pathname="/",
            req_body_type="json",
            body_type="json",
        )
        request = open_api_models.OpenApiRequest(query={})
        runtime = util_models.RuntimeOptions(read_timeout=10000, connect_timeout=5000)
        resp = route_client.call_api(api_params, request, runtime)
        return resp.get("body", {}) if isinstance(resp, dict) else {}

    def _call_init_personal_workspace(self) -> str:
        """Call InitDataAgentPersonalWorkspace to get the user's personal workspace ID.

        Returns:
            The workspace ID string.

        Raises:
            RuntimeError: If workspace ID cannot be parsed from response.
        """
        params = {
            "DMSUnit": self._resolve_dms_unit(),
            "RegionId": self._config.region,
        }
        resp = self._call_api(
            action="InitDataAgentPersonalWorkspace",
            version="2025-04-14",
            params=params,
        )
        # Try both PascalCase and camelCase response keys
        workspace_id = None
        data = resp.get("Data") or resp.get("data")
        if isinstance(data, dict):
            workspace_id = data.get("WorkspaceId") or data.get("workspaceId")
        # Fallback: workspaceId may be a top-level field in the response
        if not workspace_id:
            workspace_id = resp.get("WorkspaceId") or resp.get("workspaceId")
        if not workspace_id:
            raise RuntimeError(
                f"Failed to parse WorkspaceId from InitDataAgentPersonalWorkspace response: {resp}"
            )
        return workspace_id

    def _resolve_workspace_id(self, explicit: Optional[str] = None, force_refresh: bool = False) -> str:
        """Resolve workspace ID with priority: explicit > env > cache > API call.

        Args:
            explicit: Explicitly provided workspace ID (highest priority).
            force_refresh: Force re-fetch from API even if cached.

        Returns:
            Resolved workspace ID string.
        """
        if explicit is not None:
            self._workspace_source = "explicit"
            return explicit

        if self._config.workspace_id is not None:
            self._workspace_source = "env"
            return self._config.workspace_id

        cache_key = (self._auth_type, self._config.region, self._resolve_dms_unit())
        if not force_refresh and cache_key in DataAgentClient._workspace_cache:
            self._workspace_source = "personal"
            return DataAgentClient._workspace_cache[cache_key]

        workspace_id = self._call_init_personal_workspace()
        DataAgentClient._workspace_cache[cache_key] = workspace_id
        self._workspace_source = "personal"
        return workspace_id

    def _call_dms_enterprise_list_tag_meta_asset(
        self,
        tag_name: str,
        meta_type: str,
        meta_parent_id: Optional[str] = None,
        search_key: Optional[str] = None,
        page_number: int = 1,
        page_size: int = 50,
    ) -> dict:
        """Call dms-enterprise.ListTagMetaAsset via a short-lived OpenApiClient.

        Args:
            tag_name: The tag name to query assets for.
            meta_type: Meta asset type (e.g. META_DATABASE, META_TABLE).
            meta_parent_id: Optional parent ID for hierarchical queries.
            search_key: Optional keyword filter.
            page_number: Page number (1-based).
            page_size: Number of results per page.

        Returns:
            Raw response body dict from dms-enterprise.

        Raises:
            ApiError: If the API call fails.
        """
        if self._auth_type == "api_key":
            # API_KEY mode has no AK/SK credentials for dms-enterprise
            import warnings
            warnings.warn(
                "ListTagMetaAsset requires AK/SK credentials. "
                "Falling back is handled by the caller.",
                stacklevel=2,
            )
            raise ApiError(
                "ListTagMetaAsset not available in API_KEY mode (no AK/SK credentials).",
                code="UnsupportedAuthMode",
                request_id=None,
            )

        sdk_config = open_api_models.Config()
        sdk_config.endpoint = self._config.resolve_dms_enterprise_endpoint()
        sdk_config.user_agent = "AlibabaCloud-Agent-Skills/alibabacloud-data-agent-skill"
        sdk_config.credential = self._credential_client
        tag_client = OpenApiClient(sdk_config)

        # Note: All query param values MUST be strings to avoid
        # quote() errors in the Tea SDK signing logic.
        query_params: dict = {
            "TagName": tag_name,
            "MetaType": meta_type,
            "PageNumber": str(page_number),
            "PageSize": str(page_size),
        }
        if meta_parent_id is not None:
            query_params["MetaParentId"] = str(meta_parent_id)
        if search_key is not None:
            query_params["SearchKey"] = search_key

        api_params = open_api_models.Params(
            action="ListTagMetaAsset",
            version="2018-11-01",
            protocol="HTTPS",
            method="POST",
            auth_type="AK",
            style="RPC",
            pathname="/",
            req_body_type="json",
            body_type="json",
        )
        request = open_api_models.OpenApiRequest(query=query_params)
        runtime = util_models.RuntimeOptions(read_timeout=10000, connect_timeout=5000)

        try:
            resp = tag_client.call_api(api_params, request, runtime)
            return resp.get("body", {}) if isinstance(resp, dict) else {}
        except TeaException as e:
            raise ApiError(
                message=str(e),
                code=getattr(e, "code", "TeaException"),
                request_id=getattr(e, "data", {}).get("RequestId") if hasattr(e, "data") and isinstance(e.data, dict) else None,
            )

    def _initialize_client(self) -> None:
        """Initialize the underlying SDK client based on authentication type."""
        # Determine authentication type
        if self._config.api_key:
            # API_KEY authentication - don't initialize the Tea SDK client
            self._sdk_client = None
            self._auth_type = "api_key"
        else:
            # AK/SK authentication: static credentials from the plain
            # credentials file when configured, otherwise the Alibaba Cloud
            # default credential chain (env vars, ~/.aliyun/config.json,
            # ECS role, OIDC role, etc.)
            try:
                self._credential_client = self._config.build_credential_client()
                self._auth_type = (
                    "credential_file"
                    if self._config.using_credentials_file
                    else "default_credential_chain"
                )
                # Force credential resolution early so misconfiguration
                # fails here rather than at the first API call.
                self._credential_client.get_credential()

                sdk_config = open_api_models.Config()
                sdk_config.endpoint = self._config.endpoint
                sdk_config.user_agent = "AlibabaCloud-Agent-Skills/alibabacloud-data-agent-skill"
                sdk_config.credential = self._credential_client

                self._sdk_client = OpenApiClient(sdk_config)
            except Exception as e:
                raise AuthenticationError(
                    f"Failed to get credentials: {e}. "
                    "Please configure credentials via the credentials file "
                    "(DATA_AGENT_CREDENTIALS_FILE), ~/.aliyun/config.json, "
                    "environment variables, or instance role."
                )

        # Instance attribute to track workspace resolution source
        self._workspace_source: Optional[str] = None

    def _call_api(
        self,
        action: str,
        version: str,
        params: dict,
        method: str = "POST",
        body: dict = None,
    ) -> dict:
        """Make a generic API call.

        Args:
            action: API action name.
            version: API version.
            params: Request parameters (for query string).
            method: HTTP method (default: POST).
            body: Request body (for JSON body).

        Returns:
            API response as dictionary.

        Raises:
            ApiError: If the API call fails.
            AuthenticationError: If authentication fails.
        """
        if self._auth_type == "api_key":
            # Handle API_KEY authentication using direct HTTP requests
            return self._call_api_with_api_key(action, version, params, method, body)
        else:
            # Handle traditional AK/SK authentication using Tea SDK
            return self._call_api_with_ak_sk(action, version, params, method, body)

    def _call_api_with_api_key(
        self,
        action: str,
        version: str,
        params: dict,
        method: str = "POST",
        body: dict = None,
    ) -> dict:
        """Make an API call using API_KEY authentication."""
        # Only the chat/stream APIs are served by the stream gateway — every
        # other action (session/workspace/custom-agent/file/usage/feedback
        # APIs) lives on the control-plane gateway. Verified against the pre
        # gateways: ListFileUpload / DescribeDataAgentUsage /
        # UpdateDataAgentSession / CreateDataAgentFeedback all answer
        # "Invalid Action" on the stream gateway.
        data_plane_actions = ['SendChatMessage', 'GetChatContent']

        # Choose the correct endpoint based on the action type. Both honor the
        # configured overrides (``endpoint`` for the control plane,
        # ``stream_endpoint`` for the data plane) so API_KEY traffic can target
        # non-public gateways; the defaults match the region-derived public hosts.
        if action in data_plane_actions:
            base_endpoint = self._config.resolve_api_key_stream_endpoint()
        else:
            base_endpoint = self._config.endpoint

        # Add the required RegionId parameter to params
        params['RegionId'] = self._config.region

        # Prepare request with PascalCase parameters
        prepared_params = APIAdapter.prepare_request_params(params, api_action=action)

        # For API_KEY authentication, we should put Action and Version in the body
        # according to standard OpenAPI practices
        if body is None:
            body = {}

        # Add action and version to the body instead of query params
        body.update({
            'Action': action,
            'Version': version
        })

        # Update body with prepared parameters
        body.update(prepared_params)

        prepared_body = APIAdapter.prepare_request_body(body)

        # Build the URL for the API call (without Action and Version in query)
        base_url = f"https://{base_endpoint}"

        # Construct the URL without action/version in query string since they're in body
        full_url = base_url

        # Set up headers with API_KEY
        headers = {
            'x-api-key': self._config.api_key,
            'Content-Type': 'application/json',
            'Accept': 'application/json',
            'User-Agent': 'AlibabaCloud-Agent-Skills/alibabacloud-data-agent-skill',
        }

        # Add debug logging if enabled via environment variable
        if _is_debug_api_enabled():
            import pprint
            auth_type_display = "API_KEY" if self._auth_type == "api_key" else "AK/SK"
            print(f"[DEBUG] API Call: {action}")
            print(f"[DEBUG] Authentication Type: {auth_type_display}")
            print(f"[DEBUG] Method: {method}")
            print(f"[DEBUG] URL: {full_url}")
            print(f"[DEBUG] Headers: {pprint.pformat(_redact_sensitive_values(headers))}")
            if prepared_body:
                print(f"[DEBUG] Body: {pprint.pformat(_redact_sensitive_values(prepared_body))}")

        try:
            # Make the API call - Action and Version are now in the body
            if method.upper() == "GET":
                # For GET requests, we might still need to put action/version in query
                query_params = {'Action': action, 'Version': version}
                import urllib.parse
                query_string = urllib.parse.urlencode(query_params)
                full_url_with_params = f"{full_url}?{query_string}"
                response = requests.get(full_url_with_params, headers=headers, timeout=self._config.timeout)
            elif method.upper() == "POST":
                # For POST requests, Action and Version are in the body
                response = requests.post(full_url, headers=headers, json=prepared_body, timeout=self._config.timeout)
            else:
                # For other methods, default to POST with body
                response = requests.request(method, full_url, headers=headers, json=prepared_body, timeout=self._config.timeout)

            # Check response status
            response.raise_for_status()

            # Parse response JSON
            response_data = response.json()

            # Process response to convert keys to camelCase
            processed_response = APIAdapter.process_response(response_data, api_action=action)

            # Add debug logging for response if enabled
            if _is_debug_api_enabled():
                import pprint
                print(f"[DEBUG] Response for {action}: {pprint.pformat(_redact_sensitive_values(processed_response))}")

            return processed_response
        except requests.exceptions.RequestException as e:
            # Handle HTTP request errors
            error_msg = f"API call failed: {str(e)}"
            if hasattr(e, 'response') and e.response is not None:
                error_msg += f", Status: {e.response.status_code}, Body: {e.response.text[:500]}"
            raise ApiError(error_msg, code="HTTPRequestError", request_id=None)

    def _call_api_with_ak_sk(
        self,
        action: str,
        version: str,
        params: dict,
        method: str = "POST",
        body: dict = None,
    ) -> dict:
        """Make an API call using AK/SK authentication."""
        # Prepare request with PascalCase parameters
        prepared_params = APIAdapter.prepare_request_params(params, api_action=action)
        prepared_body = APIAdapter.prepare_request_body(body) if body else None

        api_params = open_api_models.Params(
            action=action,
            version=version,
            protocol="HTTPS",
            method=method,
            auth_type="AK",
            style="RPC",
            pathname="/",
            req_body_type="json",
            body_type="json",
        )

        request = open_api_models.OpenApiRequest(
            query=OpenApiUtilClient.query(prepared_params),
            body=prepared_body,
        )

        runtime = util_models.RuntimeOptions(
            read_timeout=self._config.timeout * 1000,
            connect_timeout=30000,
        )

        # Add debug logging if enabled via environment variable
        if _is_debug_api_enabled():
            import pprint
            auth_type_display = "API_KEY" if self._auth_type == "api_key" else "AK/SK"
            print(f"[DEBUG] API Call: {action}")
            print(f"[DEBUG] Authentication Type: {auth_type_display}")
            print(f"[DEBUG] Method: {method}")
            print(f"[DEBUG] Params: {pprint.pformat(_redact_sensitive_values(prepared_params))}")
            if prepared_body:
                print(f"[DEBUG] Body: {pprint.pformat(_redact_sensitive_values(prepared_body))}")

        try:
            response = self._sdk_client.call_api(api_params, request, runtime)

            # Process response to convert keys to camelCase
            processed_response = APIAdapter.process_response(response.get("body", {}), api_action=action)

            # Add debug logging for response if enabled
            if _is_debug_api_enabled():
                import pprint
                print(f"[DEBUG] Response for {action}: {pprint.pformat(_redact_sensitive_values(processed_response))}")

            return processed_response
        except TeaException as e:
            self._handle_tea_exception(e)

    def _handle_tea_exception(self, e: TeaException) -> None:
        """Convert TeaException to appropriate custom exception.

        Args:
            e: The TeaException to handle.

        Raises:
            AuthenticationError: For authentication-related errors.
            ApiError: For other API errors.
        """
        code = getattr(e, "code", "Unknown")
        message = getattr(e, "message", str(e))
        request_id = None
        if hasattr(e, "data") and isinstance(e.data, dict):
            request_id = e.data.get("RequestId")

        auth_error_codes = ("InvalidAccessKeyId.NotFound", "SignatureDoesNotMatch", "Forbidden")
        if code in auth_error_codes:
            raise AuthenticationError(message, code=code, request_id=request_id)

        raise ApiError(message, code=code, request_id=request_id)

    def delete_data_agent(self) -> dict:
        """Release the runtime instance belonging to the authenticated caller.

        No business parameters are required; DMSUnit is deprecated and omitted.
        Running sessions become unavailable. Starting a new analysis afterwards
        provisions the latest runtime resources; this call does not provision them.
        """
        # Do not retry an identity-scoped deletion: an ambiguous first result
        # could otherwise cause a retry to release a newly provisioned runtime.
        response = self._call_api(
            action="DeleteDataAgent",
            version="2025-04-14",
            params={},
        )
        if str(response.get("success", "")).lower() != "true":
            raise ApiError(
                response.get("errorMessage") or response.get("message")
                or "DeleteDataAgent did not confirm success.",
                code=response.get("errorCode") or response.get("code") or "Unknown",
                request_id=response.get("requestId"),
            )
        return response

    @retry_on_error(max_retries=3)
    def create_session(
        self,
        database_id: Optional[str] = None,
        title: str = "data-agent-session",
        mode: Optional[str] = None,
        enable_search: bool = False,
        file_id: Optional[str] = None,  # 添加文件ID参数
        workspace_id: Optional[str] = None,
        custom_agent_id: Optional[str] = None,
        custom_agent_stage: str = "prod",
        client_token: Optional[str] = None,
        plan_mode: Optional[str] = "force",
    ) -> SessionInfo:
        """Create a new Data Agent session.

        Args:
            database_id: Optional database ID to bind to the session.
            title: Session title (required by API).
            mode: Optional session mode: "auto", "lite", "pro", "ultra".
            enable_search: Whether to enable search capability in the session.
            file_id: Optional file ID for file-based analysis session.
            workspace_id: Optional workspace ID to bind to the session.
            custom_agent_id: Optional custom agent ID to use for the session.
            custom_agent_stage: Stage of the custom agent, "prod" (default) or "debug".
            client_token: Optional idempotency token, auto-generated for retries.
            plan_mode: Plan mode: "force" (default, always plan) or "disable" (skip planning).

        Returns:
            SessionInfo with agent_id and session_id.

        Raises:
            SessionCreationError: If session creation fails.
        """
        params = {
            "Title": title,
            "DMSUnit": self._resolve_dms_unit(),
        }
        if database_id:
            params["DatabaseId"] = database_id
        if mode:
            params["Mode"] = mode
        if file_id:
            # 当指定了文件ID时，会话将是基于文件的分析
            params["File"] = file_id
        if workspace_id:
            params["WorkspaceId"] = workspace_id
        if client_token:
            params["ClientToken"] = client_token

        # Ensure session configuration (language/mode/search) is persisted on server
        session_config = {"Language": "CHINESE", "EnableSearch": enable_search}
        if mode:
            session_config["Mode"] = mode
        if plan_mode:
            session_config["PlanMode"] = plan_mode
        if custom_agent_id:
            session_config["CustomAgentId"] = custom_agent_id
            session_config["CustomAgentStage"] = custom_agent_stage

        # For API_KEY auth, SessionConfig should be a JSON object, not string
        # For AK/SK auth, SessionConfig should be a JSON string
        if self._auth_type == "api_key":
            params["SessionConfig"] = session_config
        else:
            params["SessionConfig"] = json.dumps(session_config)

        try:
            response = self._call_api(
                action="CreateDataAgentSession",
                version="2025-04-14",
                params=params,
            )

            # Response data is nested under 'Data' field
            # After API adapter processing, the keys are in camelCase
            data = response.get("data", response)  # Changed from "Data" to "data"
            agent_id = data.get("agentId", "")  # Changed from "AgentId" to "agentId"
            session_id = data.get("sessionId", "")  # Changed from "SessionId" to "sessionId"
            agent_status = data.get("agentStatus", "").upper()  # Changed from "AgentStatus" to "agentStatus"

            if not agent_id or not session_id:
                raise SessionCreationError(
                    f"Invalid response: missing AgentId or SessionId. Response: {response}"
                )

            # Map AgentStatus to SessionStatus.
            # AgentStatus reflects the underlying agent compute readiness:
            # when RUNNING, the session can accept messages even though
            # DescribeDataAgentSession may still report SessionStatus as
            # CREATING for a prolonged period.
            if agent_status == "RUNNING":
                status = SessionStatus.RUNNING
            elif agent_status == "STOPPED":
                status = SessionStatus.STOPPED
            elif agent_status == "FAILED":
                status = SessionStatus.FAILED
            else:
                status = SessionStatus.CREATING

            return SessionInfo(
                agent_id=agent_id,
                session_id=session_id,
                status=status,
                database_id=database_id,
                workspace_id=workspace_id,
            )
        except ApiError as e:
            raise SessionCreationError(f"Failed to create session: {e.message}", request_id=e.request_id)

    @retry_on_error(max_retries=3)
    def describe_session(self, session_id: str, agent_id: str = "", workspace_id: str = "") -> SessionInfo:
        """Get the status of a session.

        Args:
            session_id: The session ID.
            agent_id: The agent ID (optional, but used to construct the request properly).
            workspace_id: The workspace ID (required for workspace-bound sessions).

        Returns:
            SessionInfo with current status.
        """
        params = {
            "SessionId": session_id,
            "DMSUnit": self._resolve_dms_unit(),
        }
        # Only include AgentId in the request if it's provided
        # According to API spec, DescribeDataAgentSession doesn't require AgentId
        if agent_id:
            params["AgentId"] = agent_id
        if workspace_id:
            params["WorkspaceId"] = workspace_id

        response = self._call_api(
            action="DescribeDataAgentSession",
            version="2025-04-14",
            params=params,
        )

        request_id = response.get("requestId", "")  # Changed from "RequestId" to "requestId"
        data = response.get("data", response)  # Changed from "Data" to "data"
        status_str = data.get("sessionStatus", data.get("status", "CREATING"))  # Changed from "SessionStatus"/"Status" to "sessionStatus"/"status"
        # Case-insensitive match + alias map for gateway/backend variants
        # ("init", "INITIALIZING", "RECOVERABLE", ...); unknown → CREATING.
        status = normalize_session_status(status_str)

        # Capture the real AgentId from response if available
        # The agent_id in the response should take precedence over the one passed in
        real_agent_id = data.get("agentId") or data.get("AgentId") or agent_id  # Try both transformed and original

        session_config = data.get("sessionConfig") or {}
        return SessionInfo(
            agent_id=real_agent_id,
            session_id=session_id,
            status=status,
            database_id=data.get("databaseId"),  # Changed from "DatabaseId" to "databaseId"
            workspace_id=workspace_id,
            request_id=request_id,
            title=data.get("title"),
            mode=session_config.get("mode"),
            raw_status=status_str,
        )

    @retry_on_error(max_retries=3)
    def send_message(
        self,
        message: str,
        session_id: str = "",
        agent_id: str = "",
        message_type: str = "primary",
        data_source: Optional[DataSource] = None,
        language: str = "CHINESE",
        workspace_id: str = "",
        mode: Optional[str] = None,
        data_sources: Optional[list] = None,
        report_config: Optional[dict] = None,
        permission_config: Optional[dict] = None,
        plan_mode: Optional[str] = None,
        enable_search: Optional[bool] = None,
        custom_agent_id: Optional[str] = None,
        custom_agent_stage: str = "prod",
    ) -> dict:
        """Send a message to the Data Agent.

        Args:
            message: The user's natural language query.
            session_id: The session ID. May be omitted for the first message
                of a session — the backend then creates the session and the
                response data carries the new AgentId / SessionId.
            agent_id: Deprecated — no longer needed. The backend resolves the
                agent automatically; kept only for backward compatibility and
                omitted from the wire when empty.
            message_type: Message type (default: "primary"; "report" for web
                report generation requests, "cancel" to cancel the session,
                "diag" for report-diagnosis requests where the message text
                is the complaint about the session's report).
            data_source: Optional DataSource with database metadata. Both
                this and ``data_sources`` are session-level (merged
                server-side); the backend recommends ``data_sources``.
            language: Response language (default: "CHINESE").
            workspace_id: The workspace ID (required for workspace-bound sessions).
            mode: Optional per-message analysis mode. Supported values:
                ``auto``, ``lite``, ``pro``, ``ultra``. When provided it is
                injected into ``SessionConfig.Mode`` and overrides the
                session-level mode for this single request.
            plan_mode: Optional per-message plan mode, ``force`` (always
                generate an execution plan) or ``disable`` (skip planning).
                When provided it is injected into ``SessionConfig.PlanMode``;
                omitted by default so the session-level setting applies.
            enable_search: Whether to enable search capability in the session,
                injected into ``SessionConfig.EnableSearch``. Carry it on the
                first message of an implicitly created session (the
                CreateDataAgentSession path set it at session level).
            data_sources: Optional list of session-level data sources, each a
                dict like ``{"DataSourceType": "remote_data_center", "FileId":
                "..."}``. Both ``DataSource`` and ``DataSources`` are
                session-level (repeated passes are merged server-side with
                equal priority); the backend recommends ``DataSources``, so
                carrying them on the first message of a session is enough —
                follow-ups inherit the session scope.
            report_config: Optional report configuration dict for
                ``message_type="report"``, e.g. ``{"ReportType": "WebReport",
                "ReportTheme": "default", "ReportPrompt": "..."}``. Injected
                as ``TaskConfig.ReportConfig``.
            permission_config: Optional session-level permission dict, e.g.
                ``{"DefaultAction": "deny"}`` — the default action for tables
                not declared in any data-source permission. Injected into
                ``SessionConfig.PermissionConfig``.
            custom_agent_id: Optional custom agent ID. When the message
                implicitly creates the session, the binding is expressed via
                ``SessionConfig.CustomAgentId`` / ``CustomAgentStage`` without
                a prior CreateDataAgentSession call (AK/SK auth; the API_KEY
                path still requires explicit pre-creation).
            custom_agent_stage: Stage of the custom agent, ``prod`` (default)
                or ``debug``.

        Returns:
            Response from the API.
        """
        # Query parameters for RPC style API.
        # AgentId / SessionId are auto-filled by the backend when omitted, so
        # the first SendChatMessage of a session can create it directly; the
        # response then carries the new AgentId / SessionId.
        params = {
            "Message": message,
            "MessageType": message_type,
        }
        if agent_id:
            params["AgentId"] = agent_id
        if session_id:
            params["SessionId"] = session_id
        dms_unit = self._resolve_dms_unit()
        if dms_unit:
            params["DMSUnit"] = dms_unit
        if workspace_id:
            params["WorkspaceId"] = workspace_id

        # SessionConfig format depends on auth type
        # For API_KEY auth: JSON object
        # For AK/SK auth: JSON string
        session_config: dict = {"Language": language}
        if mode:
            # Per-message mode override; supports auto / lite / pro / ultra.
            session_config["Mode"] = mode
        if plan_mode:
            session_config["PlanMode"] = plan_mode
        if enable_search is not None:
            session_config["EnableSearch"] = enable_search
        if permission_config:
            session_config["PermissionConfig"] = permission_config
        if custom_agent_id:
            session_config["CustomAgentId"] = custom_agent_id
            session_config["CustomAgentStage"] = custom_agent_stage or "prod"
        if self._auth_type == "api_key":
            params["SessionConfig"] = session_config
        else:
            params["SessionConfig"] = json.dumps(session_config)

        # DataSource format depends on auth type
        # For API_KEY auth: JSON object
        # For AK/SK auth: JSON string
        if data_source:
            if self._auth_type == "api_key":
                params["DataSource"] = data_source.to_api_dict()
            else:
                params["DataSource"] = json.dumps(data_source.to_api_dict())

        if data_sources:
            if self._auth_type == "api_key":
                params["DataSources"] = data_sources
            else:
                params["DataSources"] = json.dumps(data_sources)

        if report_config:
            task_config = {"ReportConfig": report_config}
            if self._auth_type == "api_key":
                params["TaskConfig"] = task_config
            else:
                params["TaskConfig"] = json.dumps(task_config)

        return self._call_api(
            action="SendChatMessage",
            version="2025-04-14",
            params=params,
        )

    @retry_on_error(max_retries=3)
    def get_chat_content(
        self,
        session_id: str,
        agent_id: str = "",
        checkpoint: Optional[str] = None,
    ) -> dict:
        """Get chat content from the Data Agent.

        Args:
            session_id: The session ID.
            agent_id: Deprecated — no longer needed. Omitted from the wire
                when empty; kept only for backward compatibility.
            checkpoint: Optional checkpoint for incremental fetching.

        Returns:
            Response containing content blocks.
        """
        params = {
            "SessionId": session_id,
        }
        if agent_id:
            params["AgentId"] = agent_id
        if checkpoint:
            params["Checkpoint"] = checkpoint

        return self._call_api(
            action="GetChatContent",
            version="2025-04-14",
            params=params,
        )

    @retry_on_error(max_retries=3)
    def get_file_upload_signature(
        self,
        filename: str,
        file_size: int,
        workspace_id: Optional[str] = None,
        dms_unit: Optional[str] = None,
    ) -> dict:
        """Get OSS upload signature for file upload.

        Args:
            filename: Name of the file to upload.
            file_size: Size of the file in bytes.
            workspace_id: Optional workspace ID to upload into (defaults to
                the caller's personal workspace).
            dms_unit: Optional DMSUnit override (defaults to auto resolution).

        Returns:
            Response containing upload URL and credentials.
        """
        params = {
            "FileName": filename,
            "FileSize": file_size,
            "DMSUnit": dms_unit or self._resolve_dms_unit(),
        }
        if workspace_id:
            params["WorkspaceId"] = workspace_id

        return self._call_api(
            action="DescribeFileUploadSignature",
            version="2025-04-14",
            params=params,
        )

    @retry_on_error(max_retries=3)
    def file_upload_callback(
        self,
        file_id: str,
        filename: str,
        upload_location: str,
        file_size: int = None,
        workspace_id: Optional[str] = None,
        dms_unit: Optional[str] = None,
        client_token: Optional[str] = None,
    ) -> dict:
        """Notify the service that file upload is complete.

        Args:
            file_id: The file ID from upload signature (uploadDir).
            filename: The original filename.
            upload_location: The full OSS path (UploadHost/UploadDir/Filename).
            file_size: The file size in bytes (required for API_KEY auth).
            workspace_id: Optional workspace ID the file was uploaded into.
            dms_unit: Optional DMSUnit override (defaults to auto resolution).
            client_token: Optional idempotency token, auto-generated for retries.

        Returns:
            Response confirming the upload.
        """
        params = {
            "Filename": filename,
            "UploadLocation": upload_location,
            "FileFrom": "Skill",
            "DMSUnit": dms_unit or self._resolve_dms_unit(),
        }
        if file_size:
            params["FileSize"] = file_size
        if workspace_id:
            params["WorkspaceId"] = workspace_id
        if client_token:
            params["ClientToken"] = client_token

        return self._call_api(
            action="FileUploadCallback",
            version="2025-04-14",
            params=params,
        )

    @retry_on_error(max_retries=3)
    def list_files(self, session_id: str, file_category: Optional[str] = None) -> dict:
        """List files associated with a session.

        Args:
            session_id: The session ID.
            file_category: Optional filter, e.g. "WebReport" for agent-generated
                           reports, or None to list all files.

        Returns:
            Response containing file list.
        """
        params = {
            "SessionId": session_id,
        }
        if file_category:
            params["FileCategory"] = file_category

        return self._call_api(
            action="ListFileUpload",
            version="2025-04-14",
            params=params,
        )

    @retry_on_error(max_retries=3)
    def list_file_databases(
        self,
        search_key: Optional[str] = None,
        page_number: int = 1,
        page_size: int = 50,
    ) -> dict:
        """List databases registered in DMS Data Center (legacy file-based).

        Args:
            search_key: Optional keyword to filter by database or instance name.
            page_number: Page number (1-based).
            page_size: Number of results per page.

        Returns:
            Raw API response containing Data.List with database metadata.
        """
        params: dict = {
            "PageNumber": page_number,
            "PageSize": page_size,
        }
        if search_key:
            params["SearchKey"] = search_key
        return self._call_api(
            action="ListDataCenterDatabase",
            version="2025-04-14",
            params=params,
        )

    @retry_on_error(max_retries=3)
    def list_databases(self, workspace_id=None, search_key=None, page_number=1, page_size=50) -> dict:
        """List databases in workspace via ListTagMetaAsset.

        Falls back to list_file_databases in API_KEY mode.

        Args:
            workspace_id: Optional explicit workspace ID.
            search_key: Optional keyword filter.
            page_number: Page number (1-based).
            page_size: Number of results per page.

        Returns:
            Raw response dict from ListTagMetaAsset or fallback.
        """
        if self._auth_type != "default_credential_chain":
            # Fallback: API_KEY mode cannot call dms-enterprise directly
            return self.list_file_databases(
                search_key=search_key,
                page_number=page_number,
                page_size=page_size,
            )
        ws = self._resolve_workspace_id(workspace_id)
        # ListTagMetaAsset tags are region-scoped. Do not use DMSUnit here:
        # DATA_AGENT_DMS_UNIT may differ from DATA_AGENT_REGION for session APIs.
        tag = f"sys::DMS-DA::{self._config.region}::space:{ws}"
        return self._call_dms_enterprise_list_tag_meta_asset(
            tag_name=tag,
            meta_type="META_DATABASE",
            search_key=search_key,
            page_number=page_number,
            page_size=page_size,
        )

    @retry_on_error(max_retries=3)
    def list_file_tables(
        self,
        instance_name: str,
        database_name: str,
        page_number: int = 1,
        page_size: int = 200,
    ) -> dict:
        """List tables inside a DMS Data Center database (legacy file-based).

        Args:
            instance_name: The InstanceName returned by list_file_databases.
            database_name: The DatabaseName returned by list_file_databases.
            page_number: Page number (1-based).
            page_size: Number of results per page.

        Returns:
            Raw API response containing Data.List with table metadata.
        """
        return self._call_api(
            action="ListDataCenterTable",
            version="2025-04-14",
            params={
                "InstanceName": instance_name,
                "DatabaseName": database_name,
                "PageNumber": page_number,
                "PageSize": page_size,
            },
        )

    @retry_on_error(max_retries=3)
    def list_tables(self, agent_db_id, workspace_id=None, page_number=1, page_size=200) -> dict:
        """List tables for a database via ListTagMetaAsset.

        Falls back to list_file_tables in API_KEY mode (not supported without extra params).

        Args:
            agent_db_id: The database meta parent ID.
            workspace_id: Optional explicit workspace ID.
            page_number: Page number (1-based).
            page_size: Number of results per page.

        Returns:
            Raw response dict from ListTagMetaAsset or raises error for unsupported mode.
        """
        if self._auth_type != "default_credential_chain":
            raise ApiError(
                "list_tables via ListTagMetaAsset not available in API_KEY mode. "
                "Use list_file_tables(instance_name, database_name) instead.",
                code="UnsupportedAuthMode",
                request_id=None,
            )
        ws = self._resolve_workspace_id(workspace_id)
        # ListTagMetaAsset tags are region-scoped. Do not use DMSUnit here:
        # DATA_AGENT_DMS_UNIT may differ from DATA_AGENT_REGION for session APIs.
        tag = f"sys::DMS-DA::{self._config.region}::space:{ws}"
        return self._call_dms_enterprise_list_tag_meta_asset(
            tag_name=tag,
            meta_type="META_TABLE",
            meta_parent_id=agent_db_id,
            page_number=page_number,
            page_size=page_size,
        )

    @retry_on_error(max_retries=3)
    def delete_file(self, file_id: str, client_token: Optional[str] = None) -> dict:
        """Delete an uploaded file.

        Args:
            file_id: The file ID to delete.
            client_token: Optional idempotency token, auto-generated for retries.

        Returns:
            Response confirming deletion.
        """
        if not file_id or not str(file_id).strip():
            raise ApiError("FileId is required before deleting a file.", code="InvalidParameter", request_id=None)

        params = {
            "FileId": str(file_id).strip(),
        }
        if client_token:
            params["ClientToken"] = client_token

        return self._call_api(
            action="DeleteFileUpload",
            version="2025-04-14",
            params=params,
        )

    @retry_on_error(max_retries=3)
    def add_data_center_table(
        self,
        instance_name: str,
        database_name: str,
        dms_instance_id: int,
        dms_db_id: int,
        table_name_list: list[str],
        db_type: str = "mysql",
        region_id: Optional[str] = None,
        client_token: Optional[str] = None,
    ) -> dict:
        """Add DMS database tables to Data Agent Data Center.

        This method imports DMS database tables into Data Agent's Data Center
        using the AddDataCenterTable API.

        Args:
            instance_name: RDS instance name (e.g., "rm-xxxxx").
            database_name: Database name (e.g., "employees").
            dms_instance_id: DMS instance ID (e.g., 1234567).
            dms_db_id: DMS database ID (e.g., 12345678).
            table_name_list: List of table names to import (required).
            db_type: Database type (default: "mysql").
            region_id: Optional region ID (defaults to config.region).
            client_token: Optional idempotency token, auto-generated for retries.

        Returns:
            API response containing the import result.

        Raises:
            ApiError: If the API call fails.
        """
        region = region_id or self._config.region
        dms_unit = self._resolve_dms_unit(region_id=region)

        if not table_name_list:
            raise ApiError("At least one table name is required for import.", code="InvalidParameter", request_id=None)
        normalized_tables = [str(table).strip() for table in table_name_list if str(table).strip()]
        if not normalized_tables:
            raise ApiError("At least one table name is required for import.", code="InvalidParameter", request_id=None)

        table_list_json = json.dumps(normalized_tables, ensure_ascii=False)

        params = {
            "DMSUnit": dms_unit,
            "RegionId": region,
            "ImportType": "DMS",
            "InstanceName": instance_name,
            "DmsInstanceId": dms_instance_id,
            "DbType": db_type,
            "DatabaseName": database_name,
            "DmsDbId": dms_db_id,
            "TableNameList": table_list_json,
        }
        if client_token:
            params["ClientToken"] = client_token

        return self._call_api(
            action="AddDataCenterTable",
            version="2025-04-14",
            params=params,
        )

    @retry_on_error(max_retries=3)
    def list_sessions(
        self,
        start_time: Optional[str] = None,
        end_time: Optional[str] = None,
        page_number: int = 1,
        page_size: int = 10,
        custom_agent_id: Optional[str] = None,
        workspace_id: Optional[str] = None,
        mode: Optional[str] = None,
        title: Optional[str] = None,
        is_saved: Optional[bool] = None,
        query_type: Optional[str] = None,
    ) -> dict:
        """List Data Agent sessions.

        For API_KEY authentication, uses startTime/endTime parameters.
        For AK/SK authentication, uses createStartTime/createEndTime parameters.
        Time values are epoch milliseconds in both cases; RFC3339/ISO strings
        are rejected by the gateway.

        Args:
            start_time: Start time for filtering sessions (epoch milliseconds).
            end_time: End time for filtering sessions (epoch milliseconds).
            page_number: Page number (1-based).
            page_size: Number of results per page.
            custom_agent_id: Filter by custom agent ID.
            workspace_id: Filter by workspace ID.
            mode: Filter by session mode (auto/lite/pro/ultra).
            title: Filter by session title.
            is_saved: Filter by saved status.
            query_type: Filter by query type.

        Returns:
            Response containing list of sessions.
        """
        params = {
            "PageNumber": page_number,
            "PageSize": page_size,
        }

        workspace_id = workspace_id or self._resolve_workspace_id()
        if workspace_id:
            params["WorkspaceId"] = workspace_id

        if custom_agent_id:
            params["CustomAgentId"] = custom_agent_id
        if mode:
            params["Mode"] = mode
        if title:
            params["Title"] = title
        if is_saved is not None:
            params["IsSaved"] = is_saved
        if query_type:
            params["QueryType"] = query_type

        # Add time parameters based on authentication type
        if self._auth_type == "api_key":
            # For API_KEY auth, use the new parameter names
            if start_time:
                params["StartTime"] = start_time
            if end_time:
                params["EndTime"] = end_time
        else:
            # For AK/SK auth, use the original parameter names
            if start_time:
                params["CreateStartTime"] = start_time
            if end_time:
                params["CreateEndTime"] = end_time

        return self._call_api(
            action="ListDataAgentSession",
            version="2025-04-14",
            params=params,
        )

    @retry_on_error(max_retries=3)
    def list_workspaces(
        self,
        workspace_type: str = "MY",
        workspace_name: Optional[str] = None,
        page_number: int = 1,
        page_size: int = 50,
        order: Optional[str] = None,
        order_by: Optional[str] = None,
    ) -> dict:
        """List Data Agent workspaces under the current account.

        Args:
            workspace_type: Workspace type filter ("MY" or "ALL").
            workspace_name: Optional workspace name filter.
            page_number: Page number (1-based).
            page_size: Number of results per page.
            order: Optional sort order ("ASC" or "DESC").
            order_by: Optional sort field (e.g., "CreateTime").

        Returns:
            Raw API response containing workspace list.
        """
        params: dict = {
            "WorkspaceType": workspace_type,
            "PageNumber": page_number,
            "PageSize": page_size,
            "DMSUnit": self._resolve_dms_unit(),
        }
        if workspace_name:
            params["WorkspaceName"] = workspace_name
        if order:
            params["Order"] = order
        if order_by:
            params["OrderBy"] = order_by

        return self._call_api(
            action="ListDataAgentWorkspace",
            version="2025-04-14",
            params=params,
            method="GET",
        )

    @retry_on_error(max_retries=3)
    def list_custom_agents(
        self,
        workspace_id: Optional[str] = None,
        search_key: Optional[str] = None,
        status: str = "RELEASED",
        page_number: int = 1,
        page_size: int = 20,
    ) -> dict:
        """List custom agents, default to RELEASED status only.

        Args:
            workspace_id: Optional workspace ID to filter agents.
            search_key: Fuzzy search by name or description.
            status: Filter by status (default: RELEASED).
            page_number: Page number (1-based).
            page_size: Number of results per page.

        Returns:
            Raw API response containing custom agent list.
        """
        params: dict = {
            "PageNumber": page_number,
            "PageSize": page_size,
            "Status": status,
            "DMSUnit": self._resolve_dms_unit(),
        }
        if workspace_id:
            params["WorkspaceId"] = workspace_id
        if search_key:
            params["SearchKey"] = search_key

        return self._call_api(
            action="ListCustomAgent",
            version="2025-04-14",
            params=params,
            method="POST",
        )

    @retry_on_error(max_retries=3)
    def describe_custom_agent(
        self,
        custom_agent_id: str,
        workspace_id: Optional[str] = None,
    ) -> dict:
        """Get detailed information about a custom agent.

        Args:
            custom_agent_id: The custom agent ID.
            workspace_id: Optional workspace ID.

        Returns:
            Raw API response containing custom agent details.
        """
        params: dict = {
            "CustomAgentId": custom_agent_id,
            "DMSUnit": self._resolve_dms_unit(),
        }
        if workspace_id:
            params["WorkspaceId"] = workspace_id

        return self._call_api(
            action="DescribeCustomAgent",
            version="2025-04-14",
            params=params,
            method="POST",
        )

    @retry_on_error(max_retries=3)
    def create_custom_agent(
        self,
        workspace_id: str,
        name: str,
        description: str = "",
        web_report_config: Optional[str] = None,
        instruction: Optional[str] = None,
        execution_config: Optional[dict] = None,
        client_token: Optional[str] = None,
    ) -> str:
        """Create a custom agent.

        Args:
            workspace_id: Workspace ID to create the agent in.
            name: Agent name (unique within the workspace).
            description: Optional agent description.
            web_report_config: Optional WebReport DSL JSON string.
            instruction: Optional agent instruction text.
            execution_config: Optional execution config dict; defaults to
                skipping plan / SQL / report / human confirmations.
            client_token: Optional idempotency token, auto-generated for retries.

        Returns:
            The new CustomAgentId.

        Raises:
            ApiError: If the API call fails or the response is invalid.
        """
        if execution_config is None:
            execution_config = {
                "SkipPlan": True,
                "SkipSqlConfirm": True,
                "SkipWebReportConfirm": True,
                "SkipAskHuman": True,
            }

        params: dict = {
            "WorkspaceId": workspace_id,
            "DMSUnit": self._resolve_dms_unit(),
            "Name": name,
        }
        if description:
            params["Description"] = description
        if web_report_config is not None:
            params["WebReportConfig"] = web_report_config
        if instruction is not None:
            params["Instruction"] = instruction
        if client_token:
            params["ClientToken"] = client_token
        if self._auth_type == "api_key":
            params["ExecutionConfig"] = execution_config
        else:
            params["ExecutionConfig"] = json.dumps(execution_config)

        response = self._call_api(
            action="CreateCustomAgent",
            version="2025-04-14",
            params=params,
        )
        data = response.get("data") or response.get("Data") or {}
        agent_id = data.get("customAgentId") or data.get("CustomAgentId")
        if not agent_id:
            raise ApiError(
                f"CreateCustomAgent response missing CustomAgentId: {response}",
                code="InvalidResponse",
                request_id=response.get("requestId"),
            )
        return agent_id

    @retry_on_error(max_retries=3)
    def modify_custom_agent(
        self,
        workspace_id: str,
        custom_agent_id: str,
        name: Optional[str] = None,
        description: Optional[str] = None,
        web_report_config: Optional[str] = None,
        instruction: Optional[str] = None,
        execution_config: Optional[dict] = None,
        client_token: Optional[str] = None,
    ) -> dict:
        """Modify an existing custom agent.

        Args:
            workspace_id: Workspace ID the agent belongs to.
            custom_agent_id: The custom agent ID to modify.
            name: Optional new agent name.
            description: Optional new description.
            web_report_config: Optional WebReport DSL JSON string.
            instruction: Optional new instruction text.
            execution_config: Optional execution config dict; defaults to
                skipping plan / SQL / report / human confirmations.
            client_token: Optional idempotency token, auto-generated for retries.

        Returns:
            Raw API response confirming the modification.

        Raises:
            ApiError: If the API call fails.
        """
        if execution_config is None:
            execution_config = {
                "SkipPlan": True,
                "SkipSqlConfirm": True,
                "SkipWebReportConfirm": True,
                "SkipAskHuman": True,
            }

        params: dict = {
            "WorkspaceId": workspace_id,
            "CustomAgentId": custom_agent_id,
            "DMSUnit": self._resolve_dms_unit(),
        }
        if name:
            params["Name"] = name
        if description is not None:
            params["Description"] = description
        if web_report_config is not None:
            params["WebReportConfig"] = web_report_config
        if instruction is not None:
            params["Instruction"] = instruction
        if client_token:
            params["ClientToken"] = client_token
        if self._auth_type == "api_key":
            params["ExecutionConfig"] = execution_config
        else:
            params["ExecutionConfig"] = json.dumps(execution_config)

        return self._call_api(
            action="ModifyCustomAgent",
            version="2025-04-14",
            params=params,
        )

    @retry_on_error(max_retries=3)
    def operate_custom_agent(
        self,
        workspace_id: str,
        custom_agent_id: str,
        operate_type: str = "RELEASE",
        client_token: Optional[str] = None,
    ) -> dict:
        """Operate on a custom agent.

        Args:
            workspace_id: Workspace ID the agent belongs to.
            custom_agent_id: The custom agent ID.
            operate_type: Operation type: RELEASE (publish) / OFFLINE /
                SET_DEFAULT / CANCEL_DEFAULT.
            client_token: Optional idempotency token, auto-generated for retries.

        Returns:
            Raw API response confirming the operation.

        Raises:
            ApiError: If the API call fails.
        """
        params: dict = {
            "WorkspaceId": workspace_id,
            "CustomAgentId": custom_agent_id,
            "OperateType": operate_type,
            "DMSUnit": self._resolve_dms_unit(),
        }
        if client_token:
            params["ClientToken"] = client_token

        return self._call_api(
            action="OperateCustomAgent",
            version="2025-04-14",
            params=params,
        )

    @property
    def config(self) -> DataAgentConfig:
        """Get the client configuration."""
        return self._config


class AsyncDataAgentClient:
    """Asynchronous client for Data Agent API.

    This client provides async/await support for all API operations.
    """

    def __init__(self, config: DataAgentConfig):
        """Initialize the async Data Agent client.

        Args:
            config: Configuration for the client.
        """
        self._config = config
        self._sync_client = DataAgentClient(config)

    async def _run_in_executor(self, func: Callable[..., T], *args, **kwargs) -> T:
        """Run a synchronous function in an executor.

        Args:
            func: The function to run.
            *args: Positional arguments.
            **kwargs: Keyword arguments.

        Returns:
            The result of the function.
        """
        loop = asyncio.get_event_loop()
        return await loop.run_in_executor(
            None,
            functools.partial(func, *args, **kwargs),
        )

    async def delete_data_agent(self) -> dict:
        """Release the caller's runtime asynchronously; running sessions become unavailable."""
        return await self._run_in_executor(self._sync_client.delete_data_agent)

    async def create_session(
        self,
        database_id: Optional[str] = None,
        title: str = "data-agent-session",
        mode: Optional[str] = None,
        enable_search: bool = False,
        file_id: Optional[str] = None,  # 添加文件ID参数
        workspace_id: Optional[str] = None,
        custom_agent_id: Optional[str] = None,
        custom_agent_stage: str = "prod",
        client_token: Optional[str] = None,
        plan_mode: Optional[str] = "force",
    ) -> SessionInfo:
        """Create a new Data Agent session asynchronously.

        Args:
            database_id: Optional database ID to bind to the session.
            title: Session title (required by API).
            mode: Optional session mode: "auto", "lite", "pro", "ultra".
            enable_search: Whether to enable search capability in the session.
            file_id: Optional file ID for file-based analysis session.
            workspace_id: Optional workspace ID to bind to the session.
            custom_agent_id: Optional custom agent ID to use for the session.
            custom_agent_stage: Stage of the custom agent, "prod" (default) or "debug".
            client_token: Optional idempotency token.
            plan_mode: Plan mode: "force" (default, always plan) or "disable" (skip planning).

        Returns:
            SessionInfo with agent_id and session_id.
        """
        return await self._run_in_executor(
            self._sync_client.create_session,
            database_id=database_id,
            title=title,
            mode=mode,
            enable_search=enable_search,
            file_id=file_id,
            workspace_id=workspace_id,
            custom_agent_id=custom_agent_id,
            custom_agent_stage=custom_agent_stage,
            client_token=client_token,
            plan_mode=plan_mode,
        )

    async def describe_session(self, session_id: str, agent_id: str = "", workspace_id: str = "") -> SessionInfo:
        """Get the status of a session asynchronously.

        Args:
            session_id: The session ID.
            agent_id: The agent ID (optional).
            workspace_id: The workspace ID (required for workspace-bound sessions).

        Returns:
            SessionInfo with current status.
        """
        return await self._run_in_executor(
            self._sync_client.describe_session,
            session_id=session_id,
            agent_id=agent_id,
            workspace_id=workspace_id,
        )

    async def send_message(
        self,
        message: str,
        session_id: str = "",
        agent_id: str = "",
        message_type: str = "primary",
        data_source: Optional[DataSource] = None,
        language: str = "CHINESE",
        workspace_id: str = "",
        mode: Optional[str] = None,
        data_sources: Optional[list] = None,
        report_config: Optional[dict] = None,
        permission_config: Optional[dict] = None,
        plan_mode: Optional[str] = None,
        enable_search: Optional[bool] = None,
        custom_agent_id: Optional[str] = None,
        custom_agent_stage: str = "prod",
    ) -> dict:
        """Send a message to the Data Agent asynchronously.

        Args:
            message: The user's natural language query.
            session_id: The session ID. May be omitted for the first message
                of a session — the backend then creates the session and the
                response data carries the new AgentId / SessionId.
            agent_id: Deprecated — no longer needed. The backend resolves the
                agent automatically; kept only for backward compatibility and
                omitted from the wire when empty.
            message_type: Message type (default: "primary"; "report" / "cancel").
            data_source: Optional DataSource with database metadata. Both
                this and ``data_sources`` are session-level (merged
                server-side); the backend recommends ``data_sources``.
            language: Response language (default: "CHINESE").
            workspace_id: The workspace ID (required for workspace-bound sessions).
            mode: Optional per-message mode (auto / lite / pro / ultra).
            data_sources: Optional session-level data sources
                (remote_data_center); merged server-side on repeated passes.
            report_config: Optional report configuration for message_type="report".
            permission_config: Optional session-level permission dict
                ({"DefaultAction": "allow" | "deny"}).
            plan_mode: Optional per-message plan mode (force / disable);
                omitted by default so the session-level setting applies.

        Returns:
            Response from the API.
        """
        return await self._run_in_executor(
            self._sync_client.send_message,
            agent_id=agent_id,
            session_id=session_id,
            message=message,
            message_type=message_type,
            data_source=data_source,
            language=language,
            workspace_id=workspace_id,
            mode=mode,
            data_sources=data_sources,
            report_config=report_config,
            permission_config=permission_config,
            plan_mode=plan_mode,
            enable_search=enable_search,
            custom_agent_id=custom_agent_id,
            custom_agent_stage=custom_agent_stage,
        )

    async def get_chat_content(
        self,
        session_id: str,
        agent_id: str = "",
        checkpoint: Optional[str] = None,
    ) -> dict:
        """Get chat content from the Data Agent asynchronously.

        Args:
            session_id: The session ID.
            agent_id: Deprecated — no longer needed. Omitted from the wire
                when empty; kept only for backward compatibility.
            checkpoint: Optional checkpoint for incremental fetching.

        Returns:
            Response containing content blocks.
        """
        return await self._run_in_executor(
            self._sync_client.get_chat_content,
            agent_id=agent_id,
            session_id=session_id,
            checkpoint=checkpoint,
        )

    async def get_file_upload_signature(
        self,
        filename: str,
        file_size: int,
        workspace_id: Optional[str] = None,
        dms_unit: Optional[str] = None,
    ) -> dict:
        """Get OSS upload signature asynchronously.

        Args:
            filename: Name of the file to upload.
            file_size: Size of the file in bytes.
            workspace_id: Optional workspace ID to upload into.
            dms_unit: Optional DMSUnit override (defaults to auto resolution).

        Returns:
            Response containing upload URL and credentials.
        """
        return await self._run_in_executor(
            self._sync_client.get_file_upload_signature,
            filename=filename,
            file_size=file_size,
            workspace_id=workspace_id,
            dms_unit=dms_unit,
        )

    async def file_upload_callback(
        self,
        file_id: str,
        filename: str,
        upload_location: str,
        file_size: int = None,
        workspace_id: Optional[str] = None,
        dms_unit: Optional[str] = None,
        client_token: Optional[str] = None,
    ) -> dict:
        """Notify file upload completion asynchronously.

        Args:
            file_id: The file ID from upload signature.
            filename: The original filename.
            upload_location: The full OSS path.
            file_size: The file size in bytes (required for API_KEY auth).
            workspace_id: Optional workspace ID the file was uploaded into.
            dms_unit: Optional DMSUnit override (defaults to auto resolution).
            client_token: Optional idempotency token.

        Returns:
            Response confirming the upload.
        """
        return await self._run_in_executor(
            self._sync_client.file_upload_callback,
            file_id=file_id,
            filename=filename,
            upload_location=upload_location,
            file_size=file_size,
            workspace_id=workspace_id,
            dms_unit=dms_unit,
            client_token=client_token,
        )

    async def list_files(self, session_id: str, file_category: Optional[str] = None) -> dict:
        """List files asynchronously.

        Args:
            session_id: The session ID.
            file_category: Optional filter, e.g. "WebReport" for agent-generated reports.

        Returns:
            Response containing file list.
        """
        return await self._run_in_executor(
            self._sync_client.list_files,
            session_id=session_id,
            file_category=file_category,
        )

    async def delete_file(self, file_id: str, client_token: Optional[str] = None) -> dict:
        """Delete a file asynchronously.

        Args:
            file_id: The file ID to delete.
            client_token: Optional idempotency token.

        Returns:
            Response confirming deletion.
        """
        return await self._run_in_executor(
            self._sync_client.delete_file,
            file_id=file_id,
            client_token=client_token,
        )

    async def list_workspaces(
        self,
        workspace_type: str = "MY",
        workspace_name: Optional[str] = None,
        page_number: int = 1,
        page_size: int = 50,
        order: Optional[str] = None,
        order_by: Optional[str] = None,
    ) -> dict:
        """List Data Agent workspaces asynchronously.

        Args:
            workspace_type: Workspace type filter ("MY" or "ALL").
            workspace_name: Optional workspace name filter.
            page_number: Page number (1-based).
            page_size: Number of results per page.
            order: Optional sort order ("ASC" or "DESC").
            order_by: Optional sort field (e.g., "CreateTime").

        Returns:
            Raw API response containing workspace list.
        """
        return await self._run_in_executor(
            self._sync_client.list_workspaces,
            workspace_type=workspace_type,
            workspace_name=workspace_name,
            page_number=page_number,
            page_size=page_size,
            order=order,
            order_by=order_by,
        )

    async def list_custom_agents(
        self,
        workspace_id: Optional[str] = None,
        search_key: Optional[str] = None,
        status: str = "RELEASED",
        page_number: int = 1,
        page_size: int = 20,
    ) -> dict:
        """List custom agents asynchronously.

        Args:
            workspace_id: Optional workspace ID to filter agents.
            search_key: Fuzzy search by name or description.
            status: Filter by status (default: RELEASED).
            page_number: Page number (1-based).
            page_size: Number of results per page.

        Returns:
            Raw API response containing custom agent list.
        """
        return await self._run_in_executor(
            self._sync_client.list_custom_agents,
            workspace_id=workspace_id,
            search_key=search_key,
            status=status,
            page_number=page_number,
            page_size=page_size,
        )

    async def describe_custom_agent(
        self,
        custom_agent_id: str,
        workspace_id: Optional[str] = None,
    ) -> dict:
        """Get detailed information about a custom agent asynchronously.

        Args:
            custom_agent_id: The custom agent ID.
            workspace_id: Optional workspace ID.

        Returns:
            Raw API response containing custom agent details.
        """
        return await self._run_in_executor(
            self._sync_client.describe_custom_agent,
            custom_agent_id=custom_agent_id,
            workspace_id=workspace_id,
        )

    async def create_custom_agent(
        self,
        workspace_id: str,
        name: str,
        description: str = "",
        web_report_config: Optional[str] = None,
        instruction: Optional[str] = None,
        execution_config: Optional[dict] = None,
        client_token: Optional[str] = None,
    ) -> str:
        """Create a custom agent asynchronously.

        Args:
            workspace_id: Workspace ID to create the agent in.
            name: Agent name (unique within the workspace).
            description: Optional agent description.
            web_report_config: Optional WebReport DSL JSON string.
            instruction: Optional agent instruction text.
            execution_config: Optional execution config dict (defaults to skip-all).
            client_token: Optional idempotency token.

        Returns:
            The new CustomAgentId.
        """
        return await self._run_in_executor(
            self._sync_client.create_custom_agent,
            workspace_id=workspace_id,
            name=name,
            description=description,
            web_report_config=web_report_config,
            instruction=instruction,
            execution_config=execution_config,
            client_token=client_token,
        )

    async def modify_custom_agent(
        self,
        workspace_id: str,
        custom_agent_id: str,
        name: Optional[str] = None,
        description: Optional[str] = None,
        web_report_config: Optional[str] = None,
        instruction: Optional[str] = None,
        execution_config: Optional[dict] = None,
        client_token: Optional[str] = None,
    ) -> dict:
        """Modify an existing custom agent asynchronously.

        Args:
            workspace_id: Workspace ID the agent belongs to.
            custom_agent_id: The custom agent ID to modify.
            name: Optional new agent name.
            description: Optional new description.
            web_report_config: Optional WebReport DSL JSON string.
            instruction: Optional new instruction text.
            execution_config: Optional execution config dict (defaults to skip-all).
            client_token: Optional idempotency token.

        Returns:
            Raw API response confirming the modification.
        """
        return await self._run_in_executor(
            self._sync_client.modify_custom_agent,
            workspace_id=workspace_id,
            custom_agent_id=custom_agent_id,
            name=name,
            description=description,
            web_report_config=web_report_config,
            instruction=instruction,
            execution_config=execution_config,
            client_token=client_token,
        )

    async def operate_custom_agent(
        self,
        workspace_id: str,
        custom_agent_id: str,
        operate_type: str = "RELEASE",
        client_token: Optional[str] = None,
    ) -> dict:
        """Operate on a custom agent asynchronously.

        Args:
            workspace_id: Workspace ID the agent belongs to.
            custom_agent_id: The custom agent ID.
            operate_type: Operation type: RELEASE / OFFLINE / SET_DEFAULT / CANCEL_DEFAULT.
            client_token: Optional idempotency token.

        Returns:
            Raw API response confirming the operation.
        """
        return await self._run_in_executor(
            self._sync_client.operate_custom_agent,
            workspace_id=workspace_id,
            custom_agent_id=custom_agent_id,
            operate_type=operate_type,
            client_token=client_token,
        )

    @property
    def config(self) -> DataAgentConfig:
        """Get the client configuration."""
        return self._config
