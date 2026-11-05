import { useState } from 'react';
import { ScrollView, Text, TextInput, View } from 'react-native';

import { errorMessage, type SavedQuery } from '@/api/client';
import { PanelForm } from '@/components/PanelForm';
import { Button, Chip, ErrorText } from '@/components/ui';
import { useCanEdit, useDashboardMutations, useDashboards, useSavedQueries, type PanelType } from '@/hooks/useDashboards';
import { RANGE_PRESETS, type TimeRange } from '@/lib/time';
import { useTheme } from '@/theme';

/** A query is a metric query when it starts with a function name rather than a selector. */
const isMetric = (q: string) => /^\s*[a-z_]/i.test(q);

/**
 * Explore actions: load a saved query (everyone), save the current query or add it
 * to a dashboard as a panel (editors and admins).
 */
export function SaveBar({
  query,
  range,
  onLoad,
}: {
  query: string;
  range: TimeRange;
  onLoad: (q: SavedQuery) => void;
}) {
  const t = useTheme();
  const canEdit = useCanEdit();
  const saved = useSavedQueries();
  const dashboards = useDashboards();
  const m = useDashboardMutations();
  const [mode, setMode] = useState<'none' | 'save' | 'dashboard'>('none');
  const [name, setName] = useState('');
  const [target, setTarget] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const presetId = range.kind === 'preset' && RANGE_PRESETS.some((p) => p.id === range.id) ? range.id : '1h';

  const save = async () => {
    setError(null);
    if (!query.trim()) return setError('Write a query first');
    if (!name.trim()) return setError('Give the query a name');
    try {
      await m.saveQuery.mutateAsync({ name: name.trim(), query: query.trim(), default_range: presetId });
      setMessage(`Saved "${name.trim()}"`);
      setName('');
      setMode('none');
    } catch (e) {
      setError(errorMessage(e, 'Could not save the query'));
    }
  };

  const dashId = target ?? dashboards.data?.[0]?.id ?? null;
  const initialType: PanelType = isMetric(query) ? 'timeseries' : 'logs';

  return (
    <View style={{ gap: 8 }}>
      {!!saved.data?.length && (
        <ScrollView horizontal showsHorizontalScrollIndicator={false} accessibilityLabel="Saved queries">
          <Text style={{ color: t.textDim, fontSize: 13, alignSelf: 'center', marginRight: 6 }}>Saved</Text>
          {saved.data.map((q) => (
            <Chip key={q.id} label={q.name} accessibilityLabel={`Load saved query ${q.name}`} onPress={() => onLoad(q)} />
          ))}
        </ScrollView>
      )}
      {canEdit && (
        <View style={{ flexDirection: 'row', gap: 6 }}>
          <Chip label="Save query" selected={mode === 'save'} onPress={() => { setMode(mode === 'save' ? 'none' : 'save'); setMessage(null); }} />
          <Chip label="Add to dashboard" selected={mode === 'dashboard'} onPress={() => { setMode(mode === 'dashboard' ? 'none' : 'dashboard'); setMessage(null); }} />
        </View>
      )}
      {message && <Text accessibilityLabel="Save status" style={{ color: t.levels.info, fontSize: 13 }}>{message}</Text>}

      {mode === 'save' && (
        <View style={{ gap: 8 }}>
          <TextInput
            accessibilityLabel="Saved query name"
            placeholder="Name, e.g. API errors"
            placeholderTextColor={t.textDim}
            value={name}
            onChangeText={setName}
            onSubmitEditing={() => void save()}
            style={{ color: t.text, backgroundColor: t.surface, borderColor: t.border, borderWidth: 1, borderRadius: 8, paddingHorizontal: 10, paddingVertical: 8, fontSize: 14 }}
          />
          {error && <ErrorText>{error}</ErrorText>}
          <Button title="Save" onPress={() => void save()} loading={m.saveQuery.isPending} />
        </View>
      )}

      {mode === 'dashboard' &&
        (dashboards.data?.length ? (
          <View style={{ gap: 8 }}>
            <ScrollView horizontal showsHorizontalScrollIndicator={false}>
              {dashboards.data.map((d) => (
                <Chip key={d.id} label={d.title} selected={dashId === d.id} accessibilityLabel={`Dashboard ${d.title}`} onPress={() => setTarget(d.id)} />
              ))}
            </ScrollView>
            <PanelForm
              key={`${initialType}-${query}`}
              initial={{ query, type: initialType, title: '' }}
              submitLabel="Add panel"
              onCancel={() => setMode('none')}
              onSubmit={async (draft) => {
                if (!dashId) throw new Error('Pick a dashboard');
                await m.addPanel.mutateAsync({ dashboardId: dashId, draft });
                setMessage(`Added "${draft.title}" to ${dashboards.data?.find((d) => d.id === dashId)?.title}`);
                setMode('none');
              }}
            />
          </View>
        ) : (
          <Text style={{ color: t.textDim, fontSize: 13 }}>Create a dashboard first (Dashboards tab).</Text>
        ))}
    </View>
  );
}
