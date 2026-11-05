import { fetch as streamingFetch } from 'expo/fetch';
import { useCallback, useEffect, useRef, useState } from 'react';

import { apiUrl } from '@/api/client';
import { refreshAccessToken, useSession } from '@/auth/session';
import { TailConnection, type TailEntry, type TailResponse, type TailStatus } from '@/lib/tailConnection';

/** Lines kept in memory; older ones are discarded. */
export const MAX_TAIL_LINES = 2000;
const FLUSH_MS = 100;

export interface TailLine extends TailEntry {
  /** Unique, increasing; used as the list key. */
  id: number;
}

export interface TailState {
  status: TailStatus;
  lines: TailLine[];
  droppedLines: number;
  error: string | null;
  paused: boolean;
  /** Lines received while paused and not yet shown. */
  buffered: number;
}

/** Appends and keeps only the newest `max` entries. */
export function appendCapped<T>(prev: T[], add: T[], max = MAX_TAIL_LINES): T[] {
  if (add.length === 0) return prev;
  const all = prev.length === 0 ? add : prev.concat(add);
  return all.length > max ? all.slice(all.length - max) : all;
}

/**
 * Live tail of an ArgusQL query. Incoming lines are batched every 100 ms so a busy
 * stream does not re-render per line; while paused they collect in a bounded buffer.
 */
export function useTail() {
  const [state, setState] = useState<TailState>({
    status: 'stopped', lines: [], droppedLines: 0, error: null, paused: false, buffered: 0,
  });
  const conn = useRef<TailConnection | null>(null);
  const pending = useRef<TailLine[]>([]);
  const nextId = useRef(1);
  const paused = useRef(false);
  const timer = useRef<ReturnType<typeof setInterval> | null>(null);

  const flush = useCallback(() => {
    if (paused.current) {
      setState((s) => (s.buffered === pending.current.length ? s : { ...s, buffered: pending.current.length }));
      return;
    }
    if (pending.current.length === 0) return;
    const add = pending.current;
    pending.current = [];
    setState((s) => ({ ...s, lines: appendCapped(s.lines, add), buffered: 0 }));
  }, []);

  const stop = useCallback(() => {
    conn.current?.stop();
    conn.current = null;
    if (timer.current) clearInterval(timer.current);
    timer.current = null;
    flush();
  }, [flush]);

  const start = useCallback(
    (query: string) => {
      stop();
      pending.current = [];
      paused.current = false;
      setState({ status: 'connecting', lines: [], droppedLines: 0, error: null, paused: false, buffered: 0 });

      const c = new TailConnection(
        {
          url: () => apiUrl(`/tail?query=${encodeURIComponent(query)}`),
          getToken: () => useSession.getState().accessToken,
          refresh: refreshAccessToken,
          fetch: (url, init) => streamingFetch(url, init as never) as unknown as Promise<TailResponse>,
          sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
        },
        {
          onStatus: (status) => setState((s) => ({ ...s, status, error: status === 'live' ? null : s.error })),
          onEntry: (e) => {
            pending.current.push({ ...e, id: nextId.current++ });
            if (pending.current.length > MAX_TAIL_LINES) pending.current.splice(0, pending.current.length - MAX_TAIL_LINES);
          },
          onDropped: (n) => setState((s) => ({ ...s, droppedLines: s.droppedLines + n })),
          onError: (message) => setState((s) => ({ ...s, error: message })),
        },
      );
      conn.current = c;
      timer.current = setInterval(flush, FLUSH_MS);
      c.start();
    },
    [stop, flush],
  );

  const setPaused = useCallback(
    (p: boolean) => {
      paused.current = p;
      setState((s) => ({ ...s, paused: p }));
      if (!p) flush();
    },
    [flush],
  );

  const clear = useCallback(() => {
    pending.current = [];
    setState((s) => ({ ...s, lines: [], buffered: 0, droppedLines: 0 }));
  }, []);

  useEffect(() => stop, [stop]); // stop when the screen unmounts

  return { ...state, start, stop, setPaused, clear };
}
