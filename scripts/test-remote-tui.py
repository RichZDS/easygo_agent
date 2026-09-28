#!/usr/bin/env python3
"""Real PTY proof against scripts/test-platform.mjs's local fixture stack.
No third-party packages. Does not start services or call a paid provider.
"""
import argparse
import codecs
import errno
import fcntl
import http.cookiejar
import json
import os
from pathlib import Path
import pty
import re
import select
import signal
import struct
import subprocess
import termios
import time
import unicodedata
import urllib.parse
import urllib.request


class Screen:
    """Small VT screen sufficient for Bubble Tea's cursor/erase renderer."""
    def __init__(self, width=120, height=40):
        self.width, self.height = width, height
        self.rows = [[' '] * width for _ in range(height)]
        self.x = self.y = 0
        self.saved = (0, 0)
        self.pending = ''
        self.decoder = codecs.getincrementaldecoder('utf-8')('replace')

    def feed(self, raw, reply):
        self.pending += self.decoder.decode(raw)
        while self.pending:
            text = self.pending
            if text[0] == '\x1b':
                if len(text) < 2:
                    return
                if text[1] == '[':
                    match = re.match(r'\x1b\[([0-?]*)([ -/]*)([@-~])', text)
                    if not match:
                        return
                    self.pending = text[match.end():]
                    params, _, command = match.groups()
                    if command == 'n' and params == '6':
                        reply(b'\x1b[1;1R')
                    else:
                        self.csi(params, command)
                    continue
                if text[1] == ']':
                    end = re.search(r'\x07|\x1b\\', text[2:])
                    if not end:
                        return
                    body = text[2:2 + end.start()]
                    if body in ('10;?', '11;?'):
                        reply(('\x1b]' + body[:2] + ';rgb:0000/0000/0000\x1b\\').encode())
                    self.pending = text[2 + end.end():]
                    continue
                self.pending = text[2:]
                if text[1] == '7':
                    self.saved = self.x, self.y
                elif text[1] == '8':
                    self.x, self.y = self.saved
                continue
            self.pending = text[1:]
            char = text[0]
            if char == '\r':
                self.x = 0
            elif char == '\n':
                self.y += 1
                self.scroll()
            elif char == '\b':
                self.x = max(0, self.x - 1)
            elif ord(char) >= 32 and not unicodedata.combining(char):
                width = 2 if unicodedata.east_asian_width(char) in ('W', 'F') else 1
                if self.x + width > self.width:
                    self.x = 0
                    self.y += 1
                    self.scroll()
                self.rows[self.y][self.x] = char
                if width == 2:
                    self.rows[self.y][self.x + 1] = ''
                self.x += width

    def scroll(self):
        if self.y >= self.height:
            self.rows.pop(0)
            self.rows.append([' '] * self.width)
            self.y = self.height - 1

    def csi(self, params, command):
        if params.startswith('?'):
            if params == '?1049' and command == 'h':
                self.rows = [[' '] * self.width for _ in range(self.height)]
                self.x = self.y = 0
            return
        values = [int(v) if v else 0 for v in params.split(';')]
        n = values[0] or 1
        if command in ('H', 'f'):
            self.y = min(self.height - 1, n - 1)
            self.x = min(self.width - 1, (values[1] or 1) - 1 if len(values) > 1 else 0)
        elif command == 'A': self.y = max(0, self.y - n)
        elif command == 'B': self.y = min(self.height - 1, self.y + n)
        elif command == 'C': self.x = min(self.width - 1, self.x + n)
        elif command == 'D': self.x = max(0, self.x - n)
        elif command in ('G', '`'): self.x = min(self.width - 1, n - 1)
        elif command == 'd': self.y = min(self.height - 1, n - 1)
        elif command == 'J':
            if values[0] == 2:
                self.rows = [[' '] * self.width for _ in range(self.height)]
            elif values[0] == 0:
                self.rows[self.y][self.x:] = [' '] * (self.width - self.x)
                for y in range(self.y + 1, self.height): self.rows[y] = [' '] * self.width
        elif command == 'K':
            start, end = (0, self.width) if values[0] == 2 else ((0, self.x + 1) if values[0] == 1 else (self.x, self.width))
            self.rows[self.y][start:end] = [' '] * (end - start)
        elif command == 's': self.saved = self.x, self.y
        elif command == 'u': self.x, self.y = self.saved

    def text(self):
        return '\n'.join(''.join(row).rstrip() for row in self.rows).rstrip() + '\n'


