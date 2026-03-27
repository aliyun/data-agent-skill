"""Tests for data_agent_cli -- file subcommand and CLI argument parsing.

Author: Tinker
Created: 2026-03-03
"""

import json
import os
import sys
import tempfile
import pytest
from pathlib import Path
from unittest.mock import Mock, MagicMock, patch

# Ensure src/ is importable
_SRC = str(Path(__file__).resolve().parents[1] / "src")
if _SRC not in sys.path:
    sys.path.insert(0, _SRC)

from data_agent_cli import (
    build_parser,
    cmd_file,
    cmd_db,
    _build_data_source,
    _fmt_jupyter_cell,
    _fmt_insights,
    _format_data_event,
    _fmt_plan_progress,
    _fmt_status_change,
    _fmt_output_conclusion,
    _fmt_recommended_questions,
    StreamState,
    _print_event,
    _finalize_stream,
)
from data_agent.models import SessionInfo, SessionStatus, FileInfo
from datetime import datetime


# ---------------------------------------------
# Fixtures
# ---------------------------------------------

@pytest.fixture
def parser():
    return build_parser()


@pytest.fixture
def temp_csv(tmp_path):
    """Temporary CSV file for upload tests."""
    f = tmp_path / "data.csv"
    f.write_text("col1,col2\n1,2\n3,4\n")
    return str(f)


@pytest.fixture
def mock_session():
    return SessionInfo(
        agent_id="agent-1",
        session_id="session-abc",
        status=SessionStatus.RUNNING,
        database_id=None,
        created_at=datetime.now(),
        last_used_at=datetime.now(),
    )


@pytest.fixture
def mock_file_info():
    return FileInfo(
        file_id="file-001",
        filename="data.csv",
        file_type="csv",
        size=64,
    )


# ---------------------------------------------
# Argument Parser Tests
# ---------------------------------------------

class TestBuildParser:
    """Tests for CLI argument parser."""

    def test_parser_requires_subcommand(self, parser):
        with pytest.raises(SystemExit):
            parser.parse_args([])

    def test_db_subcommand_parsed(self, parser):
        args = parser.parse_args([
            "db",
            "--dms-instance-id", "123",
            "--dms-db-id", "456",
            "--instance-name", "rm-test",
            "--db-name", "testdb",
            "--query", "show tables",
        ])
        assert args.command == "db"
        assert args.dms_instance_id == 123
        assert args.dms_db_id == 456
        assert args.instance_name == "rm-test"
        assert args.db_name == "testdb"
        assert args.query == "show tables"
        assert args.session_mode == "ASK_DATA"  # default

    def test_db_session_mode_analysis(self, parser):
        args = parser.parse_args([
            "db",
            "--dms-instance-id", "1", "--dms-db-id", "2",
            "--instance-name", "rm-x", "--db-name", "db1",
            "--session-mode", "ANALYSIS",
        ])
        assert args.session_mode == "ANALYSIS"

    def test_file_subcommand_parsed(self, parser):
        args = parser.parse_args(["file", "/path/to/data.csv"])
        assert args.command == "file"
        assert args.file_path == "/path/to/data.csv"
        assert args.session_mode == "ANALYSIS"  # default

    def test_file_with_query(self, parser):
        args = parser.parse_args(["file", "/data.csv", "-q", "analyze trends"])
        assert args.query == "analyze trends"

    def test_file_list_generated_files_flag(self, parser):
        args = parser.parse_args(["file", "/data.csv", "--list-generated-files"])
        assert args.list_generated_files is True


# ---------------------------------------------
# _build_data_source Tests
# ---------------------------------------------

class TestBuildDataSource:
    """Tests for DataSource construction from CLI args."""

    def _make_args(self, **kwargs):
        defaults = dict(
            dms_instance_id=111,
            dms_db_id=222,
            instance_name="rm-test",
            db_name="chinook",
            tables=None,
            table_ids=None,
            engine="mysql",
            region="cn-hangzhou",
        )
        defaults.update(kwargs)
        ns = MagicMock()
        for k, v in defaults.items():
            setattr(ns, k, v)
        return ns

    def test_basic_data_source(self):
        args = self._make_args()
        ds = _build_data_source(args)
        assert ds.dms_instance_id == 111
        assert ds.dms_database_id == 222
        assert ds.instance_name == "rm-test"
        assert ds.db_name == "chinook"
        assert ds.tables == []
        assert ds.table_ids == []

    def test_tables_parsed(self):
        args = self._make_args(tables="album,artist,track")
        ds = _build_data_source(args)
        assert ds.tables == ["album", "artist", "track"]

    def test_table_ids_parsed(self):
        args = self._make_args(table_ids="10,20,30")
        ds = _build_data_source(args)
        assert ds.table_ids == ["10", "20", "30"]

    def test_engine_and_region(self):
        args = self._make_args(engine="postgresql", region="cn-beijing")
        ds = _build_data_source(args)
        assert ds.engine == "postgresql"
        assert ds.region_id == "cn-beijing"


