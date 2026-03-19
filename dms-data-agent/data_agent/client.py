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
from typing import Optional, Any, Callable, TypeVar

from alibabacloud_tea_openapi import models as open_api_models
from alibabacloud_tea_openapi.client import Client as OpenApiClient
from alibabacloud_tea_util import models as util_models
from alibabacloud_openapi_util.client import Client as OpenApiUtilClient
from Tea.exceptions import TeaException

from data_agent.config import DataAgentConfig
from data_agent.models import SessionInfo, SessionStatus, DataSource
from data_agent.api_adapter import APIAdapter
from data_agent.exceptions import (
    ApiError,
    AuthenticationError,
    SessionCreationError,
    ConfigurationError,
)


T = TypeVar("T")


def retry_on_error(max_retries: int = 3, retry_codes: tuple = ("Throttling", "ServiceUnavailable")):
    """Decorator to retry API calls on transient errors with exponential backoff."""

    def decorator(func: Callable[..., T]) -> Callable[..., T]:
        @functools.wraps(func)
        def wrapper(self, *args, **kwargs) -> T:
            last_exception = None
            for attempt in range(max_retries + 1):
                try:
                    return func(self, *args, **kwargs)
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

    def __init__(self, config: DataAgentConfig):
        """Initialize the Data Agent client.

        Args:
            config: Configuration for the client.
        """
        self._config = config
        self._sdk_client: Optional[OpenApiClient] = None
        self._initialize_client()

    def _initialize_client(self) -> None:
        """Initialize the underlying SDK client."""
        sdk_config = open_api_models.Config(
            access_key_id=self._config.access_key_id,
            access_key_secret=self._config.access_key_secret,
        )
        sdk_config.endpoint = self._config.endpoint
        # Set STS token if provided
        if self._config.security_token:
            sdk_config.security_token = self._config.security_token
        self._sdk_client = OpenApiClient(sdk_config)

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
        if os.getenv("DATA_AGENT_DEBUG_API", "").lower() in ('true', '1', 'yes'):
            import pprint
            print(f"[DEBUG] API Call: {action}")
            print(f"[DEBUG] Method: {method}")
            print(f"[DEBUG] Params: {pprint.pformat(prepared_params)}")
            if prepared_body:
                print(f"[DEBUG] Body: {pprint.pformat(prepared_body)}")

        try:
            response = self._sdk_client.call_api(api_params, request, runtime)

            # Process response to convert keys to camelCase
            processed_response = APIAdapter.process_response(response.get("body", {}), api_action=action)

            # Add debug logging for response if enabled
            if os.getenv("DATA_AGENT_DEBUG_API", "").lower() in ('true', '1', 'yes'):
                print(f"[DEBUG] Response for {action}: {pprint.pformat(processed_response)}")

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

    @retry_on_error(max_retries=3)
    def create_session(
        self,
        database_id: Optional[str] = None,
        title: str = "data-agent-session",
        mode: Optional[str] = None,
        enable_search: bool = False,
        file_id: Optional[str] = None,  # 添加文件ID参数
    ) -> SessionInfo:
        """Create a new Data Agent session.

        Args:
            database_id: Optional database ID to bind to the session.
            title: Session title (required by API).
            mode: Optional session mode, such as "ASK_DATA", "ANALYSIS", "INSIGHT".
            enable_search: Whether to enable search capability in the session.
            file_id: Optional file ID for file-based analysis session.

        Returns:
            SessionInfo with agent_id and session_id.

        Raises:
            SessionCreationError: If session creation fails.
        """
        params = {
            "Title": title,
            "DMSUnit": self._config.region,
        }
        if database_id:
            params["DatabaseId"] = database_id
        if mode:
            params["Mode"] = mode
        if file_id:
            # 当指定了文件ID时，会话将是基于文件的分析
            params["FileId"] = file_id

        # Ensure session configuration (language/mode/search) is persisted on server
        session_config = {"Language": "CHINESE", "EnableSearch": enable_search}
        if mode:
            session_config["Mode"] = mode
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
            )
        except ApiError as e:
            raise SessionCreationError(f"Failed to create session: {e.message}", request_id=e.request_id)

    @retry_on_error(max_retries=3)
    def describe_session(self, session_id: str, agent_id: str = "") -> SessionInfo:
        """Get the status of a session.

        Args:
            session_id: The session ID.
            agent_id: The agent ID (optional, but used to construct the request properly).

        Returns:
            SessionInfo with current status.
        """
        params = {
            "SessionId": session_id,
            "DMSUnit": self._config.region,
        }
        # Only include AgentId in the request if it's provided
        # According to API spec, DescribeDataAgentSession doesn't require AgentId
        if agent_id:
            params["AgentId"] = agent_id

        response = self._call_api(
            action="DescribeDataAgentSession",
            version="2025-04-14",
            params=params,
        )

        request_id = response.get("requestId", "")  # Changed from "RequestId" to "requestId"
        data = response.get("data", response)  # Changed from "Data" to "data"
        status_str = data.get("sessionStatus", data.get("status", "CREATING"))  # Changed from "SessionStatus"/"Status" to "sessionStatus"/"status"
        try:
            status = SessionStatus(status_str)
        except ValueError:
            status = SessionStatus.CREATING

        # Capture the real AgentId from response if available
        # The agent_id in the response should take precedence over the one passed in
        real_agent_id = data.get("agentId") or data.get("AgentId") or agent_id  # Try both transformed and original

        return SessionInfo(
            agent_id=real_agent_id,
            session_id=session_id,
            status=status,
            database_id=data.get("databaseId"),  # Changed from "DatabaseId" to "databaseId"
            request_id=request_id,
        )

    @retry_on_error(max_retries=3)
    def send_message(
        self,
        agent_id: str,
        session_id: str,
        message: str,
        message_type: str = "primary",
        data_source: Optional[DataSource] = None,
        language: str = "CHINESE",
    ) -> dict:
        """Send a message to the Data Agent.

        Args:
            agent_id: The agent ID.
            session_id: The session ID.
            message: The user's natural language query.
            message_type: Message type (default: "primary").
            data_source: Optional DataSource with database metadata.
            language: Response language (default: "CHINESE").

        Returns:
            Response from the API.
        """
        # Query parameters for RPC style API
        params = {
            "AgentId": agent_id,
            "SessionId": session_id,
            "Message": message,
            "MessageType": message_type,
            "DMSUnit": self._config.region,
        }

        # SessionConfig as JSON string
        session_config = {"Language": language}
        params["SessionConfig"] = json.dumps(session_config)

        # DataSource as JSON string (like official SDK)
        if data_source:
            params["DataSource"] = json.dumps(data_source.to_api_dict())

        return self._call_api(
            action="SendChatMessage",
            version="2025-04-14",
            params=params,
        )

    @retry_on_error(max_retries=3)
    def get_chat_content(
        self,
        agent_id: str,
        session_id: str,
        checkpoint: Optional[str] = None,
    ) -> dict:
        """Get chat content from the Data Agent.

        Args:
            agent_id: The agent ID.
            session_id: The session ID.
            checkpoint: Optional checkpoint for incremental fetching.

        Returns:
            Response containing content blocks.
        """
        params = {
            "AgentId": agent_id,
            "SessionId": session_id,
        }
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
    ) -> dict:
        """Get OSS upload signature for file upload.

        Args:
            filename: Name of the file to upload.
            file_size: Size of the file in bytes.

        Returns:
            Response containing upload URL and credentials.
        """
        params = {
            "FileName": filename,
            "FileSize": file_size,
        }

        return self._call_api(
            action="DescribeFileUploadSignature",
            version="2025-04-14",
            params=params,
        )

    @retry_on_error(max_retries=3)
    def file_upload_callback(self, file_id: str, filename: str, upload_location: str) -> dict:
        """Notify the service that file upload is complete.

        Args:
            file_id: The file ID from upload signature.
            filename: The original filename.
            upload_location: The full OSS path (UploadHost/UploadDir/Filename).

        Returns:
            Response confirming the upload.
        """
        params = {
            "FileId": file_id,
            "Filename": filename,
            "UploadLocation": upload_location,
        }

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
    def list_databases(
        self,
        search_key: Optional[str] = None,
        page_number: int = 1,
        page_size: int = 50,
    ) -> dict:
        """List databases registered in DMS Data Center.

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
    def list_tables(
        self,
        instance_name: str,
        database_name: str,
        page_number: int = 1,
        page_size: int = 200,
    ) -> dict:
        """List tables inside a DMS Data Center database.

        Args:
            instance_name: The InstanceName returned by list_databases.
            database_name: The DatabaseName returned by list_databases.
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
    def delete_file(self, file_id: str) -> dict:
        """Delete an uploaded file.

        Args:
            file_id: The file ID to delete.

        Returns:
            Response confirming deletion.
        """
        params = {
            "FileId": file_id,
        }

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

        Returns:
            API response containing the import result.

        Raises:
            ApiError: If the API call fails.
        """
        region = region_id or self._config.region

        # Convert table_name_list to JSON string for RPC API
        import json
        table_list_json = json.dumps(table_name_list, ensure_ascii=False)

        params = {
            "DMSUnit": region,
            "RegionId": region,
            "ImportType": "DMS",
            "InstanceName": instance_name,
            "DmsInstanceId": dms_instance_id,
            "DbType": db_type,
            "DatabaseName": database_name,
            "DmsDbId": dms_db_id,
            "TableNameList": table_list_json,
        }

        return self._call_api(
            action="AddDataCenterTable",
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

    async def create_session(
        self,
        database_id: Optional[str] = None,
        title: str = "data-agent-session",
        mode: Optional[str] = None,
        enable_search: bool = False,
        file_id: Optional[str] = None,  # 添加文件ID参数
    ) -> SessionInfo:
        """Create a new Data Agent session asynchronously.

        Args:
            database_id: Optional database ID to bind to the session.
            title: Session title (required by API).
            mode: Optional session mode, such as "ASK_DATA", "ANALYSIS", "INSIGHT".
            enable_search: Whether to enable search capability in the session.
            file_id: Optional file ID for file-based analysis session.

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
        )

    async def describe_session(self, session_id: str, agent_id: str = "") -> SessionInfo:
        """Get the status of a session asynchronously.

        Args:
            session_id: The session ID.
            agent_id: The agent ID (optional).

        Returns:
            SessionInfo with current status.
        """
        return await self._run_in_executor(
            self._sync_client.describe_session,
            session_id=session_id,
            agent_id=agent_id,
        )

    async def send_message(
        self,
        agent_id: str,
        session_id: str,
        message: str,
        message_type: str = "primary",
        data_source: Optional[DataSource] = None,
        language: str = "CHINESE",
    ) -> dict:
        """Send a message to the Data Agent asynchronously.

        Args:
            agent_id: The agent ID.
            session_id: The session ID.
            message: The user's natural language query.
            message_type: Message type (default: "primary").
            data_source: Optional DataSource with database metadata.
            language: Response language (default: "CHINESE").

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
        )

    async def get_chat_content(
        self,
        agent_id: str,
        session_id: str,
        checkpoint: Optional[str] = None,
    ) -> dict:
        """Get chat content from the Data Agent asynchronously.

        Args:
            agent_id: The agent ID.
            session_id: The session ID.
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
    ) -> dict:
        """Get OSS upload signature asynchronously.

        Args:
            filename: Name of the file to upload.
            file_size: Size of the file in bytes.

        Returns:
            Response containing upload URL and credentials.
        """
        return await self._run_in_executor(
            self._sync_client.get_file_upload_signature,
            filename=filename,
            file_size=file_size,
        )

    async def file_upload_callback(self, file_id: str) -> dict:
        """Notify file upload completion asynchronously.

        Args:
            file_id: The file ID from upload signature.

        Returns:
            Response confirming the upload.
        """
        return await self._run_in_executor(
            self._sync_client.file_upload_callback,
            file_id=file_id,
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

    async def delete_file(self, file_id: str) -> dict:
        """Delete a file asynchronously.

        Args:
            file_id: The file ID to delete.

        Returns:
            Response confirming deletion.
        """
        return await self._run_in_executor(
            self._sync_client.delete_file,
            file_id=file_id,
        )

    @property
    def config(self) -> DataAgentConfig:
        """Get the client configuration."""
        return self._config
