import { useQuery } from '@tanstack/react-query';

import { api, unwrap } from '@/api/client';
import { useSession } from '@/auth/session';

/** Label names for the browser and autocomplete. */
export function useLabelNames() {
  const signedIn = useSession((s) => s.status === 'signedIn');
  return useQuery({
    queryKey: ['labels'],
    enabled: signedIn,
    staleTime: 30_000,
    queryFn: async () => unwrap(await api.GET('/labels')).data,
  });
}

/** Values of one label; disabled while name is null. */
export function useLabelValues(name: string | null) {
  const signedIn = useSession((s) => s.status === 'signedIn');
  return useQuery({
    queryKey: ['labels', name, 'values'],
    enabled: signedIn && !!name,
    staleTime: 30_000,
    queryFn: async () =>
      unwrap(await api.GET('/labels/{name}/values', { params: { path: { name: name as string } } })).data,
  });
}

export interface SubmittedQuery {
  query: string;
  start: string;
  end: string;
  direction: 'backward' | 'forward';
  limit: number;
  /** Changes on every Run so the same query can be re-run. */
  nonce: number;
}

export function useRangeQuery(submitted: SubmittedQuery | null) {
  return useQuery({
    queryKey: ['range', submitted],
    enabled: submitted !== null,
    staleTime: Infinity,
    retry: false,
    queryFn: async () => {
      const q = submitted as SubmittedQuery;
      return unwrap(
        await api.GET('/query/range', {
          params: { query: { query: q.query, start: q.start, end: q.end, direction: q.direction, limit: q.limit } },
        }),
      );
    },
  });
}

/** Services (values of the `service` label) with activity and volume; polls so new ones appear. */
export function useServices(range?: { start: string; end: string }) {
  const signedIn = useSession((s) => s.status === 'signedIn');
  return useQuery({
    queryKey: ['services', range ?? null],
    enabled: signedIn,
    staleTime: 5_000,
    refetchInterval: 10_000,
    queryFn: async () =>
      unwrap(await api.GET('/services', { params: { query: range ? { start: range.start, end: range.end } : {} } })).data,
  });
}