# ---------------------------------------------
# cmd_file Tests
# ---------------------------------------------

class TestCmdFile:
    """Tests for the file subcommand handler."""

    def _make_args(self, file_path, query=None,
                   session_mode="ANALYSIS", list_generated_files=False):
        ns = MagicMock()
        ns.file_path = file_path
        ns.file_id = None
        ns.query = query
        ns.session_mode = session_mode
        ns.list_generated_files = list_generated_files
        ns.enable_search = False
        ns.async_run = False
        ns.output = "summary"
        return ns

    def test_file_not_found_exits(self, capsys):
        args = self._make_args("/nonexistent/data.csv")
        with pytest.raises(SystemExit) as exc:
            cmd_file(args)
        assert exc.value.code == 1
        captured = capsys.readouterr()
        assert "not found" in captured.err

    def test_unsupported_file_type_exits(self, tmp_path, capsys):
        bad = tmp_path / "data.pdf"
        bad.write_bytes(b"fake")
        args = self._make_args(str(bad))
        with patch("cli.cmd_file.DataAgentConfig.from_env"):
            with pytest.raises(SystemExit) as exc:
                cmd_file(args)
        assert exc.value.code == 1
        captured = capsys.readouterr()
        assert "Unsupported" in captured.err

    def test_single_query_mode(self, temp_csv, mock_session, mock_file_info, capsys):
        """Test file subcommand with --query flag."""
        args = self._make_args(temp_csv, query="What is the column count?")

        with patch("cli.cmd_file.DataAgentConfig.from_env") as mock_cfg, \
             patch("cli.cmd_file.DataAgentClient") as mock_client_cls, \
             patch("cli.cmd_file.SessionManager") as mock_sm_cls, \
             patch("cli.cmd_file.MessageHandler") as mock_mh_cls, \
             patch("cli.cmd_file.FileManager") as mock_fm_cls:

            mock_cfg.return_value = MagicMock()
            mock_fm = mock_fm_cls.return_value
            mock_fm.is_supported_file.return_value = True
            mock_fm.upload_file.return_value = mock_file_info

            mock_sm = mock_sm_cls.return_value
            mock_sm.create_or_reuse.return_value = mock_session

            mock_mh = mock_mh_cls.return_value
            mock_mh.stream_events.return_value = iter([])

            cmd_file(args)

        mock_fm.upload_file.assert_called_once_with(temp_csv)
        mock_sm.create_or_reuse.assert_called_once_with(mode="ANALYSIS", enable_search=False, file_id='file-001')
        mock_mh.stream_events.assert_called_once()
        captured = capsys.readouterr()
        assert "File uploaded" in captured.out
        assert "Session ready" in captured.out

    def test_default_preset_queries(self, temp_csv, mock_session, mock_file_info, capsys):
        """Without --query, runs three preset questions."""
        args = self._make_args(temp_csv)

        with patch("cli.cmd_file.DataAgentConfig.from_env"), \
             patch("cli.cmd_file.DataAgentClient"), \
             patch("cli.cmd_file.SessionManager") as mock_sm_cls, \
             patch("cli.cmd_file.MessageHandler") as mock_mh_cls, \
             patch("cli.cmd_file.FileManager") as mock_fm_cls:

            mock_fm = mock_fm_cls.return_value
            mock_fm.is_supported_file.return_value = True
            mock_fm.upload_file.return_value = mock_file_info

            mock_sm_cls.return_value.create_or_reuse.return_value = mock_session

            mock_mh = mock_mh_cls.return_value
            mock_mh.stream_events.return_value = iter([])

            cmd_file(args)

        # stream_events called once per preset query (3 questions)
        assert mock_mh.stream_events.call_count == 3

    def test_list_generated_files_after_queries(self, temp_csv, mock_session, mock_file_info, capsys):
        """--list-generated-files triggers file listing after analysis."""
        args = self._make_args(temp_csv, list_generated_files=True)

        with patch("cli.cmd_file.DataAgentConfig.from_env"), \
             patch("cli.cmd_file.DataAgentClient"), \
             patch("cli.cmd_file.SessionManager") as mock_sm_cls, \
             patch("cli.cmd_file.MessageHandler") as mock_mh_cls, \
             patch("cli.cmd_file.FileManager") as mock_fm_cls:

            mock_fm = mock_fm_cls.return_value
            mock_fm.is_supported_file.return_value = True
            mock_fm.upload_file.return_value = mock_file_info
            mock_fm.list_files.return_value = [
                FileInfo(file_id="gen-1", filename="report.html",
                         file_type="html", size=512,
                         download_url="https://example.com/report.html")
            ]

            mock_sm_cls.return_value.create_or_reuse.return_value = mock_session
            mock_mh_cls.return_value.stream_events.return_value = iter([])

            cmd_file(args)

        mock_fm.list_files.assert_called_once_with(mock_session.session_id)
        captured = capsys.readouterr()
        assert "report.html" in captured.out

    def test_upload_failure_exits(self, temp_csv, capsys):
        """Upload exception causes sys.exit(1)."""
        args = self._make_args(temp_csv)

        with patch("cli.cmd_file.DataAgentConfig.from_env"), \
             patch("cli.cmd_file.DataAgentClient"), \
             patch("cli.cmd_file.SessionManager"), \
             patch("cli.cmd_file.MessageHandler"), \
             patch("cli.cmd_file.FileManager") as mock_fm_cls:

            mock_fm = mock_fm_cls.return_value
            mock_fm.is_supported_file.return_value = True
            mock_fm.upload_file.side_effect = Exception("OSS failure")

            with pytest.raises(SystemExit) as exc:
                cmd_file(args)

        assert exc.value.code == 1
        captured = capsys.readouterr()
        assert "Upload failed" in captured.err


