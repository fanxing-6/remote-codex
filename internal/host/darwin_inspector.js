function instructions(framework, name, count) {
  let address = framework.getExportByName(name);
  return Array.from({length: count}, () => {
    const instruction = Instruction.parse(address);
    address = instruction.next;
    return instruction;
  });
}

function timerCallback(framework) {
  const name = '_ZN4node11Environment9RunTimersEP10uv_timer_s';
  const address = framework.getExportByName(name);
  const setup = instructions(framework, '_ZN4node11Environment13ScheduleTimerEx', 9);
  const usesTimer = setup.some(i => i.mnemonic === 'adr' && i.operands[0].value === 'x1' && ptr(i.operands[1].value).equals(address));
  const adds = setup.filter(i => i.mnemonic === 'add' && i.operands[0].value === 'x0' && i.operands[1].value === 'x0');
  const subtracts = instructions(framework, name, 12).filter(i => i.mnemonic === 'sub' && i.operands[0].value !== 'sp' && i.operands[1].value === 'x0');
  if (!usesTimer || adds.length !== 1 || subtracts.length !== 1) return null;
  const offset = Number(adds[0].operands[2].value);
  if (!Number.isSafeInteger(offset) || offset <= 0 || offset % Process.pointerSize !== 0 || offset !== Number(subtracts[0].operands[2].value)) return null;
  return {address, offset};
}

function layout() {
  if (Process.arch !== 'arm64') throw new Error('INSPECTOR_ARCH_UNSUPPORTED');
  const framework = Process.getModuleByName('Codex Framework');
  const threads = Process.enumerateThreads().filter(thread => thread.name === 'CrBrowserMain');
  if (threads.length !== 1) throw new Error('INSPECTOR_THREAD_NOT_UNIQUE');
  const wait = instructions(framework, '_ZN4node11Environment33WaitForInspectorFrontendByOptionsEv', 8);
  const load = wait[5], call = wait[6];
  const target = framework.getExportByName('_ZN4node9inspector5Agent23WaitForConnectByOptionsEv');
  if (load.mnemonic !== 'ldr' || load.operands[0].value !== 'x0' || load.operands[1].value.base !== 'x0' || call.mnemonic !== 'bl' || !ptr(call.operands[0].value).equals(target)) throw new Error('INSPECTOR_LAYOUT_UNSUPPORTED');
  const startName = '_ZN4node9inspector5Agent13StartIoThreadEv';
  const code = instructions(framework, startName, 16);
  const checks = code.filter((instruction, index) => {
    const [register, memory] = instruction.operands;
    const branch = code[index + 1], result = code[index + 2];
    return instruction.mnemonic === 'ldr' && register?.type === 'reg' && memory?.type === 'mem' && memory.value.base === 'x0' && memory.value.disp === 0x18 && branch?.mnemonic === 'cbz' && branch.operands[0].value === register.value && result?.mnemonic === 'mov' && result.operands[0].value === 'w0' && Number(result.operands[1].value) === 1;
  });
  if (checks.length !== 1) throw new Error('INSPECTOR_STATE_UNSUPPORTED');
  const callbacks = ['_ZN4node11Environment12GetNowUint64Ev', '_ZN4node11Environment27RunAndClearNativeImmediatesEb'].map(name => ({address: framework.getExportByName(name), offset: 0}));
  const timer = timerCallback(framework);
  if (timer) callbacks.push(timer);
  return {callbacks, thread: threads[0], agentOffset: load.operands[1].value.disp, ioOffset: checks[0].operands[1].value.disp, start: new NativeFunction(framework.getExportByName(startName), 'bool', ['pointer'])};
}

rpc.exports = {
  enable() {
    const {callbacks, thread, agentOffset, ioOffset, start} = layout();
    return new Promise((resolve, reject) => {
      let completed = false, starting = false;
      const listeners = [];
      const finish = (value, error) => {
        if (completed) return;
        completed = true;
        clearTimeout(timeout);
        for (const listener of listeners.splice(0)) listener.detach();
        if (error) reject(new Error(error)); else resolve(value);
      };
      const timeout = setTimeout(() => finish(null, 'INSPECTOR_THREAD_TIMEOUT'), 20000);
      for (const {address, offset} of callbacks) listeners.push(Interceptor.attach(address, {
        onEnter(args) {
          if (this.threadId === thread.id) this.environment = args[0].sub(offset);
        },
        onLeave() {
          if (completed || starting || !this.environment) return;
          const agent = this.environment.add(agentOffset).readPointer();
          if (agent.isNull() || agent.add(8).readPointer().isNull()) return;
          if (!agent.readPointer().equals(this.environment)) { finish(null, 'INSPECTOR_ENVIRONMENT_INVALID'); return; }
          const alreadyOpen = !agent.add(ioOffset).readPointer().isNull();
          starting = true;
          for (const listener of listeners.splice(0)) listener.detach();
          const enabled = alreadyOpen || start(agent);
          finish({pid: Process.id, enabled, alreadyOpen}, enabled ? null : 'INSPECTOR_START_FAILED');
        }
      }));
    });
  }
};
