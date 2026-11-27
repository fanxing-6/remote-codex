import argparse
import json
from pathlib import Path
import plistlib
import socket
import sys
import threading
import time
import urllib.request
from urllib.parse import urlparse
from contextlib import contextmanager

import frida
import psutil
import websocket

sys.dont_write_bytecode = True

PORT = 9229


def inspector_is_owned(process):
    listeners = [item for item in process.net_connections(kind='tcp')
                 if item.status == psutil.CONN_LISTEN and item.laddr.port == PORT]
    if listeners:
        if len(listeners) != 1 or listeners[0].laddr.ip != '127.0.0.1':
            raise RuntimeError('INSPECTOR_BIND_INVALID')
        return True
    with socket.socket() as connection:
        connection.settimeout(1)
        if connection.connect_ex(('127.0.0.1', PORT)) == 0:
            raise RuntimeError('INSPECTOR_PORT_OWNED')
    return False


def enable_inspector(process):
    session = frida.attach(process.pid)
    try:
        script = session.create_script(Path(__file__).with_name('darwin_inspector.js').read_text())
        script.load()
        try:
            result = script.exports_sync.enable()
            if result['pid'] != process.pid or not result['enabled'] or result['alreadyOpen']:
                raise RuntimeError('INSPECTOR_IDENTITY_INVALID')
        finally:
            script.unload()
    finally:
        session.detach()
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if inspector_is_owned(process):
            return
        time.sleep(0.1)
    raise TimeoutError('INSPECTOR_START_TIMEOUT')


class Inspector:
    def __init__(self, process):
        if not inspector_is_owned(process):
            raise RuntimeError('INSPECTOR_NOT_OWNED')
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(f'http://127.0.0.1:{PORT}/json/list', timeout=5) as response:
            targets = json.load(response)
        if len(targets) != 1 or targets[0]['title'] != 'electron/js2c/browser_init':
            raise RuntimeError('INSPECTOR_TARGET_INVALID')
        endpoint = urlparse(targets[0]['webSocketDebuggerUrl'])
        if endpoint.scheme != 'ws' or endpoint.netloc != f'127.0.0.1:{PORT}':
            raise RuntimeError('INSPECTOR_ENDPOINT_INVALID')
        self.connection = websocket.create_connection(
            endpoint.geturl(), timeout=20, suppress_origin=True, http_no_proxy=['127.0.0.1'])
        self.sequence = 0
        if self.evaluate('process.pid') != process.pid:
            self.close()
            raise RuntimeError('INSPECTOR_PID_INVALID')

    def evaluate(self, expression):
        self.sequence += 1
        self.connection.send(json.dumps({
            'id': self.sequence, 'method': 'Runtime.evaluate',
            'params': {'expression': expression, 'awaitPromise': True, 'returnByValue': True},
        }))
        while True:
            response = json.loads(self.connection.recv())
            if response.get('id') == self.sequence:
                break
        if 'error' in response or 'exceptionDetails' in response['result']:
            raise RuntimeError('INSPECTOR_EVALUATION_FAILED')
        return response['result']['result'].get('value')

    def close(self):
        self.connection.close()


@contextmanager
def transient_inspector(process, metrics):
    if inspector_is_owned(process):
        raise RuntimeError('INSPECTOR_ALREADY_OPEN')
    observed = [None, None]
    finished = threading.Event()

    def observe():
        while not finished.is_set():
            with socket.socket() as connection:
                connection.settimeout(0.02)
                listening = connection.connect_ex(('127.0.0.1', 9229)) == 0
            now = time.monotonic()
            if listening and observed[0] is None:
                observed[0] = now
            if not listening and observed[0] is not None and observed[1] is None:
                observed[1] = now
            finished.wait(0.005)

    watcher = threading.Thread(target=observe, daemon=True)
    watcher.start()
    begin = time.monotonic()
    inspector = None
    try:
        enable_inspector(process)
        inspector = Inspector(process)
        yield inspector
    finally:
        try:
            if inspector is None and inspector_is_owned(process):
                inspector = Inspector(process)
            if inspector is not None:
                try:
                    inspector.evaluate("setTimeout(() => process.getBuiltinModule('inspector').close(), 0); true")
                finally:
                    inspector.close()
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline and inspector_is_owned(process):
                time.sleep(0.01)
            if inspector_is_owned(process):
                raise RuntimeError('INSPECTOR_NOT_CLOSED')
            time.sleep(0.02)
        finally:
            finished.set()
            watcher.join()
        if observed[0] is None or observed[1] is None:
            raise RuntimeError('INSPECTOR_MEASUREMENT_MISSING')
        metrics.update({'inspectorClosed': True,
                        'inspectorOpenMs': round((observed[1] - observed[0]) * 1000, 2),
                        'observationIntervalMs': 5,
                        'bootstrapElapsedMs': round((time.monotonic() - begin) * 1000, 2)})


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--helper', help=argparse.SUPPRESS)
    parser.add_argument('--discover', action='store_true')
    parser.add_argument('--bundle')
    args = parser.parse_args()
    processes = [process for process in psutil.process_iter(['exe', 'create_time'])
                 if process.info['exe'] and process.info['exe'].endswith('/ChatGPT.app/Contents/MacOS/ChatGPT')]
    if len(processes) != 1:
        raise RuntimeError('PROCESS_NOT_UNIQUE')
    process = processes[0]
    executable = process.exe()
    contents = Path(executable).parent.parent
    with (contents / 'Info.plist').open('rb') as source:
        info = plistlib.load(source)
    identity = {'pid': process.pid, 'startedAt': process.create_time(), 'executablePath': executable,
                'appVersion': info['CFBundleShortVersionString'], 'asarPath': str(contents / 'Resources/app.asar')}
    if args.discover:
        print(json.dumps(identity))
        return
    options = json.load(sys.stdin)
    if options['pid'] != process.pid or options['startedAt'] != process.create_time():
        raise RuntimeError('PROCESS_CHANGED')
    if inspector_is_owned(process):
        raise RuntimeError('INSPECTOR_ALREADY_OPEN')
    before = {item.laddr.port for item in process.net_connections(kind='tcp')
              if item.status == psutil.CONN_LISTEN}
    metrics = {}
    with transient_inspector(process, metrics) as inspector:
        filename = json.dumps(str(Path(args.bundle).resolve()))
        expression = 'delete process.getBuiltinModule("module")._cache[' + filename + ']; process.mainModule.require(' + filename + ').install(' + json.dumps(options) + '); true'
        if inspector.evaluate(expression) is not True:
            raise RuntimeError('INSTALL_REJECTED')
    deadline = time.monotonic() + 5
    while True:
        ports = {item.laddr.port for item in process.net_connections(kind='tcp')
                 if item.status == psutil.CONN_LISTEN} - before
        if len(ports) == 1:
            break
        if time.monotonic() > deadline:
            raise RuntimeError('IPC_LISTENER_NOT_UNIQUE')
        time.sleep(0.01)
    print(json.dumps({**identity, **metrics, 'url': f'ws://127.0.0.1:{ports.pop()}/internal'}))


if __name__ == '__main__':
    main()