# ---------------------------------------------
# cmd_db Tests
# ---------------------------------------------

class TestCmdDb:
    """Tests for the db subcommand handler."""

    def _make_args(self, query=None, session_mode="ASK_DATA", **db_params):
        ns = MagicMock()
        ns.dms_instance_id = db_params.get("dms_instance_id", 111)
        ns.dms_db_id       = db_params.get("dms_db_id", 222)
        ns.instance_name   = db_params.get("instance_name", "rm-test")
        ns.db_name         = db_params.get("db_name", "testdb")
        ns.tables          = db_params.get("tables", "album,artist")
        ns.table_ids       = None
        ns.engine          = "mysql"
        ns.region          = "cn-hangzhou"
        ns.query           = query
        ns.session_mode    = session_mode
        ns.async_run       = False
        ns.enable_search   = False
        ns.output          = "summary"
        return ns

    def test_missing_params_exits(self, capsys):
        ns = MagicMock()
        ns.dms_instance_id = None
        ns.dms_db_id       = None
        ns.instance_name   = None
        ns.db_name         = None
        ns.tables          = None
        ns.session_mode    = "ASK_DATA"
        ns.query           = None

        with pytest.raises(SystemExit) as exc:
            cmd_db(ns)
        assert exc.value.code == 1
        captured = capsys.readouterr()
        assert "Missing required" in captured.err

    def test_single_query_executed(self, mock_session, capsys):
        args = self._make_args(query="show tables")

        with patch("cli.cmd_db.DataAgentConfig.from_env"), \
             patch("cli.cmd_db.DataAgentClient"), \
             patch("cli.cmd_db.SessionManager") as mock_sm_cls, \
             patch("cli.cmd_db.MessageHandler") as mock_mh_cls:

            mock_sm_cls.return_value.create_or_reuse.return_value = mock_session
            mock_mh_cls.return_value.stream_events.return_value = iter([])

            cmd_db(args)

        mock_mh_cls.return_value.stream_events.assert_called_once()
        captured = capsys.readouterr()
        assert "Session ready" in captured.out

    def test_default_batch_queries_ask_data(self, mock_session, capsys):
        """No --query runs two preset ASK_DATA queries."""
        args = self._make_args()

        with patch("cli.cmd_db.DataAgentConfig.from_env"), \
             patch("cli.cmd_db.DataAgentClient"), \
             patch("cli.cmd_db.SessionManager") as mock_sm_cls, \
             patch("cli.cmd_db.MessageHandler") as mock_mh_cls:

            mock_sm_cls.return_value.create_or_reuse.return_value = mock_session
            mock_mh_cls.return_value.stream_events.return_value = iter([])

            cmd_db(args)

        assert mock_mh_cls.return_value.stream_events.call_count == 2

    def test_default_batch_queries_analysis(self, mock_session, capsys):
        """ANALYSIS mode also runs two preset queries."""
        args = self._make_args(session_mode="ANALYSIS")

        with patch("cli.cmd_db.DataAgentConfig.from_env"), \
             patch("cli.cmd_db.DataAgentClient"), \
             patch("cli.cmd_db.SessionManager") as mock_sm_cls, \
             patch("cli.cmd_db.MessageHandler") as mock_mh_cls:

            mock_sm_cls.return_value.create_or_reuse.return_value = mock_session
            mock_mh_cls.return_value.stream_events.return_value = iter([])

            cmd_db(args)

        assert mock_mh_cls.return_value.stream_events.call_count == 2