class Terminal:
    def __init__(self, command, env, evidence, label):
        self.evidence, self.label = evidence, label
        self.screen = Screen()
        self.raw = bytearray()
        self.status = None
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.execve(command[0], command, env)
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack('HHHH', 40, 120, 0, 0))
        os.kill(self.pid, signal.SIGWINCH)
        self.open = True

    def send(self, data):
        if self.open:
            os.write(self.fd, data)

    def pump(self, timeout=0.1):
        if self.open and select.select([self.fd], [], [], timeout)[0]:
            try:
                data = os.read(self.fd, 65536)
            except OSError as error:
                if error.errno != errno.EIO: raise
                data = b''
            if data:
                self.raw.extend(data)
                self.screen.feed(data, self.send)
            else:
                self.open = False
        if self.status is None:
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid:
                self.status = os.waitstatus_to_exitcode(status)

    def snapshot(self, name):
        text = self.screen.text()
        (self.evidence / (name + '.txt')).write_text(text)
        (self.evidence / (self.label + '.ansi')).write_bytes(self.raw)
        return text

    def until(self, predicate, name, timeout=25):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.pump()
            if predicate(self.screen.text()):
                # A renderer frame may span PTY reads. Drain its remaining bytes
                # before saving a screen, rather than accepting a partial frame.
                settled = time.monotonic() + 0.15
                while time.monotonic() < settled:
                    self.pump(0.02)
                if predicate(self.screen.text()):
                    return self.snapshot(name)
            if self.status is not None:
                break
        self.snapshot(name + '-FAILED')
        raise AssertionError(f'{name}: expected frame not observed, process status={self.status}')

    def stop(self):
        if self.status is None:
            os.kill(self.pid, signal.SIGKILL)
            _, status = os.waitpid(self.pid, 0)
            self.status = os.waitstatus_to_exitcode(status)
        self.snapshot(self.label + '-final')
        os.close(self.fd)
        self.open = False


