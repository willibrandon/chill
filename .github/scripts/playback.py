"""Exercise the built CLI with isolated config and null audio output."""

import argparse
import csv
import io
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import wave


def player_pids():
    """Track players created by the test, including Windows mpv launchers."""
    if os.name == "nt":
        output = subprocess.check_output(["tasklist", "/FO", "CSV", "/NH"], text=True)
        return {
            int(row[1])
            for row in csv.reader(io.StringIO(output))
            if row and row[0].lower() in {"mpv.exe", "mpv.com"}
        }
    output = subprocess.check_output(["ps", "-axo", "pid=,comm="], text=True)
    return {
        int(pid)
        for line in output.splitlines()
        for pid, command in [line.strip().split(maxsplit=1)]
        if Path(command).name == "mpv"
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
            wav.writeframes(bytes(16000))

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
            print(run(), flush=True)
            wait_for("playing", timeout=190 if args.youtube else 10)
            # Catch failures that occur just after mpv's file-loaded event.
            time.sleep(0.5)
            require(run("--status").startswith("playing │"), "Playback did not stay playing")
            run("--toggle")
            run("--vol", "31")
            run("--mute")
            status = wait_for("paused")
            require("vol 31" in status and "muted" in status, f"Live controls failed: {status}")
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
                print(run(), flush=True)
                status = wait_for("playing")
                require("ci-audio" in status and "vol 31" in status,
                        f"Default station or volume was not remembered: {status}")
                run("remove", "ci-audio")
                require("ci-audio" not in run("--list"), "Removed station still listed")
                require(run("--status").startswith("playing │"), "Removal interrupted playback")
        finally:
            print(run("--stop"), flush=True)
            wait_for("not running")
            deadline = time.monotonic() + 5
            while True:
                remaining = player_pids() - baseline
                if not remaining:
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError(f"mpv processes left after stop: {remaining}")
                time.sleep(0.1)
    print("CLI playback, controls, timer, and process cleanup passed", flush=True)


if __name__ == "__main__":
    main()