# ---------------------------------------------
# Formatting Helper Tests
# ---------------------------------------------

class TestFmtJupyterCell:
    """Tests for _fmt_jupyter_cell."""

    def _make_outer(self, inner_dict):
        return {"result": json.dumps(inner_dict)}

    def test_empty_outputs_returns_none(self):
        outer = self._make_outer({"title": "Step 1", "content_type": "sql",
                                  "content": "SELECT 1", "nb_file_outputs": []})
        assert _fmt_jupyter_cell(outer) is None

    def test_dms_executing_placeholder_skipped(self):
        outer = self._make_outer({
            "title": "Running",
            "content_type": "sql",
            "content": "SELECT 1",
            "nb_file_outputs": [
                {"output_type": "display_data",
                 "metadata": {"content_type": "dms/executing"},
                 "data": {}}
            ],
        })
        assert _fmt_jupyter_cell(outer) is None

    def test_sql_cell_with_table_result(self):
        outer = self._make_outer({
            "title": "Query Results",
            "content_type": "sql",
            "content": "SELECT name FROM artist",
            "nb_file_outputs": [
                {
                    "output_type": "display_data",
                    "metadata": {},
                    "data": {
                        "application/json": {
                            "data": {
                                "columns": [{"field": "name", "title": "Name"}],
                                "result": [{"name": "AC/DC"}, {"name": "Beatles"}],
                            }
                        }
                    },
                }
            ],
        })
        result = _fmt_jupyter_cell(outer)
        assert result is not None
        assert "Query Results" in result
        assert "SELECT name FROM artist" in result
        assert "AC/DC" in result

    def test_code_cell_stream_output(self):
        outer = self._make_outer({
            "title": "",
            "content_type": "code",
            "content": "print('hello')",
            "nb_file_outputs": [
                {"output_type": "stream", "text": "hello", "metadata": {}}
            ],
        })
        result = _fmt_jupyter_cell(outer)
        assert result is not None
        assert "hello" in result
        # Code content itself should NOT appear (only output)
        assert "print('hello')" not in result

    def test_invalid_json_result_returns_none(self):
        outer = {"result": "NOT JSON {{{{"}
        assert _fmt_jupyter_cell(outer) is None


class TestFmtInsights:
    """Tests for _fmt_insights."""

    def test_basic_insight(self):
        items = [{"title": "Sales Trend", "summary": "Revenue grew 20% YoY."}]
        result = _fmt_insights(items)
        assert "Sales Trend" in result
        assert "Revenue grew 20% YoY." in result
        assert "Analysis Result" in result

    def test_insight_with_data_table(self):
        items = [{
            "title": "Top Artists",
            "summary": "By revenue",
            "data": json.dumps({
                "columns": ["Artist", "Revenue"],
                "data": [["AC/DC", 100], ["Beatles", 90]],
            }),
        }]
        result = _fmt_insights(items)
        assert "Artist" in result
        assert "AC/DC" in result

    def test_multiple_insights(self):
        items = [
            {"title": "Insight A", "summary": "Summary A"},
            {"title": "Insight B", "summary": "Summary B"},
        ]
        result = _fmt_insights(items)
        assert "Insight A" in result
        assert "Insight B" in result

    def test_insight_caps_rows_at_10(self):
        rows = [[f"row{i}", i] for i in range(20)]
        items = [{
            "title": "Big Table",
            "summary": "",
            "data": json.dumps({"columns": ["Label", "Val"], "data": rows}),
        }]
        result = _fmt_insights(items)
        # Only first 10 rows rendered
        assert "row9" in result
        assert "row10" not in result


