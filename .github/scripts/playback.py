"""Exercise the built CLI with isolated config and null audio output."""

import argparse
import csv
import io
import http.server
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import threading
import time
import wave


def player_pids():
    """Track players created by the test, including Windows mpv launchers."""
    if os.name == "nt":
        output = subprocess.check_output(["tasklist", "/FO", "CSV", "/NH"], text=True)
        return {
            int(row[1])
            for row in csv.reader(io.StringIO(output))
            if row and row[0].lower() in {"mpv.exe", "mpv.com", "ffmpeg.exe"}
        }
    output = subprocess.check_output(["ps", "-axo", "pid=,comm="], text=True)
    return {
        int(pid)
        for line in output.splitlines()
        for pid, command in [line.strip().split(maxsplit=1)]
        if Path(command).name in {"mpv", "ffmpeg"}
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--youtube", metavar="URL", help="test a live YouTube stream instead of local audio")
    parser.add_argument("--legacy-bin-dir", type=Path, help="directory containing v0.3.2 for upgrade testing")
    args = parser.parse_args()
    binary = Path("chill.exe" if os.name == "nt" else "chill").resolve()
    baseline = player_pids()

    # A short runtime path also leaves room for macOS's Unix socket limit.
    with tempfile.TemporaryDirectory(prefix="c-", dir=os.environ.get("RUNNER_TEMP")) as root:
        root = Path(root)
        env = dict(os.environ, HOME=str(root), USERPROFILE=str(root),
                   XDG_CONFIG_HOME=str(root / "config"), APPDATA=str(root / "config"),
                   TMPDIR=str(root), TMP=str(root), TEMP=str(root), MPV_HOME=str(root))
        (root / "mpv.conf").write_text("ao=null\nloop-file=inf\n", encoding="utf-8")
        audio = root / "silence.wav"
        with wave.open(str(audio), "wb") as wav:
            wav.setnchannels(1)
            wav.setsampwidth(2)
            wav.setframerate(8000)
            # The PCM decoder owns media EOF; mpv's loop-file setting only
            # applies to the legacy backend. Keep the fixture alive throughout.
            wav.writeframes(bytes(8000 * 2 * 180))

        def run(*command, executable=binary):
            result = subprocess.run([str(executable), *command], env=env, capture_output=True,
                                    text=True, encoding="utf-8", errors="replace", timeout=15)
            output = re.sub(r"\x1b\[[0-9;]*m", "", result.stdout).strip()
            if result.returncode:
                raise RuntimeError(f"chill {' '.join(command)} failed:\n{output}\n{result.stderr}")
            return output

        def require(condition, message):
            if not condition:
                raise RuntimeError(message)

        def wait_for(state, timeout=10):
            deadline = time.monotonic() + timeout
            previous = None
            while time.monotonic() < deadline:
                status = run("--status")
                if status != previous:
                    print(status, flush=True)
                    previous = status
                actual = status.split(" │ ", 1)[0]
                if actual == state:
                    return status
                if actual == "failed":
                    raise RuntimeError(f"Playback failed: {status}")
                time.sleep(0.1)
            raise RuntimeError(f"Timed out waiting for {state}: {previous}")

        try:
            # Exercise the real help entry points with playback dependencies
            # unavailable, so help remains usable before installation/setup.
            for arguments, expected in [
                (("--help",), ("Commands:", "doctor", "add <name>", "remove <name>",
                               "default <name>", "upgrade", "--status --json", "--stations")),
                (("-h",), ("Commands:", "doctor", "--status --json")),
                (("doctor", "--help"), ("chill doctor [options]", "--stations", "--stream", "--timeout", "--logs")),
            ]:
                help_result = subprocess.run([str(binary), *arguments], env=dict(env, PATH=""),
                                             capture_output=True, text=True, timeout=15)
                help_text = help_result.stdout + help_result.stderr
                require(help_result.returncode == 0 and all(word in help_text for word in expected),
                        f"Incomplete help for {arguments}: {help_text}")
            stopped = json.loads(run("--status", "--json"))
            require(not stopped["running"] and stopped["state"] == "stopped",
                    f"Unexpected status before startup: {stopped}")
            doctor = run("doctor")
            require("no daemon running" in doctor, "Doctor did not report the stopped daemon")
            # A nonempty directory blocks both Unix sockets and the Windows
            # discovery file, forcing a real child-daemon startup failure.
            blocked = root / ("chill.port" if os.name == "nt" else f"chill-{os.getuid()}.sock")
            blocked.mkdir()
            (blocked / "blocker").write_text("test", encoding="utf-8")
            try:
                failed = subprocess.run([str(binary), "--toggle"], env=env, capture_output=True,
                                        text=True, timeout=15)
                require(failed.returncode != 0 and "daemon failed to start" in failed.stderr
                        and "failed to start daemon" in failed.stderr and "daemon.log" in failed.stderr,
                        f"Missing daemon startup diagnostics: {failed.stderr}")
                stale = subprocess.run([str(binary), "--status", "--json"], env=env,
                                       capture_output=True, text=True, timeout=15)
                require(stale.returncode != 0 and json.loads(stale.stdout)["state"] == "unknown",
                        f"Stale IPC was not reported as JSON: {stale.stdout}")
            finally:
                shutil.rmtree(blocked)
            run("add", "ci-audio", args.youtube or str(audio), "CI playback")
            run("default", "ci-audio")
            if args.legacy_bin_dir:
                legacy = args.legacy_bin_dir / binary.name
                # Leave an actual old daemon running, then issue an ordinary
                # volume command using the new executable, without --stop.
                run("ci-audio", executable=legacy)
                run("--vol", "55", executable=legacy)
                run("--toggle", executable=legacy)
                old_players = player_pids() - baseline
                inspected = json.loads(run("--status", "--json"))
                require(inspected["compatibility"] == "daemon-outdated",
                        f"Legacy daemon was not identified: {inspected}")
                require("stale daemon" in run("doctor"), "Doctor did not identify the stale daemon")
                require(player_pids() - baseline == old_players,
                        "Read-only inspection restarted the legacy player")
                run("--vol", "90")
                status = wait_for("paused")
                require("ci-audio" in status and "vol 90" in status,
                        f"Upgrade lost station, pause, or volume: {status}")
                upgraded_players = player_pids() - baseline
                require(upgraded_players and not (old_players & upgraded_players),
                        "Outdated player was not replaced")
                run("--vol", "60")
                require(player_pids() - baseline == upgraded_players,
                        "Volume change restarted the upgraded player")
                require(run("--status").startswith("paused │"), "Upgrade did not retain pause")
                run("--stop")
                wait_for("not running")
                print("v0.3.2 daemon upgraded automatically; subsequent volume changes kept the same player", flush=True)
            print(run("--toggle"), flush=True)
            wait_for("playing", timeout=190 if args.youtube else 10)
            # Catch failures that occur just after mpv's file-loaded event.
            time.sleep(0.5)
            require(run("--status").startswith("playing │"), "Playback did not stay playing")
            run("--toggle")
            run("--vol", "31")
            run("--mute")
            status = wait_for("paused")
            require("vol 31" in status and "muted" in status, f"Live controls failed: {status}")
            structured = json.loads(run("--status", "--json"))
            require(structured["running"] and structured["paused"] and structured["muted"]
                    and structured["volume"] == 31 and structured["station"] == "ci-audio",
                    f"Structured status lost playback fields: {structured}")
            run("--mute")
            status = run("--status")
            require(status.startswith("paused │") and "muted" not in status,
                    f"Unmute changed pause state or failed: {status}")
            run("--toggle")
            wait_for("playing")
            run("--sleep", "1h")
            require("sleep " in run("--status"), "Sleep countdown missing")
            run("--sleep", "off")
            require("sleep " not in run("--status"), "Sleep cancellation failed")
            run("--sleep", "300ms")
            wait_for("idle")

            if not args.youtube:
                run("--stop")
                wait_for("not running")
                print(run("--toggle"), flush=True)
                status = wait_for("playing")
                require("ci-audio" in status and "vol 31" in status,
                        f"Default station or volume was not remembered: {status}")
                run("remove", "ci-audio")
                require("ci-audio" not in run("--list"), "Removed station still listed")
                require(run("--status").startswith("playing │"), "Removal interrupted playback")

                class PodcastHandler(http.server.BaseHTTPRequestHandler):
                    def log_message(self, *args):
                        pass

                    def do_GET(self):
                        if self.path == "/feed":
                            host = f"http://127.0.0.1:{self.server.server_port}"
                            body = (f'<rss xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel>'
                                    f'<title>CI Podcast</title><item><title>First episode</title><guid>one</guid>'
                                    f'<itunes:duration>180</itunes:duration><enclosure url="{host}/audio" type="audio/wav"/>'
                                    f'</item><item><title>Second episode</title><guid>two</guid>'
                                    f'<enclosure url="{host}/audio" type="audio/wav"/></item></channel></rss>').encode()
                        else:
                            body = audio.read_bytes()
                        self.send_response(200)
                        self.send_header("Content-Length", str(len(body)))
                        self.end_headers()
                        self.wfile.write(body)

                server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), PodcastHandler)
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                feed = f"http://127.0.0.1:{server.server_port}/feed"
                try:
                    episodes = json.loads(run("podcasts", "episodes", feed, "--json"))
                    require(len(episodes["episodes"]) == 2, "Podcast feed was not parsed")
                    run("podcasts", "subscribe", feed)
                    subscriptions = json.loads(run("podcasts", "subscriptions", "--json"))
                    require(subscriptions[0]["feed_url"] == feed, "Subscription was not saved")
                    run("podcasts", "play", feed, "1")
                    wait_for("playing")
                    run("pause")
                    run("seek", "+30")
                    wait_for("paused")
                    state = json.loads(run("status", "--json"))
                    require(state["episode"]["guid"] == "one" and state["position"] >= 30,
                            f"Podcast seek failed: {state}")
                    run("speed", "1.5")
                    run("podcasts", "queue", feed, "2")
                    require(len(json.loads(run("podcasts", "queue", "--json"))) == 1,
                            "Episode queue was not updated")
                    run("next")
                    wait_for("playing")
                    state = json.loads(run("--status", "--json"))
                    require(state["episode"]["guid"] == "two" and state["speed"] == 1.5,
                            f"Queue transition lost episode/speed: {state}")
                    run("--stop")
                    run("podcasts", "play", feed, "1")
                    wait_for("playing")
                    state = json.loads(run("--status", "--json"))
                    require(state["position"] >= 25 and state["speed"] == 1.5,
                            f"Restart lost podcast position/speed: {state}")
                    run("podcasts", "unsubscribe", feed)
                    require(not json.loads(run("podcasts", "subscriptions", "--json")),
                            "Unsubscribe did not persist")
                    print("Podcast CLI, subscriptions, seeking, speed, and queue passed", flush=True)
                finally:
                    server.shutdown()
                    server.server_close()
                    thread.join()
        finally:
            print(run("--stop"), flush=True)
            wait_for("not running")
            deadline = time.monotonic() + 5
            while True:
                remaining = player_pids() - baseline
                if not remaining:
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError(f"audio processes left after stop: {remaining}")
                time.sleep(0.1)
    print("CLI playback, controls, timer, and process cleanup passed", flush=True)


if __name__ == "__main__":
    main()
