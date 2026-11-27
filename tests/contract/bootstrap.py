import argparse
import json
import secrets
from pathlib import Path
import sys
import time
import unittest

sys.dont_write_bytecode = True

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from internal.host import bootstrap as helper
from internal.host.bootstrap import transient_inspector


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('bundle')
    args = parser.parse_args()
    processes = [process for process in helper.psutil.process_iter(['exe', 'create_time'])
                 if process.info['exe'] and process.info['exe'].endswith('/ChatGPT.app/Contents/MacOS/ChatGPT')]
    if len(processes) != 1:
        raise RuntimeError('目标主进程必须唯一')
    process = processes[0]
    listeners_before = {item.laddr.port for item in process.net_connections(kind='tcp')
                        if item.status == helper.psutil.CONN_LISTEN}
    token = secrets.token_hex(32)
    options = {'token': token, 'instance': secrets.token_hex(16), 'startedAt': process.create_time()}
    if helper.inspector_is_owned(process):
        raise RuntimeError('Inspector 已由其他操作打开')
    metrics = {}
    with transient_inspector(process, metrics) as inspector:
        result = inspector.evaluate(
            'delete process.getBuiltinModule("module")._cache[' + json.dumps(str(Path(args.bundle).resolve())) + ']; process.mainModule.require(' + json.dumps(str(Path(args.bundle).resolve())) + ').install(' + json.dumps(options) + '); true')
        if result is not True:
            raise RuntimeError('Shim 安装未返回确认')
    elapsed = metrics['bootstrapElapsedMs']
    ports = {item.laddr.port for item in process.net_connections(kind='tcp')
             if item.status == helper.psutil.CONN_LISTEN} - listeners_before
    if len(ports) != 1:
        raise RuntimeError('内部监听端口必须唯一')
    port = ports.pop()
    assertion = unittest.TestCase()
    with assertion.assertRaises(helper.websocket.WebSocketBadStatusException):
        helper.websocket.create_connection(f'ws://127.0.0.1:{port}/internal',
            header={'Authorization': 'Bearer ' + secrets.token_hex(32)}, suppress_origin=True, timeout=5)
    with assertion.assertRaises(helper.websocket.WebSocketBadStatusException):
        helper.websocket.create_connection(f'ws://127.0.0.1:{port}/internal',
            header={'Authorization': 'Bearer ' + token}, origin='https://example.test', timeout=5)
    connection = helper.websocket.create_connection(f'ws://127.0.0.1:{port}/internal',
        header={'Authorization': 'Bearer ' + token}, suppress_origin=True, timeout=10)
    required = ['connect-app-host', 'codex_desktop:connect-app-server-manager']
    git_channel = 'codex_desktop:git-worker-port'
    did_stop = False
    try:
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            connection.send(json.dumps({'kind': 'probe'}))
            status = json.loads(connection.recv())
            if status['failure']:
                print(json.dumps({'failure': status['failure'], 'menu': status.get('menuSummary'), 'bootstrapElapsedMs': elapsed}))
                raise RuntimeError(status['failure'])
            channels = (status.get('result') or {}).get('channels') or {}
            if all(channels.get(name, {}).get('sent') and channels.get(name, {}).get('received') for name in required) and channels.get(git_channel, {}).get('portTransferred'):
                break
            time.sleep(1)
        connection.send_binary(b'CBOR_TRANSPORT_CHECK')
        if connection.recv() != b'CBOR_TRANSPORT_CHECK':
            raise RuntimeError('二进制帧往返失败')
        connection.send(json.dumps({'kind': 'stop'}))
        stopped = json.loads(connection.recv())
        if stopped['kind'] != 'stopped' or not stopped['windowReleased'] or not stopped['originalWindowsPreserved']:
            raise RuntimeError('停止未确认')
        did_stop = True
    finally:
        if connection.connected and not did_stop:
            connection.send(json.dumps({'kind': 'stop'}))
        connection.close()
    time.sleep(0.2)
    remaining = {item.laddr.port for item in process.net_connections(kind='tcp')
                 if item.status == helper.psutil.CONN_LISTEN}
    if port in remaining or helper.inspector_is_owned(process):
        raise RuntimeError('端口未释放')
    report = {'pid': process.pid, **metrics, 'ipcClosed': True, 'binaryRoundtrip': True,
              'originRejected': True, 'invalidTokenRejected': True, 'cleanup': stopped, 'status': status}
    print(json.dumps(report, ensure_ascii=False))
    Path('.work/phase0-report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))
    if not all(channels.get(name, {}).get('sent') and channels.get(name, {}).get('received') for name in required) or not channels.get(git_channel, {}).get('portTransferred'):
        raise RuntimeError('必需通道双向流量未通过')
    if not status['result']['bridgeContractValid']:
        raise RuntimeError('ELECTRON_BRIDGE_SIGNATURE_INVALID')


if __name__ == '__main__':
    main()