class TestFormatDataEvent:
    """Tests for _format_data_event."""

    def test_plain_text_returned_as_is(self):
        result = _format_data_event("hello world")
        assert result == "hello world"

    def test_unknown_list_returns_none(self, capsys):
        content = json.dumps([{"no_title_key": "val"}])
        result = _format_data_event(content)
        assert result is None

    def test_insights_list_formatted(self):
        items = [{"title": "T", "summary": "S"}]
        content = json.dumps(items)
        result = _format_data_event(content)
        assert result is not None
        assert "T" in result

    def test_jupyter_cell_dict_formatted(self):
        inner = {
            "title": "Cell",
            "content_type": "sql",
            "content": "SELECT 1",
            "nb_file_outputs": [
                {
                    "output_type": "display_data",
                    "metadata": {},
                    "data": {
                        "application/json": {
                            "data": {
                                "columns": [{"field": "x", "title": "X"}],
                                "result": [{"x": 42}],
                            }
                        }
                    },
                }
            ],
        }
        content = json.dumps({"result_type": "jupyter_cell", "result": json.dumps(inner)})
        result = _format_data_event(content)
        assert result is not None
        assert "42" in result

    def test_unknown_dict_returns_none(self, capsys):
        content = json.dumps({"result_type": "unknown_type", "data": "xyz"})
        result = _format_data_event(content)
        assert result is None

    def test_invalid_json_returned_as_is(self, capsys):
        result = _format_data_event("{invalid json")
        assert result == "{invalid json"


# ---------------------------------------------
# New Formatter Tests (plan, status_change, conclusion, suggestions)
# ---------------------------------------------

class TestFmtPlanProgress:
    """Tests for _fmt_plan_progress."""

    def _plan_data(self, steps, current_step=1):
        return {
            "current_step": current_step,
            "plan_status": "executing",
            "plans": [{
                "plan": {
                    "steps": [
                        {"name": s[0], "description": s[1]} for s in steps
                    ]
                }
            }],
        }

    def test_basic_plan_display(self):
        data = self._plan_data([
            ("SQL Generation", "Generate SQL from question"),
            ("Data Analysis", "Analyze query results"),
        ], current_step=1)
        out = _fmt_plan_progress(data, 1, 2)
        assert "[Plan] Step 1/2: SQL Generation" in out
        assert "Generate SQL from question" in out

    def test_step_2_of_3(self):
        data = self._plan_data([
            ("A", "desc A"), ("B", "desc B"), ("C", "desc C"),
        ], current_step=2)
        out = _fmt_plan_progress(data, 2, 3)
        assert "[Plan] Step 2/3: B" in out
        assert "desc B" in out

    def test_no_steps_graceful(self):
        data = {"current_step": 1, "plans": []}
        out = _fmt_plan_progress(data, 1, 0)
        assert "[Plan] Step 1/0" in out

    def test_out_of_range_step(self):
        data = self._plan_data([("Only", "one")], current_step=5)
        out = _fmt_plan_progress(data, 5, 1)
        # Should not crash, just show step number without name
        assert "[Plan] Step 5/1" in out


class TestFmtStatusChange:
    """Tests for _fmt_status_change."""

    def test_basic_transition(self):
        content = json.dumps({
            "previous": "PLANNING",
            "current": "STEP_EXECUTION",
            "current_task": "STEP_EXECUTION",
        })
        out = _fmt_status_change(content)
        assert "[Task] PLANNING \u2192 STEP_EXECUTION" in out

    def test_different_task_shown(self):
        content = json.dumps({
            "previous": "none",
            "current": "SQL_GENERATION",
            "current_task": "QUERY_PROCESSING",
        })
        out = _fmt_status_change(content)
        assert "SQL_GENERATION" in out
        assert "(QUERY_PROCESSING)" in out

    def test_same_task_not_duplicated(self):
        content = json.dumps({
            "previous": "A",
            "current": "B",
            "current_task": "B",
        })
        out = _fmt_status_change(content)
        assert out.count("B") == 1  # not duplicated

    def test_invalid_json_returns_none(self):
        assert _fmt_status_change("not json {{{") is None


