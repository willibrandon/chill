# Station health

`station-health.yml` audits all built-in streams daily at 09:23 UTC and can also
be run manually on the default branch. It uses an isolated config, current
yt-dlp, mpv, and Deno. Deterministic CI remains independent of YouTube access.

If any station fails to resolve or is no longer live, the workflow repeats the
audit after five minutes. A station must fail both passes with the same URL
before it is reported in the **Built-in station health checks failing** issue.
Later confirmed failures update that issue; a fully healthy audit closes it.
Failures seen only on the confirmation pass leave an existing issue open.

Each run retains logs for 14 days and adds a workflow summary. Setup failures
fail the workflow without opening a station issue. Check the logs to distinguish
an unavailable broadcast from extractor or runner-network failures.

The job uses the repository's built-in GitHub token with `issues: write`; no
additional secret is needed. The existing **YouTube smoke test** workflow remains
available for manual end-to-end playback checks.

Run the reporting tests locally:

```sh
python3 -m unittest discover -s .github/scripts -p 'test_*.py'
```
