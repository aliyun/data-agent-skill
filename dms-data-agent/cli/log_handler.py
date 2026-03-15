"""Structured logging handler for Data Agent.

Provides simultaneous logging to plain text (progress.log/process.log) and structured JSONL format (progress.jsonl/process.jsonl).
"""

import json
from pathlib import Path
from datetime import datetime
from typing import TextIO, Optional, Dict, Any


class StructuredLogHandler:
    """Handles simultaneous logging to plain text and structured JSONL formats."""

    def __init__(self, session_dir: Path, log_prefix: str = "progress"):
        """Initialize the structured log handler.

        Args:
            session_dir: Directory for the session where logs will be stored.
            log_prefix: Prefix for log files (e.g., "progress" for progress.log, or "process" for process.log)
        """
        self.session_dir = session_dir
        self.log_prefix = log_prefix
        self.progress_log_path = session_dir / f"{log_prefix}.log"
        self.progress_jsonl_path = session_dir / f"{log_prefix}.jsonl"

        # Open both files for writing
        self.progress_log_file: Optional[TextIO] = None
        self.progress_jsonl_file: Optional[TextIO] = None

    def __enter__(self):
        """Enter context manager, opening both log files."""
        # Ensure directory exists
        self.session_dir.mkdir(parents=True, exist_ok=True)

        # Open both log files
        self.progress_log_file = open(self.progress_log_path, "w", encoding="utf-8")
        self.progress_jsonl_file = open(self.progress_jsonl_path, "w", encoding="utf-8")

        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        """Exit context manager, closing both log files."""
        if self.progress_log_file:
            self.progress_log_file.close()
        if self.progress_jsonl_file:
            self.progress_jsonl_file.close()

    def write_log(self, text: str):
        """Write text to the plain log file (e.g., progress.log or process.log).

        Args:
            text: Text to write to the plain text log.
        """
        if self.progress_log_file:
            self.progress_log_file.write(text)
            self.progress_log_file.flush()  # Ensure immediate write

    def write_jsonl(self, data: Dict[str, Any]):
        """Write structured data to the JSONL file (e.g., progress.jsonl or process.jsonl).

        Args:
            data: Dictionary containing structured log data.
        """
        if self.progress_jsonl_file:
            # Add timestamp if not present
            if 'timestamp' not in data:
                data['timestamp'] = datetime.now().isoformat()

            json_line = json.dumps(data, ensure_ascii=False)
            self.progress_jsonl_file.write(json_line + '\n')
            self.progress_jsonl_file.flush()  # Ensure immediate write

    def write_both(self, text: str, data: Optional[Dict[str, Any]] = None):
        """Write to both plain text and JSONL logs.

        Args:
            text: Text to write to the plain text log.
            data: Optional structured data to write to JSONL log.
        """
        self.write_log(text)

        if data:
            self.write_jsonl(data)
        else:
            # If no structured data provided, at least log the text
            # as a basic structured entry
            self.write_jsonl({
                'type': 'log',
                'text': text.strip() if text.strip() else text
            })

    def close(self):
        """Close all file handles (alias for __exit__ compatibility)."""
        if self.progress_log_file:
            self.progress_log_file.close()
            self.progress_log_file = None
        if self.progress_jsonl_file:
            self.progress_jsonl_file.close()
            self.progress_jsonl_file = None