#!/usr/bin/env python3
"""Check a built Chill against macOS process audio, using real playback only."""
import argparse
import functools
import http.server
import json
import math
import os
from pathlib import Path
import platform
import struct
import subprocess
import tempfile
import threading
import time
import wave


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path('chill'))
    parser.add_argument('--source', default='chillhop')
    parser.add_argument('--local-only', action='store_true')
    parser.add_argument('--report', type=Path)
    args = parser.parse_args()
    if platform.system() != 'Darwin':
        raise RuntimeError('This check requires macOS with a physical audio output; playback was not verified')
    binary = args.binary.resolve()
    repo = Path(__file__).resolve().parent.parent
    results = []
    with tempfile.TemporaryDirectory(prefix='chill-audio-', dir='/tmp') as directory:
        root = Path(directory)
        env = dict(os.environ, CHILL_CONFIG_DIR=str(root / 'config'), TMPDIR=str(root))
        # Race-enabled CLI subprocesses otherwise sleep a second at every exit.
        env['GORACE'] = env.get('GORACE', '') + ' atexit_sleep_ms=0 halt_on_error=1'
        meter = root / 'output-meter'
        subprocess.run(['clang', '-fobjc-arc', '-framework', 'Foundation', '-framework', 'CoreAudio',
                        str(repo / 'internal/playback/testdata/output_meter.m'), '-o', str(meter)], check=True, timeout=30)
        with wave.open(str(root / 'tone.wav'), 'wb') as audio:
            audio.setnchannels(2)
            audio.setsampwidth(2)
            audio.setframerate(48000)
            second = b''.join(struct.pack('<hh', *(2*[round(0.15*32767*math.sin(2*math.pi*440*i/48000))])) for i in range(48000))
            for _ in range(180):
                audio.writeframesraw(second)
        class Handler(http.server.SimpleHTTPRequestHandler):
            def log_message(self, *_):
                pass
            def do_GET(self):
                try:
                    super().do_GET()
                except (BrokenPipeError, ConnectionResetError):
                    pass  # A station/settings change cancels the old HTTP read.
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), functools.partial(Handler, directory=str(root)))
        threading.Thread(target=server.serve_forever, daemon=True).start()
        with (root / 'daemon.log').open('w+') as log:
            daemon = subprocess.Popen([str(binary), '--daemon'], env=env, stdout=log, stderr=log)
            def command(*arguments):
                completed = subprocess.run([str(binary), *arguments], env=env, capture_output=True, text=True, timeout=25)
                if completed.returncode:
                    raise RuntimeError(f'{arguments}: {completed.stdout}\n{completed.stderr}')
                return completed.stdout.strip()
            def status():
                return json.loads(command('status', '--json'))
            def wait_playing():
                deadline = time.monotonic() + 60
                while time.monotonic() < deadline:
                    state = status()
                    if state.get('state') == 'failed':
                        raise RuntimeError(f'Playback failed: {state}')
                    if state.get('playing'):
                        time.sleep(0.5)
                        return state
                    time.sleep(0.1)
                raise RuntimeError(f'Playback never started: {status()}')
            def measure(label, audible=True):
                before = status()
                measured = subprocess.run([str(meter), str(daemon.pid)], capture_output=True, text=True, timeout=15)
                try:
                    data = json.loads(measured.stdout)
                except ValueError:
                    raise RuntimeError(f'macOS output measurement failed: {measured.stdout}\n{measured.stderr}')
                after = status()
                data.update(check=label, backend=after.get('audio', {}).get('backend'),
                            position_before=before.get('position', 0), position_after=after.get('position', 0))
                data['nonzero_ratio'] = data['nonzero'] / max(data['samples'], 1)
                results.append(data)
                print(json.dumps(data), flush=True)
                if data['error'] or data['samples'] < 48000:
                    raise RuntimeError(f'{label}: macOS output could not be measured')
                if audible:
                    if data['nonzero_ratio'] < .90 or data['rms'] < .001:
                        raise RuntimeError(f'{label}: macOS received silence/dropouts despite playback status')
                    if data['position_after'] <= data['position_before'] + 1:
                        raise RuntimeError(f'{label}: playback clock did not advance with sound')
                elif data['nonzero_ratio'] > .01 or data['rms'] > .0001:
                    raise RuntimeError(f'{label}: audio continued while paused or muted')
                return data
            try:
                deadline = time.monotonic() + 10
                socket = root / f'chill-{os.getuid()}' / 'daemon.sock'
                while not socket.exists():
                    if daemon.poll() is not None or time.monotonic() > deadline:
                        raise RuntimeError('Daemon did not start')
                    time.sleep(.05)
                command('play', f'http://127.0.0.1:{server.server_port}/tone.wav')
                wait_playing()
                measure('HTTP tone')
                command('pause')
                time.sleep(.5)
                paused = measure('pause', audible=False)
                if abs(paused['position_after'] - paused['position_before']) > .1:
                    raise RuntimeError('Playback clock moved while paused')
                command('resume')
                time.sleep(.5)
                measure('resume')
                command('mute')
                time.sleep(.5)
                measure('mute', audible=False)
                command('mute')
                time.sleep(.5)
                measure('unmute')
                command('audio', 'sample-rate', '44100')
                wait_playing()
                measure('44100 Hz source')
                command('audio', 'sample-rate', '48000')
                wait_playing()
                measure('48000 Hz source')
                devices = [d for d in json.loads(command('device', 'list', '--json')) if d['id'] != 'auto']
                if len(devices) == 1:
                    command('device', 'set', devices[0]['id'])
                    time.sleep(.5)
                    measure('explicit output')
                    command('device', 'set', 'auto')
                    time.sleep(.5)
                    measure('return to automatic output')
                if not args.local_only:
                    command('play', args.source)
                    wait_playing()
                    for i in range(3):
                        measure(f'{args.source} window {i+1}')
            finally:
                try:
                    command('stop')
                finally:
                    try:
                        daemon.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        daemon.terminate()
                        daemon.wait(timeout=10)
                    server.shutdown()
                    log.seek(0)
                    diagnostics = log.read()
                    if diagnostics:
                        print(diagnostics, flush=True)
                    if args.report:
                        args.report.write_text(json.dumps(results, indent=2) + '\n')
    print('PASS: real playback reached macOS; controls and playback clock agreed.', flush=True)


if __name__ == '__main__':
    main()
