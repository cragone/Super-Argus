export interface SSEEvent {
  event: string;
  data: string;
}

/**
 * Incremental parser for text/event-stream. Feed it decoded text in whatever
 * chunks the network delivers; it returns the events completed by each chunk.
 * Comment lines (heartbeats) are ignored.
 */
export class SSEParser {
  private buffer = '';
  private event = '';
  private data: string[] = [];

  push(chunk: string): SSEEvent[] {
    this.buffer += chunk;
    const out: SSEEvent[] = [];
    let nl: number;
    while ((nl = this.buffer.search(/\r\n|\n|\r/)) >= 0) {
      const sep = this.buffer.startsWith('\r\n', nl) ? 2 : 1;
      // A lone trailing \r may be the first half of \r\n; wait for more input.
      if (this.buffer[nl] === '\r' && nl + 1 === this.buffer.length) break;
      const line = this.buffer.slice(0, nl);
      this.buffer = this.buffer.slice(nl + sep);
      if (line === '') {
        if (this.data.length > 0 || this.event) {
          out.push({ event: this.event || 'message', data: this.data.join('\n') });
        }
        this.event = '';
        this.data = [];
      } else if (line.startsWith(':')) {
        continue;
      } else {
        const colon = line.indexOf(':');
        const field = colon < 0 ? line : line.slice(0, colon);
        let value = colon < 0 ? '' : line.slice(colon + 1);
        if (value.startsWith(' ')) value = value.slice(1);
        if (field === 'event') this.event = value;
        else if (field === 'data') this.data.push(value);
      }
    }
    return out;
  }
}
