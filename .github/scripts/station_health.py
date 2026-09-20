"""Audit built-in stations and maintain one issue for confirmed failures."""

import argparse
from dataclasses import dataclass
import json
import os
from pathlib import Path
import re
import subprocess
import time


TITLE = "Built-in station health checks failing"
MARKER = "<!-- chill-station-health -->"


@dataclass
class Audit:
    stations: dict[str, str]
    failures: dict[str, str]


def parse_audit(output, returncode):
    stations = {}
    results = {}
    setup_failed = False
    for line in output.splitlines():
        check = re.fullmatch(r"Checking ([\w-]+) \((https?://\S+)\)\.\.\.", line)
        if check:
            name, url = check.groups()
            stations[name] = url
            continue
        result = re.match(r"\[(OK|WARN|FAIL)\] ([\w-]+): (.*)", line)
        if result:
            level, name, message = result.groups()
            if name in stations:
                results[name] = (level, message)
            elif level == "FAIL":
                setup_failed = True
    if setup_failed or not stations or stations.keys() != results.keys():
        raise RuntimeError("Incomplete station audit or setup failure; inspect the audit logs")
    failures = {name: message for name, (level, message) in results.items() if level != "OK"}
    if returncode and not failures:
        raise RuntimeError("Doctor failed without a station finding; inspect the audit logs")
    # WARN includes resolved broadcasts which are no longer live.
    return Audit(stations, failures)


def audit(binary, log):
    result = subprocess.run(
        [binary, "doctor", "--stations", "--timeout", "45s"],
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=360,
    )
    log.write_text(result.stdout, encoding="utf-8")
    return parse_audit(result.stdout, result.returncode)


def confirmed_failures(first, latest):
    # A changed URL must get its own confirmation rather than inherit a failure.
    return {name: latest.failures[name] for name in first.failures.keys() & latest.failures.keys()
            if first.stations[name] == latest.stations[name]}


def summary(latest, confirmed, run_url):
    lines = [MARKER, "## Built-in station health", "",
             f"[Audit logs and workflow run]({run_url})", ""]
    if confirmed:
        lines += ["These stations failed both audits, five minutes apart:", ""]
        for name in sorted(confirmed):
            lines.append(f"- `{name}` — {latest.stations[name]}")
        lines += ["", "Checks verify stream resolution and live status from a GitHub-hosted Ubuntu runner.",
                  "The attached audit logs include extractor and network diagnostics."]
    elif latest.failures:
        lines.append("Some checks failed only on the confirmation pass; no new issue was opened.")
    else:
        lines.append("All built-in stations resolve and are live.")
    lines += ["", "This issue is updated on confirmed failures and closed after a healthy audit."]
    return "\n".join(lines) + "\n"


def gh(*args):
    return subprocess.check_output(["gh", *args], text=True)


def publish(repo, report, latest, confirmed, run_url):
    issues = json.loads(gh("issue", "list", "--repo", repo, "--state", "open",
                          "--search", f'"{TITLE}" in:title', "--limit", "100",
                          "--json", "number,title,body"))
    existing = next((issue for issue in issues
                     if issue["title"] == TITLE and issue["body"].startswith(MARKER)), None)
    if confirmed:
        if existing:
            gh("issue", "edit", str(existing["number"]), "--repo", repo, "--body-file", str(report))
        else:
            gh("issue", "create", "--repo", repo, "--title", TITLE, "--body-file", str(report))
    elif not latest.failures and existing:
        gh("issue", "close", str(existing["number"]), "--repo", repo,
           "--comment", f"All built-in stations are healthy again: {run_url}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="./chill")
    parser.add_argument("--output", type=Path, default=Path("station-health"))
    parser.add_argument("--publish", action="store_true")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    first = audit(args.binary, args.output / "first.log")
    latest = first
    confirmed = {}
    if first.failures:
        print("Station checks failed; confirming in five minutes.", flush=True)
        time.sleep(300)
        latest = audit(args.binary, args.output / "confirmation.log")
        confirmed = confirmed_failures(first, latest)
    repo = os.environ.get("GITHUB_REPOSITORY", "willibrandon/chill")
    server = os.environ.get("GITHUB_SERVER_URL", "https://github.com")
    run_url = f"{server}/{repo}/actions/runs/{os.environ.get('GITHUB_RUN_ID', '')}"
    report = args.output / "summary.md"
    report.write_text(summary(latest, confirmed, run_url), encoding="utf-8")
    if step_summary := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(step_summary, "a", encoding="utf-8") as out:
            out.write(report.read_text(encoding="utf-8"))
    if args.publish:
        publish(repo, report, latest, confirmed, run_url)
    return int(bool(confirmed))


if __name__ == "__main__":
    raise SystemExit(main())