class TestFmtOutputConclusion:
    """Tests for _fmt_output_conclusion."""

    def test_wraps_text(self):
        out = _fmt_output_conclusion("Key finding: revenue grew 20%")
        assert "Analysis Conclusion" in out
        assert "Key finding: revenue grew 20%" in out
        # Has visual borders
        assert "##" in out

    def test_multiline_text(self):
        text = "Line 1\nLine 2\nLine 3"
        out = _fmt_output_conclusion(text)
        assert "Line 1" in out
        assert "Line 3" in out


class TestFmtRecommendedQuestions:
    """Tests for _fmt_recommended_questions."""

    def test_dict_with_questions(self):
        content = json.dumps({"questions": ["Q1?", "Q2?", "Q3?"]})
        out = _fmt_recommended_questions(content)
        assert "Suggestions" in out
        assert "1. Q1?" in out
        assert "3. Q3?" in out

    def test_dict_with_recommend_key(self):
        content = json.dumps({"recommendQuestions": ["A?", "B?"]})
        out = _fmt_recommended_questions(content)
        assert "1. A?" in out

    def test_list_format(self):
        content = json.dumps(["X?", "Y?"])
        out = _fmt_recommended_questions(content)
        assert "1. X?" in out

    def test_empty_returns_none(self):
        content = json.dumps({"questions": []})
        assert _fmt_recommended_questions(content) is None

    def test_invalid_json_returns_none(self):
        assert _fmt_recommended_questions("not json") is None


# ---------------------------------------------
# StreamState + _print_event dispatch tests
# ---------------------------------------------

