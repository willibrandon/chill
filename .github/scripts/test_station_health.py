import json
from pathlib import Path
import unittest
from unittest.mock import patch

from station_health import Audit, MARKER, TITLE, confirmed_failures, parse_audit, publish


class StationHealthTests(unittest.TestCase):
    def test_resolution_and_non_live_results(self):
        output = """[OK] client: chill dev
[OK] mpv: mpv 0.41
Checking lofi-girl (https://example.com/live)...
[OK] lofi-girl: music (is_live; stream resolves)
Checking sleep (https://example.com/ended)...
[WARN] sleep: music (was_live; stream resolves)
Checking study (https://example.com/broken)...
[FAIL] study: stream resolution: unavailable
more extractor diagnostics
"""
        result = parse_audit(output, 1)
        self.assertEqual(set(result.stations), {"lofi-girl", "sleep", "study"})
        self.assertEqual(set(result.failures), {"sleep", "study"})

    def test_incomplete_or_setup_failures_do_not_become_station_issues(self):
        good = "Checking sleep (https://example.com/live)...\n[OK] sleep: live\n"
        for output, code in [("", 0), (good.splitlines()[0], 1), (good, 1),
                             ("[FAIL] yt-dlp: missing\n" + good, 1)]:
            with self.subTest(output=output), self.assertRaises(RuntimeError):
                parse_audit(output, code)

    def test_only_repeated_failures_for_same_url_are_confirmed(self):
        first = Audit({"a": "url-a", "b": "url-b"}, {"a": "offline", "b": "offline"})
        second = Audit({"a": "url-a", "b": "new-url", "c": "url-c"},
                       {"a": "still offline", "b": "offline", "c": "offline"})
        self.assertEqual(confirmed_failures(first, second), {"a": "still offline"})
        self.assertEqual(confirmed_failures(first, Audit(first.stations, {})), {})

    @patch("station_health.gh")
    def test_issue_created_then_updated_and_closed_after_recovery(self, gh):
        report = Path("summary.md")
        failed = Audit({"sleep": "url"}, {"sleep": "offline"})
        gh.return_value = "[]"
        publish("owner/repo", report, failed, failed.failures, "run-url")
        self.assertEqual(gh.call_args.args[:2], ("issue", "create"))

        gh.return_value = json.dumps([{"number": 12, "title": TITLE, "body": MARKER}])
        publish("owner/repo", report, failed, failed.failures, "run-url")
        self.assertEqual(gh.call_args.args[:3], ("issue", "edit", "12"))

        gh.reset_mock()
        publish("owner/repo", report, failed, {}, "run-url")
        self.assertEqual(gh.call_count, 1)  # unconfirmed failures don't close an existing issue

        publish("owner/repo", report, Audit(failed.stations, {}), {}, "run-url")
        self.assertEqual(gh.call_args.args[:3], ("issue", "close", "12"))

    @patch("station_health.gh")
    def test_unrelated_issue_with_same_title_is_not_edited(self, gh):
        gh.return_value = json.dumps([{"number": 13, "title": TITLE, "body": "User report"}])
        publish("owner/repo", Path("summary.md"), Audit({}, {}), {}, "run-url")
        self.assertEqual(gh.call_count, 1)


if __name__ == "__main__":
    unittest.main()
