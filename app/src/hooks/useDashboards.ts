import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { api, unwrap, type Panel } from '@/api/client';
import { useSession } from '@/auth/session';
import { createLimiter, retryOn429 } from '@/lib/limiter';
import { resolveRange, type TimeRange } from '@/lib/time';

// Every panel of a dashboard queries at once; keep a few in flight so we stay under the
// server's per-user concurrency cap, and retry the occasional 429 from other open tabs.
const panelQueries = createLimiter(3);

export type PanelType = 'logs' | 'timeseries' | 'top_n' | 'stat';

export interface PanelDraft {
  title: string;
  type: PanelType;
  query: string;
  step: string;
  options: Record<string, unknown>;
  position?: number;
}

export function useCanEdit(): boolean {
  const role = useSession((s) => s.user?.role);
  return role === 'editor' || role === 'admin';
}

function useSignedIn() {
  return useSession((s) => s.status === 'signedIn');
}

export function useDashboards() {
  const on = useSignedIn();
  return useQuery({ queryKey: ['dashboards'], enabled: on, queryFn: async () => unwrap(await api.GET('/dashboards')) });
}

export function useDashboard(id: string) {
  const on = useSignedIn();
  return useQuery({
    queryKey: ['dashboard', id],
    enabled: on && !!id,
    queryFn: async () => unwrap(await api.GET('/dashboards/{id}', { params: { path: { id } } })),
  });
}

export function useSavedQueries() {
  const on = useSignedIn();
  return useQuery({ queryKey: ['saved-queries'], enabled: on, queryFn: async () => unwrap(await api.GET('/saved-queries')) });
}

export function useDashboardMutations(dashboardId?: string) {
  const qc = useQueryClient();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['dashboards'] });
    if (dashboardId) void qc.invalidateQueries({ queryKey: ['dashboard', dashboardId] });
  };
  return {
    createDashboard: useMutation({
      mutationFn: async (title: string) => unwrap(await api.POST('/dashboards', { body: { title } })),
      onSuccess: refresh,
    }),
    renameDashboard: useMutation({
      mutationFn: async (v: { id: string; title: string; description?: string; layout?: Record<string, unknown> }) =>
        unwrap(await api.PUT('/dashboards/{id}', { params: { path: { id: v.id } }, body: { title: v.title, description: v.description, layout: v.layout } })),
      onSuccess: refresh,
    }),
    deleteDashboard: useMutation({
      mutationFn: async (id: string) => {
        const r = await api.DELETE('/dashboards/{id}', { params: { path: { id } } });
        if (r.error !== undefined) unwrap(r as never);
      },
      onSuccess: refresh,
    }),
    addPanel: useMutation({
      mutationFn: async (v: { dashboardId: string; draft: PanelDraft }) =>
        unwrap(await api.POST('/dashboards/{id}/panels', { params: { path: { id: v.dashboardId } }, body: v.draft })),
      onSuccess: refresh,
    }),
    updatePanel: useMutation({
      mutationFn: async (v: { dashboardId: string; panelId: string; draft: PanelDraft }) =>
        unwrap(await api.PUT('/dashboards/{id}/panels/{panelId}', { params: { path: { id: v.dashboardId, panelId: v.panelId } }, body: v.draft })),
      onSuccess: refresh,
    }),
    deletePanel: useMutation({
      mutationFn: async (v: { dashboardId: string; panelId: string }) => {
        const r = await api.DELETE('/dashboards/{id}/panels/{panelId}', { params: { path: { id: v.dashboardId, panelId: v.panelId } } });
        if (r.error !== undefined) unwrap(r as never);
      },
      onSuccess: refresh,
    }),
    saveQuery: useMutation({
      mutationFn: async (v: { name: string; query: string; default_range: string }) => unwrap(await api.POST('/saved-queries', { body: v })),
      onSuccess: () => void qc.invalidateQueries({ queryKey: ['saved-queries'] }),
    }),
  };
}

export function panelDraft(p: Panel): PanelDraft {
  return { title: p.title, type: p.type, query: p.query, step: p.step, options: (p.options ?? {}) as Record<string, unknown>, position: p.position };
}

export const rangeKey = (r: TimeRange) => (r.kind === 'preset' ? r.id : `${r.start}|${r.end}`);

export const REFRESH_OPTIONS = [
  { id: 'off', label: 'Off', ms: 0 },
  { id: '15s', label: '15s', ms: 15_000 },
  { id: '30s', label: '30s', ms: 30_000 },
  { id: '1m', label: '1m', ms: 60_000 },
  { id: '5m', label: '5m', ms: 300_000 },
] as const;

/**
 * Data for one panel over the shared time range. Metric panels use /query/metric
 * (the server caches per step), logs panels the newest 50 lines. While a refetch is
 * in flight the previous data stays on screen.
 */
export function usePanelData(panel: Panel, range: TimeRange, refreshMs: number) {
  const on = useSignedIn();
  return useQuery({
    queryKey: ['panel', panel.id, panel.type, panel.query, panel.step, rangeKey(range)],
    enabled: on,
    retry: false,
    placeholderData: keepPreviousData,
    refetchInterval: refreshMs > 0 ? refreshMs : false,
    queryFn: () => panelQueries(() => retryOn429(() => fetchPanel(panel, range))),
  });
}

async function fetchPanel(panel: Panel, range: TimeRange) {
  const r = resolveRange(range);
  if ('error' in r) throw new Error(r.error);
  if (panel.type === 'logs') {
    const res = unwrap(await api.GET('/query/range', { params: { query: { query: panel.query, start: r.start, end: r.end, direction: 'backward', limit: 50 } } }));
    return { kind: 'logs' as const, logs: res };
  }
  const res = unwrap(await api.GET('/query/metric', { params: { query: { query: panel.query, start: r.start, end: r.end, ...(panel.step ? { step: panel.step } : {}) } } }));
  return { kind: 'metric' as const, metric: res };
}