class API:
    def __init__(self, origin, email, password):
        self.origin = origin
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.call('/api/login', {'email': email, 'password': password})

    def call(self, path, body):
        req = urllib.request.Request(self.origin + path, data=json.dumps(body).encode(), headers={'Content-Type': 'application/json', 'Origin': self.origin})
        with self.opener.open(req, timeout=10) as response:
            return json.load(response)

    def rpc(self, method, **params):
        return self.call('/api/rpc', {'method': method, 'params': params})['result']


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--origin', required=True)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--email', default='alice@example.test')
    parser.add_argument('--password-env', default='EASYGO_TUI_TEST_PASSWORD')
    parser.add_argument('--evidence', required=True)
    parser.add_argument('--bad-password', action='store_true')
    args = parser.parse_args()
    url = urllib.parse.urlsplit(args.origin)
    assert url.scheme == 'http' and url.hostname in ('127.0.0.1', 'localhost'), 'local fixture origin required'
    evidence = Path(args.evidence).resolve()
    evidence.mkdir(parents=True, exist_ok=True)
    password = os.environ[args.password_env]
    binary = str(Path(args.binary).resolve())
    env = {**os.environ, 'TERM': 'xterm-256color', 'LANG': 'C.UTF-8', args.password_env: password + '-wrong' if args.bad_password else password}
    command = [binary, '--url', args.origin, '--email', args.email, '--password-env', args.password_env]
    began = time.monotonic()
    terminals = []
    report = {'pass': False, 'origin': args.origin, 'mode': 'bad-password' if args.bad_password else 'interactive', 'terminal': {'columns': 120, 'rows': 40}, 'paid_provider': False}
    try:
        if args.bad_password:
            terminal = Terminal(command, env, evidence, 'bad-password')
            terminals.append(terminal)
            terminal.until(lambda frame: 'platform HTTP 401' in frame, '01-login-rejected')
            deadline = time.monotonic() + 5
            while terminal.status is None and time.monotonic() < deadline: terminal.pump()
            assert terminal.status == 1, f'expected application exit 1, got {terminal.status}'
            report['application_exit'] = terminal.status
        else:
            api = API(args.origin, args.email, password)
            listed = subprocess.run(command + ['--sessions'], env=env, text=True, capture_output=True, timeout=20, check=True)
            existing = json.loads(listed.stdout)
            assert existing, 'fixture should contain a prior session'
            (evidence / '01-cli-session-list.txt').write_text(listed.stdout)
            terminal = Terminal(command, env, evidence, 'interactive')
            terminals.append(terminal)
            terminal.until(lambda frame: 'EASY GO' in frame and 'running: 0' in frame, '02-new-session')
            match = re.search(rb'Session: ([a-f0-9-]{36})', terminal.raw)
            assert match, 'session ID not printed by TUI startup'
            session = match[1].decode()
            assert session not in {s['id'] for s in existing}, 'new session was not created'
            report['session_id'] = session
            terminal.send(b'PTY fixture hello\r')
            terminal.until(lambda frame: 'assistant: PLATFORM_OK' in frame, '03-response')
            terminal.send(b'HANG_MODEL\r')
            terminal.until(lambda frame: 'running: 1' in frame and 'HANG_MODEL' in frame, '04-running-queue')
            # Running state can precede dispatch. Require the real gateway's
            # provisional fixture delta before sending the cancellation key.
            runs = api.rpc('agent.session.history', session_id=session)['runs']
            hanging = next(r for r in runs if r.get('input') == 'HANG_MODEL')
            deadline = time.monotonic() + 10
            while True:
                events = api.rpc('agent.run.events', run_id=hanging['id'])['events']
                if any(e['kind'] == 'delta' and e['data'].get('event', {}).get('delta') == 'waiting' for e in events):
                    break
                assert time.monotonic() < deadline, 'HANG_MODEL did not reach real provider fixture'
                terminal.pump(0.1)
            (evidence / '04-provider-events.json').write_text(json.dumps(events, indent=2))
            terminal.snapshot('04-running-queue')
            terminal.send(b'\x03')
            terminal.until(lambda frame: 'assistant (canceled)' in frame and 'running: 0' in frame, '05-canceled')
            runs = api.rpc('agent.session.history', session_id=session)['runs']
            canceled = [r for r in runs if r.get('input') == 'HANG_MODEL']
            assert len(canceled) == 1 and canceled[0]['status'] == 'canceled', canceled
            report['canceled_run_id'] = canceled[0]['id']
            (evidence / '05-server-runs.json').write_text(json.dumps(runs, indent=2))
            terminal.stop()
            terminals.remove(terminal)
            reconnect = Terminal(command + ['--session', session], env, evidence, 'reconnect')
            terminals.append(reconnect)
            reconnect.until(lambda frame: 'EASY GO' in frame and 'assistant: PLATFORM_OK' in frame and 'HANG_MODEL' in frame, '06-reconnected-history')
            # There are no in-TUI session-picker or memory/skill panels. Exercise
            # existing CLI modes separately and label them honestly in evidence.
            for name in ('memory', 'skills'):
                method = 'agent.' + name + '.list'
                cli = subprocess.run(command + ['--rpc', method], input='{}\n', env=env, text=True, capture_output=True, timeout=20, check=True)
                data = json.loads(cli.stdout)
                assert data == api.rpc(method), f'{name} CLI/public namespace mismatch'
                assert data['memories' if name == 'memory' else 'skills'], f'no fixture {name}'
                (evidence / ('07-cli-' + name + '.txt')).write_text(cli.stdout)
            report['limitations'] = ['Session listing/selection use --sessions/--session; no interactive session picker.', 'Memory/skills use --rpc CLI mode; no Bubble Tea panels.']
            report['checks'] = ['new session in real PTY', 'PLATFORM_OK rendered', 'running queue rendered', 'Ctrl+C cancellation rendered and server-confirmed', 'SIGKILL/reconnect history restored', 'CLI memory/skills equal same-user public RPC']
        report['pass'] = True
    except Exception as error:
        report['error'] = str(error)
        raise
    finally:
        for terminal in terminals:
            terminal.stop()
        report['elapsed_seconds'] = round(time.monotonic() - began, 3)
        (evidence / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
        print(json.dumps(report))


if __name__ == '__main__':
    main()
