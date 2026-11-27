import argparse
import configparser
import ctypes
from ctypes import wintypes
import hashlib
import json
from pathlib import Path
import sys
import threading
import time
import xml.etree.ElementTree as ET

import frida
import psutil

sys.dont_write_bytecode = True
VALIDATED_VERSION = '26.1117.41037'
VALIDATED_CHROME_SHA256 = '0db43d9385011630242c68e95c5fe7534e8a71a23e81f06a3be7a83185cc8022'


def session_id(pid):
    value = wintypes.DWORD()
    if not ctypes.windll.kernel32.ProcessIdToSessionId(pid, ctypes.byref(value)):
        raise ctypes.WinError()
    return value.value


def discover(only_pid=None):
    owner = psutil.Process().username().casefold()
    own_session = session_id(psutil.Process().pid)
    if own_session == 0:
        raise RuntimeError('INTERACTIVE_SESSION_REQUIRED')
    candidates = []
    attributes = ['exe', 'cmdline', 'create_time']
    processes = psutil.process_iter(attributes) if only_pid is None else [psutil.Process(only_pid)]
    for item in processes:
        details = item.info if only_pid is None else item.as_dict(attributes)
        if (not details['exe'] or Path(details['exe']).name.lower() != 'chatgpt.exe'
                or any(arg.startswith('--type=') for arg in details['cmdline'] or [])):
            continue
        if session_id(item.pid) == own_session and item.username().casefold() == owner:
            candidates.append(item)
    if len(candidates) != 1:
        raise RuntimeError('MAIN_PROCESS_NOT_UNIQUE')
    process = candidates[0]
    if session_id(process.pid) != own_session:
        raise RuntimeError('INTERACTIVE_SESSION_REQUIRED')
    directory = Path(process.exe()).parent
    manifest = ET.parse(directory.parent / 'AppxManifest.xml')
    namespace = {'m': 'http://schemas.microsoft.com/appx/manifest/foundation/windows10'}
    if manifest.find('m:Identity', namespace).attrib['Name'] != 'OpenAI.Codex':
        raise RuntimeError('PACKAGE_IDENTITY_INVALID')
    ini = configparser.ConfigParser()
    if not ini.read(directory / 'resources' / 'owl-app.ini'):
        raise RuntimeError('APP_VERSION_MISSING')
    versions = [value for section in ini.values() for key, value in section.items() if key == 'appversion']
    if len(versions) != 1:
        raise RuntimeError('APP_VERSION_NOT_UNIQUE')
    asar = directory / 'resources' / 'app.asar'
    if not asar.is_file():
        raise RuntimeError('ASAR_MISSING')
    return process, {'pid': process.pid, 'startedAt': process.create_time(), 'executablePath': process.exe(),
                     'appVersion': versions[0], 'asarPath': str(asar)}


def listeners(process):
    return {item.laddr.port for item in process.net_connections(kind='tcp') if item.status == psutil.CONN_LISTEN}


def execute(process, expression):
    completed = threading.Event()
    result = {}

    def receive(message, data):
        if message['type'] == 'error':
            result.update({'failure': 'FRIDA_AGENT_ERROR'})
            completed.set()
        elif message['type'] == 'send' and message['payload'].get('kind') == 'complete':
            result.update(message['payload'])
            completed.set()

    session = frida.attach(process.pid)
    try:
        source = Path(__file__).with_suffix('.js').read_text().replace('BOOTSTRAP_SOURCE', json.dumps(expression))
        script = session.create_script(source)
        script.on('message', receive)
        script.load()
        if not completed.wait(20):
            script.exports_sync.cancel()
            if not completed.wait(20):
                raise RuntimeError('MAIN_CALLBACK_CLEANUP_TIMEOUT')
        script.unload()
        if result.get('failure') or not result.get('executed') or not result.get('asyncClosed'):
            raise RuntimeError(result.get('failure') or 'NATIVE_EXECUTION_UNCONFIRMED')
    finally:
        session.detach()
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--discover', action='store_true')
    parser.add_argument('--bundle')
    parser.add_argument('--pid', type=int)
    args = parser.parse_args()
    process, identity = discover(args.pid)
    if args.discover:
        print(json.dumps(identity))
        return
    options = json.load(sys.stdin)
    if identity['appVersion'] != VALIDATED_VERSION or options['appVersion'] != VALIDATED_VERSION:
        raise RuntimeError('APP_VERSION_UNVERIFIED')
    if options['pid'] != identity['pid'] or options['startedAt'] != identity['startedAt']:
        raise RuntimeError('PROCESS_CHANGED')
    with (Path(identity['executablePath']).parent / 'chrome.dll').open('rb') as binary:
        if hashlib.file_digest(binary, 'sha256').hexdigest() != VALIDATED_CHROME_SHA256:
            raise RuntimeError('RUNTIME_ABI_UNVERIFIED')
    before = listeners(process)
    if 9229 in before:
        raise RuntimeError('INSPECTOR_ALREADY_OPEN')
    bundle = json.dumps(str(Path(args.bundle).resolve()))
    expression = ('if(process.type!=="browser")throw new Error("MAIN_RUNTIME_INVALID");'
                  'delete process.getBuiltinModule("module")._cache[' + bundle + '];'
                  'process.mainModule.require(' + bundle + ').install(' + json.dumps(options) + ');true')
    begin = time.monotonic()
    execute(process, expression)
    deadline = time.monotonic() + 5
    while True:
        ports = listeners(process) - before
        if len(ports) == 1:
            break
        if time.monotonic() > deadline:
            raise RuntimeError('IPC_LISTENER_NOT_UNIQUE')
        time.sleep(0.02)
    if 9229 in listeners(process) or process.create_time() != identity['startedAt']:
        raise RuntimeError('FINAL_IDENTITY_CHECK_FAILED')
    print(json.dumps({**identity, 'url': f'ws://127.0.0.1:{ports.pop()}/internal', 'inspectorClosed': True,
                      'inspectorOpenMs': 0, 'bootstrapElapsedMs': round((time.monotonic() - begin) * 1000, 2)}))


if __name__ == '__main__':
    main()
