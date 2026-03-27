"""Tests for Data Agent file manager module."""

import os
import pytest
import tempfile
from pathlib import Path
from unittest.mock import Mock, patch, MagicMock

from data_agent.file_manager import FileManager, AsyncFileManager, SUPPORTED_FILE_TYPES
from data_agent.client import DataAgentClient
from data_agent.models import FileInfo
from data_agent.exceptions import FileUploadError, FileDownloadError


class TestFileManager:
    """Test cases for FileManager."""

    @pytest.fixture
    def mock_client(self, mock_config):
        """Create a mock client."""
        client = Mock(spec=DataAgentClient)
        client.config = mock_config
        return client

    @pytest.fixture
    def file_manager(self, mock_client):
        """Create a file manager with mock client."""
        return FileManager(mock_client)

    @pytest.fixture
    def temp_csv_file(self):
        """Create a temporary CSV file."""
        with tempfile.NamedTemporaryFile(mode="w", suffix=".csv", delete=False) as f:
            f.write("col1,col2,col3\n")
            f.write("1,2,3\n")
            f.write("4,5,6\n")
            temp_path = f.name

        yield temp_path

        # Cleanup
        if os.path.exists(temp_path):
            os.unlink(temp_path)

    @pytest.fixture
    def temp_xlsx_file(self):
        """Create a temporary XLSX file."""
        with tempfile.NamedTemporaryFile(suffix=".xlsx", delete=False) as f:
            f.write(b"fake xlsx content")
            temp_path = f.name

        yield temp_path

        if os.path.exists(temp_path):
            os.unlink(temp_path)

    def test_upload_file_success(self, file_manager, mock_client, temp_csv_file):
        """Test successful file upload."""
        mock_client.get_file_upload_signature.return_value = {
            "Data": {
                "UploadHost": "https://oss.example.com",
                "UploadDir": "upload/dir",
                "Policy": "policy123",
                "OssSignature": "sig123",
                "OssDate": "20260301",
                "OssSecurityToken": "token123",
                "OssCredential": "cred123",
            }
        }
        mock_client.file_upload_callback.return_value = {
            "Data": {"FileId": "file-123"}
        }

        with patch("requests.post") as mock_post:
            mock_post.return_value = MagicMock(status_code=200)
            mock_post.return_value.raise_for_status = Mock()

            result = file_manager.upload_file(temp_csv_file)

        assert result.file_id == "file-123"
        assert result.file_type == "csv"
        # Extract filename from temp_csv_file
        filename = os.path.basename(temp_csv_file)
        # file_upload_callback now receives file_size as 4th argument
        file_size = os.path.getsize(temp_csv_file)
        mock_client.file_upload_callback.assert_called_once_with("upload/dir", filename, f"upload/dir/{filename}", file_size)

    def test_upload_file_not_found(self, file_manager):
        """Test upload with non-existent file."""
        with pytest.raises(FileUploadError) as exc_info:
            file_manager.upload_file("/non/existent/file.csv")

        assert "not found" in str(exc_info.value).lower()

    def test_upload_file_unsupported_type(self, file_manager):
        """Test upload with unsupported file type."""
        with tempfile.NamedTemporaryFile(suffix=".xyz", delete=False) as f:
            temp_path = f.name

        try:
            with pytest.raises(FileUploadError) as exc_info:
                file_manager.upload_file(temp_path)

            assert "unsupported" in str(exc_info.value).lower()
        finally:
            os.unlink(temp_path)

    def test_upload_file_signature_failure(self, file_manager, mock_client, temp_csv_file):
        """Test upload when signature request fails."""
        mock_client.get_file_upload_signature.side_effect = Exception("API error")

        with pytest.raises(FileUploadError) as exc_info:
            file_manager.upload_file(temp_csv_file)

        assert "signature" in str(exc_info.value).lower()

    def test_upload_file_oss_failure(self, file_manager, mock_client, temp_csv_file):
        """Test upload when OSS upload fails."""
        import requests

        mock_client.get_file_upload_signature.return_value = {
            "Data": {
                "UploadHost": "https://oss.example.com",
                "UploadDir": "upload/dir",
                "Policy": "policy123",
                "OssSignature": "sig123",
                "OssDate": "20260301",
                "OssSecurityToken": "token123",
                "OssCredential": "cred123",
            }
        }

        with patch("requests.post") as mock_post:
            mock_post.side_effect = requests.RequestException("OSS error")

            with pytest.raises(FileUploadError) as exc_info:
                file_manager.upload_file(temp_csv_file)

        assert "oss" in str(exc_info.value).lower()

    def test_list_files(self, file_manager, mock_client):
        """Test listing files."""
        mock_client.list_files.return_value = {
            "Files": [
                {"FileId": "f1", "FileName": "report.csv", "FileType": "csv", "FileSize": 1024},
                {"FileId": "f2", "FileName": "chart.png", "FileType": "png", "FileSize": 2048},
            ]
        }

        files = file_manager.list_files("session-123")

        assert len(files) == 2
        assert files[0].file_id == "f1"
        assert files[0].filename == "report.csv"

    def test_list_files_empty(self, file_manager, mock_client):
        """Test listing files when none exist."""
        mock_client.list_files.return_value = {"Files": []}

        files = file_manager.list_files("session-123")

        assert len(files) == 0

    def test_delete_file_success(self, file_manager, mock_client):
        """Test successful file deletion."""
        mock_client.delete_file.return_value = {}

        result = file_manager.delete_file("file-123")

        assert result is True
        mock_client.delete_file.assert_called_once_with("file-123")

    def test_delete_file_failure(self, file_manager, mock_client):
        """Test file deletion failure."""
        mock_client.delete_file.side_effect = Exception("Delete failed")

        result = file_manager.delete_file("file-123")

        assert result is False

    def test_get_file_type_csv(self, file_manager):
        """Test getting file type for CSV."""
        result = file_manager.get_file_type("data.csv")
        assert result == "csv"

    def test_get_file_type_xlsx(self, file_manager):
        """Test getting file type for XLSX."""
        result = file_manager.get_file_type("report.xlsx")
        assert result == "xlsx"

    def test_get_file_type_unsupported(self, file_manager):
        """Test getting file type for unsupported format."""
        result = file_manager.get_file_type("image.png")
        assert result is None

    def test_is_supported_file_true(self, file_manager):
        """Test supported file check."""
        assert file_manager.is_supported_file("data.csv") is True
        assert file_manager.is_supported_file("report.xlsx") is True
        assert file_manager.is_supported_file("config.json") is True

    def test_is_supported_file_false(self, file_manager):
        """Test unsupported file check."""
        assert file_manager.is_supported_file("image.png") is False
        assert file_manager.is_supported_file("doc.pdf") is False