class TestStreamStateDispatch:
    """Tests for the stateful event dispatch architecture."""

    def _make_event(self, event_type, category=None, content="",
                    content_type=None, data=None):
        from data_agent import SSEEvent
        return SSEEvent(
            event_type=event_type,
            data=data or {},
            category=category,
            content=content,
            content_type=content_type,
        )

    def test_content_type_field_on_sse_event(self):
        from data_agent import SSEEvent
        e = SSEEvent(event_type="data", data={}, content_type="json")
        assert e.content_type == "json"

    def test_content_type_default_none(self):
        from data_agent import SSEEvent
        e = SSEEvent(event_type="data", data={})
        assert e.content_type is None

    def test_status_change_printed_in_detail(self, capsys):
        state = StreamState(output_mode="detail")
        event = self._make_event(
            "status_change",
            content=json.dumps({"previous": "none", "current": "SQL_GEN", "current_task": "SQL_GEN"}),
            content_type="json",
        )
        _print_event(event, "detail", state=state)
        captured = capsys.readouterr()
        assert "[Task] none \u2192 SQL_GEN" in captured.out
        assert state.current_task == "SQL_GEN"

    def test_status_change_silent_in_summary(self, capsys):
        state = StreamState(output_mode="summary")
        event = self._make_event(
            "status_change",
            content=json.dumps({"previous": "none", "current": "SQL_GEN", "current_task": "SQL_GEN"}),
            content_type="json",
        )
        _print_event(event, "summary", state=state)
        captured = capsys.readouterr()
        assert captured.out == ""
        # But state is still tracked
        assert state.current_task == "SQL_GEN"

    def test_plan_data_event_detail_mode(self, capsys):
        state = StreamState(output_mode="detail")
        plan_json = json.dumps({
            "current_step": 1,
            "plan_status": "executing",
            "plans": [{"plan": {"steps": [
                {"name": "Analyze", "description": "Do analysis"},
                {"name": "Report", "description": "Generate report"},
            ]}}],
        })
        event = self._make_event("data", category="plan", content=plan_json, content_type="json")
        _print_event(event, "detail", state=state)
        captured = capsys.readouterr()
        assert "[Plan] Step 1/2: Analyze" in captured.out
        assert state.current_step == 1
        assert state.total_steps == 2

    def test_plan_data_event_summary_compact(self, capsys):
        state = StreamState(output_mode="summary")
        plan_json = json.dumps({
            "current_step": 1,
            "plan_status": "executing",
            "plans": [{"plan": {"steps": [
                {"name": "Analyze", "description": "Do analysis"},
                {"name": "Report", "description": "Generate report"},
            ]}}],
        })
        event = self._make_event("data", category="plan", content=plan_json, content_type="json")
        _print_event(event, "summary", state=state)
        captured = capsys.readouterr()
        # summary mode defers step label to next conclusion header
        assert captured.out == ""
        assert state.pending_step_label == "Step 1/2: Analyze"
        assert state.step_names == {1: "Analyze", 2: "Report"}
        assert "[Plan]" not in captured.out  # no verbose label

    def test_plan_step_change_detected(self, capsys):
        state = StreamState(output_mode="detail")
        # Step 1
        plan1 = json.dumps({
            "current_step": 1, "plan_status": "executing",
            "plans": [{"plan": {"steps": [{"name": "A", "description": ""}, {"name": "B", "description": ""}]}}],
        })
        _print_event(self._make_event("data", "plan", plan1, "json"), "detail", state=state)
        # Step 2
        plan2 = json.dumps({
            "current_step": 2, "plan_status": "executing",
            "plans": [{"plan": {"steps": [{"name": "A", "description": ""}, {"name": "B", "description": ""}]}}],
        })
        _print_event(self._make_event("data", "plan", plan2, "json"), "detail", state=state)
        captured = capsys.readouterr()
        assert "Step 1/2: A" in captured.out
        assert "Step 2/2: B" in captured.out

    def test_duplicate_plan_step_not_reprinted(self, capsys):
        state = StreamState(output_mode="detail")
        plan = json.dumps({
            "current_step": 1, "plan_status": "executing",
            "plans": [{"plan": {"steps": [{"name": "A", "description": ""}]}}],
        })
        _print_event(self._make_event("data", "plan", plan, "json"), "detail", state=state)
        _print_event(self._make_event("data", "plan", plan, "json"), "detail", state=state)
        captured = capsys.readouterr()
        # Should only appear once
        assert captured.out.count("Step 1/1: A") == 1

    def test_output_conclusion_lifecycle(self, capsys):
        """content_start -> delta x N -> content_finish accumulates conclusion."""
        state = StreamState()
        # content_start
        _print_event(self._make_event("content_start", "output_conclusion"), "summary", state=state)
        assert state.content_category == "output_conclusion"
        assert state.output_conclusion_chunks is not None
        # deltas
        _print_event(self._make_event("delta", "output_conclusion", "Hello "), "summary", state=state)
        _print_event(self._make_event("delta", "output_conclusion", "World"), "summary", state=state)
        # content_finish
        _print_event(self._make_event("content_finish"), "summary", state=state)
        captured = capsys.readouterr()
        assert "Analysis Conclusion" in captured.out
        assert "Hello World" in captured.out
        assert state.output_conclusion_chunks is None

    def test_output_conclusion_data_without_lifecycle(self, capsys):
        """Old-format data/output_conclusion without content_start."""
        state = StreamState()
        event = self._make_event("data", "output_conclusion", "Direct conclusion text", "str")
        _print_event(event, "summary", state=state)
        captured = capsys.readouterr()
        assert "Analysis Conclusion" in captured.out
        assert "Direct conclusion text" in captured.out

    def test_tool_call_response_lifecycle_detail(self, capsys):
        """content_start -> delta (JSON chunks) -> content_finish for jupyter_cell in detail mode."""
        state = StreamState(output_mode="detail")
        inner = json.dumps({
            "title": "Query",
            "content_type": "sql",
            "content": "SELECT 1",
            "nb_file_outputs": [{
                "output_type": "display_data",
                "metadata": {},
                "data": {"application/json": {"data": {
                    "columns": [{"field": "x", "title": "X"}],
                    "result": [{"x": 42}],
                }}},
            }],
        })
        payload = json.dumps({"result_type": "jupyter_cell", "result": inner})
        chunk1 = payload[:50]
        chunk2 = payload[50:]

        _print_event(self._make_event("content_start", "tool_call_response"), "detail", state=state)
        _print_event(self._make_event("delta", "tool_call_response", chunk1), "detail", state=state)
        _print_event(self._make_event("delta", "tool_call_response", chunk2), "detail", state=state)
        _print_event(self._make_event("content_finish"), "detail", state=state)
        captured = capsys.readouterr()
        assert "42" in captured.out
        assert state.got_content is True

    def test_tool_call_response_silent_in_summary(self, capsys):
        """jupyter_cell tool_call_response suppressed in summary mode."""
        state = StreamState(output_mode="summary")
        inner = json.dumps({
            "title": "Query",
            "content_type": "sql",
            "content": "SELECT 1",
            "nb_file_outputs": [{
                "output_type": "display_data",
                "metadata": {},
                "data": {"application/json": {"data": {
                    "columns": [{"field": "x", "title": "X"}],
                    "result": [{"x": 42}],
                }}},
            }],
        })
        payload = json.dumps({"result_type": "jupyter_cell", "result": inner})
        _print_event(self._make_event("content_start", "tool_call_response"), "summary", state=state)
        _print_event(self._make_event("delta", "tool_call_response", payload), "summary", state=state)
        _print_event(self._make_event("content_finish"), "summary", state=state)
        captured = capsys.readouterr()
        assert captured.out == ""

    def test_chat_finish_chat_category(self, capsys):
        state = StreamState()
        _print_event(self._make_event("chat_finish", "chat"), "summary", state=state)
        captured = capsys.readouterr()
        assert "\u2705" in captured.out
        assert state.got_content is True

    def test_chat_canceled(self, capsys):
        state = StreamState()
        _print_event(self._make_event("chat_canceled"), "summary", state=state)
        captured = capsys.readouterr()
        assert "[Canceled]" in captured.out

    def test_jsx_report_displayed(self, capsys):
        state = StreamState()
        content = json.dumps({"type": "analysis"})
        _print_event(self._make_event("data", "jsx_report", content, "json"), "summary", state=state)
        captured = capsys.readouterr()
        assert "Report Generated" in captured.out

    def test_mission_report_displayed(self, capsys):
        state = StreamState()
        content = json.dumps({"title": "Final Report"})
        _print_event(self._make_event("data", "mission_report", content, "json"), "summary", state=state)
        captured = capsys.readouterr()
        assert "Report Generated" in captured.out
        assert "Final Report" in captured.out

    def test_recommended_questions_displayed(self, capsys):
        state = StreamState()
        content = json.dumps({"questions": ["What is X?", "How about Y?"]})
        _print_event(self._make_event("data", "recommended_question", content, "json"), "summary", state=state)
        captured = capsys.readouterr()
        assert "Suggestions" in captured.out
        assert "What is X?" in captured.out

    def test_finalize_flushes_pending_conclusion(self, capsys):
        """_finalize_stream flushes un-closed output_conclusion."""
        state = StreamState()
        state.content_category = "output_conclusion"
        state.output_conclusion_chunks = ["Unflushed ", "text"]
        _finalize_stream(state)
        captured = capsys.readouterr()
        assert "Analysis Conclusion" in captured.out
        assert "Unflushed text" in captured.out
        assert state.got_content is True

    def test_raw_mode_shows_content_type(self, capsys):
        event = self._make_event("data", "plan", '{"test": 1}', content_type="json")
        _print_event(event, "raw")
        captured = capsys.readouterr()
        assert "ct=json" in captured.out

    def test_delta_silent_in_summary_for_llm(self, capsys):
        """llm/think deltas should be silent in summary mode."""
        state = StreamState()
        _print_event(self._make_event("delta", "llm", "thinking..."), "summary", state=state)
        captured = capsys.readouterr()
        assert captured.out == ""

    def test_data_fallback_silent_in_summary(self, capsys):
        """Unrecognised data categories suppressed in summary mode."""
        state = StreamState(output_mode="summary")
        content = json.dumps({"title": "Some step", "status": "running"})
        _print_event(self._make_event("data", "unknown_cat", content, "json"), "summary", state=state)
        captured = capsys.readouterr()
        assert captured.out == ""

    def test_data_fallback_shown_in_detail(self, capsys):
        """Unrecognised data categories shown in detail mode."""
        state = StreamState(output_mode="detail")
        content = json.dumps({"title": "Some step"})
        _print_event(self._make_event("data", "unknown_cat", content, "json"), "detail", state=state)
        captured = capsys.readouterr()
        assert "[Step] Some step" in captured.out
