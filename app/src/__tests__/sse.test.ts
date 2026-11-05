import { SSEParser } from '@/lib/sse';

describe('SSEParser', () => {
  it('parses complete events', () => {
    const p = new SSEParser();
    expect(p.push('event: ready\ndata: {}\n\nevent: entry\ndata: {"a":1}\n\n')).toEqual([
      { event: 'ready', data: '{}' },
      { event: 'entry', data: '{"a":1}' },
    ]);
  });

  it('handles events split across arbitrary chunk boundaries', () => {
    const p = new SSEParser();
    const text = 'event: entry\ndata: {"line":"héllo"}\n\n';
    const out = [];
    for (const ch of text) out.push(...p.push(ch)); // one character at a time
    expect(out).toEqual([{ event: 'entry', data: '{"line":"héllo"}' }]);
  });

  it('ignores heartbeat comments', () => {
    const p = new SSEParser();
    expect(p.push(': ping\n\n')).toEqual([]);
    expect(p.push(': ping\nevent: dropped\ndata: {"count":3}\n\n')).toEqual([{ event: 'dropped', data: '{"count":3}' }]);
  });

  it('joins multi-line data and defaults the event name', () => {
    expect(new SSEParser().push('data: a\ndata: b\n\n')).toEqual([{ event: 'message', data: 'a\nb' }]);
  });

  it('accepts CRLF line endings, even split between chunks', () => {
    const p = new SSEParser();
    expect(p.push('event: ready\r')).toEqual([]);
    expect(p.push('\ndata: {}\r\n\r\n')).toEqual([{ event: 'ready', data: '{}' }]);
  });

  it('holds incomplete events until the blank line', () => {
    const p = new SSEParser();
    expect(p.push('event: entry\ndata: 1')).toEqual([]);
    expect(p.push('\n')).toEqual([]);
    expect(p.push('\n')).toEqual([{ event: 'entry', data: '1' }]);
  });
});
