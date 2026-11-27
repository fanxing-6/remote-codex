import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time

sys.dont_write_bytecode = True
import psutil


def listeners(process):
    return {item.laddr.port for item in process.net_connections(kind='tcp')
            if item.status == psutil.CONN_LISTEN}


def main():
    project = Path(__file__).resolve().parents[2]
    work = project / '.work/crash'
    work.mkdir(mode=0o700, parents=True, exist_ok=True)
    config = work / 'config.json'
    shutil.copyfile(project / '.work/e2e/config.json', config)
    config.chmod(0o600)
    binary = os.environ.get('CWB_BINARY', str(project / 'bin/remote-codex'))
    candidates = [process for process in psutil.process_iter(['exe'])
                  if process.info['exe'] == '/Applications/ChatGPT.app/Contents/MacOS/ChatGPT']
    if len(candidates) != 1:
        raise RuntimeError('PROCESS_NOT_UNIQUE')
    host = candidates[0]
    identity = host.create_time()
    before = listeners(host)
    if 9229 in before:
        raise RuntimeError('INSPECTOR_ALREADY_OPEN')
    gateway = subprocess.Popen([binary, 'serve', '--no-tray', '--config', str(config)],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        deadline = time.monotonic() + 80
        while not (work / 'runtime/cli-session.json').exists():
            if gateway.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError('GATEWAY_START_FAILED')
            time.sleep(0.1)
        while True:
            result = subprocess.run([binary, 'status', '--config', str(config)],
                                    capture_output=True, check=True, timeout=90)
            if json.loads(result.stdout)['shimConnected']:
                break
            if time.monotonic() > deadline:
                raise RuntimeError('HOST_ATTACH_TIMEOUT')
            time.sleep(0.2)
        owned = listeners(host) - before
        if len(owned) != 1:
            raise RuntimeError('IPC_LISTENER_NOT_UNIQUE')
        begin = time.monotonic()
        gateway.kill()
        gateway.wait(timeout=10)
        while owned & listeners(host):
            if time.monotonic() - begin > 75:
                raise RuntimeError('CRASH_GRACE_NOT_RELEASED')
            time.sleep(0.5)
        if not host.is_running() or host.create_time() != identity or 9229 in listeners(host):
            raise RuntimeError('HOST_IDENTITY_OR_INSPECTOR_CHANGED')
        report = {'gatewayKilled': True, 'ipcReleased': True, 'inspectorClosed': True,
                  'hostProcessPreserved': True,
                  'graceCleanupMs': round((time.monotonic() - begin) * 1000, 2)}
        (work / 'report.json').write_text(json.dumps(report, indent=2))
        print(json.dumps(report))
    finally:
        if gateway.poll() is None:
            gateway.terminate()
            gateway.wait(timeout=100)
        for name in ['cli-session.json', 'bootstrap.code']:
            (work / 'runtime' / name).unlink(missing_ok=True)


if __name__ == '__main__':
    main()
