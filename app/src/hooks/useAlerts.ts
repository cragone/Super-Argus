import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { api, unwrap, type AlertRuleInput, type ChannelInput } from '@/api/client';
import { useSession } from '@/auth/session';

const REFETCH_MS = 15_000;

function useSignedIn() {
  return useSession((s) => s.status === 'signedIn');
}

export function useIsAdmin(): boolean {
  return useSession((s) => s.user?.role) === 'admin';
}

/** Rules with live state; refreshed on the ruler's cadence. */
export function useAlertRules() {
  const on = useSignedIn();
  return useQuery({
    queryKey: ['alert-rules'],
    enabled: on,
    refetchInterval: REFETCH_MS,
    queryFn: async () => unwrap(await api.GET('/alert-rules')),
  });
}

export function useAlertEvents(ruleId?: string) {
  const on = useSignedIn();
  return useQuery({
    queryKey: ['alert-events', ruleId ?? 'all'],
    enabled: on,
    refetchInterval: REFETCH_MS,
    queryFn: async () => unwrap(await api.GET('/alert-events', { params: { query: { limit: 50, ...(ruleId ? { rule_id: ruleId } : {}) } } })),
  });
}

/** Channels are readable by editors and above; viewers get a 403, so skip the call. */
export function useChannels(enabled = true) {
  const on = useSignedIn();
  return useQuery({ queryKey: ['channels'], enabled: on && enabled, queryFn: async () => unwrap(await api.GET('/channels')) });
}

export function useAlertMutations() {
  const qc = useQueryClient();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['alert-rules'] });
    void qc.invalidateQueries({ queryKey: ['alert-events'] });
  };
  const refreshChannels = () => void qc.invalidateQueries({ queryKey: ['channels'] });
  return {
    createRule: useMutation({
      mutationFn: async (body: AlertRuleInput) => unwrap(await api.POST('/alert-rules', { body })),
      onSuccess: refresh,
    }),
    updateRule: useMutation({
      mutationFn: async (v: { id: string; body: AlertRuleInput }) =>
        unwrap(await api.PUT('/alert-rules/{id}', { params: { path: { id: v.id } }, body: v.body })),
      onSuccess: refresh,
    }),
    deleteRule: useMutation({
      mutationFn: async (id: string) => {
        const r = await api.DELETE('/alert-rules/{id}', { params: { path: { id } } });
        if (r.error !== undefined) unwrap(r as never);
      },
      onSuccess: refresh,
    }),
    createChannel: useMutation({
      mutationFn: async (body: ChannelInput) => unwrap(await api.POST('/channels', { body })),
      onSuccess: refreshChannels,
    }),
    deleteChannel: useMutation({
      mutationFn: async (id: string) => {
        const r = await api.DELETE('/channels/{id}', { params: { path: { id } } });
        if (r.error !== undefined) unwrap(r as never);
      },
      onSuccess: refreshChannels,
    }),
    testChannel: useMutation({
      mutationFn: async (id: string) => unwrap(await api.POST('/channels/{id}/test', { params: { path: { id } } })),
    }),
  };
}
