#!/usr/bin/env python3
"""Record real WTC commands against fresh, offline, synthetic Git repos."""
import argparse
import codecs
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pty
import re
import select
import signal
import shutil
import struct
import subprocess
import tempfile
import termios
import time

import pyte

ROOT = Path(__file__).resolve().parents[2]
OUT = Path(__file__).resolve().parent
COLS, ROWS = 88, 16


class RecordingScreen(pyte.Screen):
    # Bubble Tea optimizes updates with SU/SD within scroll margins; pyte 0.8.2
    # does not dispatch these two CSI commands. Preserve them in text captures.
    def scroll_up(self, count=1):
        previous = self.cursor.y
        self.cursor.y = self.margins.bottom if self.margins else self.lines - 1
        for _ in range(min(count or 1, self.lines)):
            self.index()
        self.cursor.y = previous

    def scroll_down(self, count=1):
        previous = self.cursor.y
        self.cursor.y = self.margins.top if self.margins else 0
        for _ in range(min(count or 1, self.lines)):
            self.reverse_index()
        self.cursor.y = previous


class RecordingStream(pyte.Stream):
    csi = {**pyte.Stream.csi, 'S': 'scroll_up', 'T': 'scroll_down'}
    events = pyte.Stream.events | {'scroll_up', 'scroll_down'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--wtc', required=True, type=Path)
    parser.add_argument('--agg', type=Path)
    parser.add_argument('--only', choices=['all', 'harness'], default='all')
    parser.add_argument('--cli-version', help='exact pin; must match the supplied binary')
    parser.add_argument('--source-revision', help='public source commit for capture provenance')
    args = parser.parse_args()
    binary = args.wtc.resolve()
    binary_sha256 = hashlib.sha256(binary.read_bytes()).hexdigest()
    if args.source_revision and not re.fullmatch(r'[0-9a-f]{40}', args.source_revision):
        parser.error('--source-revision must be a full public commit ID')
    sandbox = Path(tempfile.mkdtemp(prefix='wtc-public-demo-')).resolve()
    bindir = sandbox / 'bin'
    bindir.mkdir()
    for name, target in [('wtc', binary), ('taba', binary)]:
        (bindir / name).symlink_to(target.resolve())
    # No inherited project environment, credential store selection, prompt, or
    # user Git config. Tool binaries are available without global mise/herdr.
    env = {'PATH': str(bindir) + ':/usr/bin:/bin:/usr/sbin:/sbin',
           'TERM': 'xterm-256color', 'COLORTERM': 'truecolor', 'LANG': 'en_US.UTF-8',
           'HOME': os.environ['HOME'],  # Preserve the account home; never redirect it.
           'XDG_CONFIG_HOME': str(sandbox / 'xdg-config'),
           'XDG_DATA_HOME': str(sandbox / 'xdg-data'),
           'GIT_PAGER': 'cat',
           'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null',
           'GIT_AUTHOR_NAME': 'Example', 'GIT_AUTHOR_EMAIL': 'example@example.invalid',
           'GIT_COMMITTER_NAME': 'Example', 'GIT_COMMITTER_EMAIL': 'example@example.invalid',
           'WTC_CONFIG_ROOT': str(sandbox / 'workspace' / '.config'),
           'TABA_DEV_BUILD': 'off', 'WTC_DEV_BUILD': 'off'}
    try:
        version_output = subprocess.check_output([str(binary), '--version'], env=env, text=True).strip()
    except (OSError, subprocess.CalledProcessError):
        shutil.rmtree(sandbox)
        raise
    match = re.fullmatch(r'(?:taba|wtc) version (\d+\.\d+\.\d+)', version_output)
    if not match:
        shutil.rmtree(sandbox)
        parser.error('supply a published stable CLI binary')
    version = match.group(1)
    if args.cli_version and args.cli_version != version:
        shutil.rmtree(sandbox)
        parser.error('--cli-version differs from the supplied binary')
    provenance = {'version': version, 'binarySha256': binary_sha256}
    if args.source_revision:
        provenance['sourceRevision'] = args.source_revision
    workspace = sandbox / 'workspace'
    sources = sandbox / 'sources'
    sources.mkdir()
    (workspace / '.bare').mkdir(parents=True)

    def run(command, cwd, check=True):
        return subprocess.run(command, cwd=cwd, env=env, check=check,
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)

    def git(cwd, *args):
        return run(['git', *args], cwd)

    def owner(name):
        source = sources / name
        git(source, 'init', '-b', 'main')
        git(source, 'add', '.')
        git(source, 'commit', '-m', 'Synthetic demo fixture')
        bare = workspace / '.bare' / (name + '.git')
        git(sandbox, 'clone', '--bare', str(source), str(bare))
        git(sandbox, '--git-dir=' + str(bare), 'config', 'remote.origin.fetch',
            '+refs/heads/*:refs/remotes/origin/*')
        git(sandbox, '--git-dir=' + str(bare), 'fetch', 'origin')

    def safe_output(data):
        # Replace only this recording's sandbox path; retain actual command output.
        return data.replace(str(sandbox), '~/wtc-demo')

    class Recording:
        def __init__(self, name, title):
            self.name = name
            self.start = time.monotonic()
            self.events = []
            self.transcript = []
            self.emit('\x1b[2J\x1b[H\x1b[1;35m' + title + '\x1b[0m\r\n\r\n')
            self.transcript.append(title + '\n')

        def emit(self, data):
            self.events.append([round(time.monotonic() - self.start, 3), 'o', data])

        def command(self, command, cwd, expected=0, pause=1.2, keys=(), finish=False):
            label = 'workspace' if cwd == workspace else cwd.name
            self.emit(f'\x1b[2m{label}\x1b[0m \x1b[36m❯\x1b[0m ')
            # A little typing gives each command a readable lead-in.
            for char in command:
                self.emit(char.replace('\n', '\r\n  '))
                time.sleep(0.018)
            self.emit('\r\n')
            self.transcript.append(f'{label} ❯ {command}\n')
            master, slave = pty.openpty()
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', ROWS, COLS, 0, 0))
            process = subprocess.Popen(['/bin/sh', '-c', command], cwd=cwd,
                                       env=env, stdin=slave,
                                       stdout=slave, stderr=slave, start_new_session=True)
            os.close(slave)
            screen = RecordingScreen(COLS, ROWS)
            stream = RecordingStream(screen)
            decoder = codecs.getincrementaldecoder('utf-8')(errors='replace')
            output, pending, snapshot = [], '', None
            started = time.monotonic()
            actions = iter(keys)
            action = next(actions, None)
            recording = True

            def flush(text, final=False):
                nonlocal pending
                pending += text
                # Hold a possible split sandbox path until the next chunk.
                hold = 0
                if not final:
                    for size in range(1, len(str(sandbox))):
                        if pending.endswith(str(sandbox)[:size]):
                            hold = size
                visible = pending[:-hold] if hold else pending
                pending = pending[-hold:] if hold else ''
                if visible and recording:
                    self.emit(safe_output(visible))

            try:
                while True:
                    elapsed = time.monotonic() - started
                    if action and elapsed >= action[0]:
                        if action[1] == 'q':
                            snapshot = '\n'.join(line.rstrip() for line in screen.display).strip()
                            if finish:
                                flush('', final=True)
                                recording = False
                        os.write(master, action[1].encode())
                        action = next(actions, None)
                    ready, _, _ = select.select([master], [], [], 0.05)
                    if ready:
                        try:
                            data = os.read(master, 65536)
                        except OSError:
                            break
                        if not data:
                            break
                        text = decoder.decode(data)
                        output.append(text)
                        stream.feed(text)
                        flush(text)
                        # Answer terminal queries, as a real terminal would.
                        if b'\x1b[6n' in data:
                            os.write(master, b'\x1b[1;1R')
                    if elapsed > 30:
                        raise RuntimeError('demo command timed out: ' + command)
                flush(decoder.decode(b'', final=True), final=True)
                code = process.wait(timeout=5)
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
                os.close(master)
            text = safe_output(''.join(output))
            if keys:
                if snapshot is None or '\x1b[?1049h' not in text:
                    raise RuntimeError('interactive screen was not captured: ' + command)
                self.transcript.append('[Interactive screen before q]\n' + safe_output(snapshot) + '\n\n')
            else:
                self.transcript.append(text.replace('\r\n', '\n') + '\n')
            if code != expected:
                raise RuntimeError(f'{command}: exit {code}, expected {expected}\n{text}')
            time.sleep(pause)

        def save(self):
            if hashlib.sha256(binary.read_bytes()).hexdigest() != binary_sha256:
                raise RuntimeError('CLI binary changed during recording')
            header = {'version': 2, 'width': COLS, 'height': ROWS,
                      'title': self.name, 'env': {'TERM': 'xterm-256color'},
                      'source': provenance}
            cast = OUT / (self.name + '.cast')
            cast.write_text('\n'.join(json.dumps(x) for x in [header, *self.events]) + '\n')
            transcript = re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]', '', ''.join(self.transcript)).replace('\r', '')
            transcript = '\n'.join(line.rstrip() for line in transcript.splitlines())
            (OUT / (self.name + '.txt')).write_text(re.sub(r'\n{3,}', '\n\n', transcript).rstrip() + '\n')
            if args.agg:
                subprocess.run([str(args.agg.resolve()), '--quiet', '--theme', 'nord',
                                '--font-size', '20', '--line-height', '1.3', '--fps-cap', '15',
                                '--last-frame-duration', '4', '--idle-time-limit', '3',
                                str(cast), str(OUT / (self.name + '.gif'))], check=True)

    try:
        # The scaffold is exercised here, outside a collection. The pin
        # is fixture metadata only: every command uses the supplied binary.
        harness = sources / 'agent-harness'
        rec = Recording('harness', 'Taba / a small, project-owned harness')
        rec.command(f'taba harness init agent-harness \\\n  --remote https://example.invalid/h.git \\\n  --cli-version {version}', sources, pause=1.5)
        rec.command('cd agent-harness', sources, pause=0.4)
        rec.command('ls -A', harness, pause=1.5)
        rec.command('cat taba.toml', harness, pause=2)
        rec.save()
        if args.only == 'harness':
            print('Recorded harness scaffold only.')
            return
        registry = 'schema_version: 1\nrepositories:\n'
        for name, offset in [('agent-harness', 0), ('api', 1), ('web', 2)]:
            registry += f'  - name: {name}\n    remote: {sources / name}\n    default_ref: origin/main\n'
            if offset:
                registry += f'    port_offset: {offset}\n'
        (harness / '.harness-repos.yml').write_text(registry)
        (sources / 'api').mkdir()
        (sources / 'api' / 'README.md').write_text('# Synthetic API\n')
        (sources / 'web').mkdir()
        (sources / 'web' / 'README.md').write_text('# Synthetic frontend\n')
        for name in ['api', 'web']:
            (sources / name / '.gitignore').write_text('.env\n')
            control = Path(env['WTC_CONFIG_ROOT']) / name
            control.mkdir(parents=True)
            (control / '.env').write_text('DEMO_ONLY=synthetic\n')
        for name in ['agent-harness', 'api', 'web']:
            owner(name)
        main_dir = workspace / 'main'
        git(sandbox, '--git-dir=' + str(workspace / '.bare/agent-harness.git'),
            'worktree', 'add', '--detach', str(main_dir / 'harness'), 'origin/main')
        run(['wtc', 'env', 'setup', '--skip-hooks'], main_dir)
        run(['wtc', 'skills', 'render', '--seed-scope'], main_dir)

        rec = Recording('collections', 'WTC / one task, two repositories')
        rec.command('wtc new add-search api web', main_dir)
        task = workspace / 'add-search'
        rec.command('cd ../add-search', main_dir, pause=0.4)
        rec.command('wtc status', task, keys=[(3.5, 'q')], pause=0.5)
        rec.command('echo "Search in progress" >> web/README.md', task)
        rec.command('wtc status', task, keys=[(3, 'r'), (5.5, 'q')], finish=True)
        # Reset only the disposable recording fixture's edit.
        git(task / 'web', 'restore', 'README.md')
        rec.save()

        rec = Recording('inventories', 'WTC / inspect environment and secret links')
        rec.command('wtc env list', task, keys=[(2, 'j'), (3, 'G'), (5, 'q')], pause=0.5)
        rec.command('wtc secrets list', task, keys=[(2, 'j'), (4.5, 'q')], finish=True)
        rec.save()

        print('Recorded harness scaffold, live status, and inventory TUIs.')
    finally:
        # Every owner and worktree here belongs to this invocation.
        shutil.rmtree(sandbox)


if __name__ == '__main__':
    main()
