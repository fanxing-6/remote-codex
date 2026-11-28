import { maxFrame, maxQueue } from '../shared';

export class BridgeReplay {
  sent = 0;
  received = 0;
  private bytes = 0;
  private frames = new Map<number, Uint8Array>();

  enqueue(payload: Uint8Array) {
    if (
      payload.byteLength > maxFrame ||
      this.bytes + payload.byteLength + 4 > maxQueue ||
      this.sent >= 0xffffffff
    )
      throw new Error('QUEUE_LIMIT');
    const frame = new Uint8Array(payload.byteLength + 4);
    new DataView(frame.buffer).setUint32(0, ++this.sent);
    frame.set(payload, 4);
    this.frames.set(this.sent, frame);
    this.bytes += frame.byteLength;
    return frame;
  }

  acknowledge(sequence: number) {
    if (!Number.isInteger(sequence) || sequence < 0 || sequence > this.sent)
      throw new Error('ACK_INVALID');
    for (const [id, frame] of this.frames) {
      if (id > sequence) break;
      this.bytes -= frame.byteLength;
      this.frames.delete(id);
    }
  }

  replay() {
    return this.frames.values();
  }

  receive(frame: ArrayBuffer, deliver: (payload: Uint8Array) => void) {
    if (frame.byteLength < 5 || frame.byteLength > maxFrame + 4)
      throw new Error('FRAME_INVALID');
    const sequence = new DataView(frame).getUint32(0);
    if (!sequence || sequence > this.received + 1)
      throw new Error('SEQUENCE_INVALID');
    if (sequence <= this.received) return;
    deliver(new Uint8Array(frame, 4));
    this.received = sequence;
  }
}