class TestSupportedFileTypes:
    """Test cases for supported file types constant."""

    def test_csv_supported(self):
        """Test CSV is supported."""
        assert ".csv" in SUPPORTED_FILE_TYPES

    def test_xlsx_supported(self):
        """Test XLSX is supported."""
        assert ".xlsx" in SUPPORTED_FILE_TYPES

    def test_xls_supported(self):
        """Test XLS is supported."""
        assert ".xls" in SUPPORTED_FILE_TYPES

    def test_json_supported(self):
        """Test JSON is supported."""
        assert ".json" in SUPPORTED_FILE_TYPES

    def test_txt_supported(self):
        """Test TXT is supported."""
        assert ".txt" in SUPPORTED_FILE_TYPES

    def test_mime_types_correct(self):
        """Test MIME types are correctly defined."""
        assert SUPPORTED_FILE_TYPES[".csv"] == "text/csv"
        assert "spreadsheet" in SUPPORTED_FILE_TYPES[".xlsx"]


class TestAsyncFileManager:
    """Test cases for AsyncFileManager."""

    @pytest.fixture
    def mock_async_client(self, mock_config):
        """Create a mock async client."""
        from data_agent.client import AsyncDataAgentClient
        client = Mock(spec=AsyncDataAgentClient)
        client.config = mock_config
        return client

    @pytest.fixture
    def async_file_manager(self, mock_async_client):
        """Create an async file manager."""
        return AsyncFileManager(mock_async_client)

    @pytest.mark.asyncio
    async def test_list_files_async(self, async_file_manager, mock_async_client):
        """Test async file listing."""
        mock_async_client.list_files.return_value = {
            "Files": [
                {"FileId": "f1", "FileName": "data.csv", "FileType": "csv", "FileSize": 100}
            ]
        }

        files = await async_file_manager.list_files("session-123")

        assert len(files) == 1
        assert files[0].file_id == "f1"

    @pytest.mark.asyncio
    async def test_delete_file_async(self, async_file_manager, mock_async_client):
        """Test async file deletion."""
        mock_async_client.delete_file.return_value = {}

        result = await async_file_manager.delete_file("file-123")

        assert result is True
