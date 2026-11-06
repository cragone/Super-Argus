import { useState } from 'react';
import { ScrollView, Text, View } from 'react-native';

import { errorMessage, type AlertRule } from '@/api/client';
import { AlertRuleForm } from '@/components/AlertRuleForm';
import { Button, Card, Chip, ErrorText } from '@/components/ui';
import { useAlertEvents, useAlertMutations, useAlertRules, useChannels } from '@/hooks/useAlerts';
import { useCanEdit } from '@/hooks/useDashboards';
import { describeCondition, formatValue, stateColor, type AlertState } from '@/lib/alerts';
import { mono, useTheme } from '@/theme';

function StateBadge({ state }: { state: AlertState | 'resolved' }) {
  const t = useTheme();
  const c = stateColor(state, t);
  return (
    <View accessibilityLabel={`State ${state}`} style={{ borderColor: c, borderWidth: 1, borderRadius: 10, paddingHorizontal: 8, paddingVertical: 2 }}>
      <Text style={{ color: c, fontSize: 12, fontWeight: '700', textTransform: 'uppercase' }}>{state}</Text>
    </View>
  );
}

function RuleCard({ rule, canEdit, onEdit }: { rule: AlertRule; canEdit: boolean; onEdit: () => void }) {
  const t = useTheme();
  const { deleteRule, updateRule } = useAlertMutations();
  const [error, setError] = useState<string | null>(null);
  const toggle = () =>
    updateRule.mutate(
      {
        id: rule.id,
        body: {
          name: rule.name, query: rule.query, comparator: rule.comparator, threshold: rule.threshold, window: rule.window,
          for_duration: rule.for_duration, cooldown: rule.cooldown, severity: rule.severity, enabled: !rule.enabled,
          notify_channel_ids: rule.notify_channel_ids,
        },
      },
      { onError: (e) => setError(errorMessage(e, 'Could not update the rule')) },
    );
  return (
    <Card style={{ gap: 6, opacity: rule.enabled ? 1 : 0.6 }}>
      <View style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
        <Text style={{ color: t.text, fontSize: 16, fontWeight: '600', flex: 1 }} numberOfLines={1}>
          {rule.name}
        </Text>
        <StateBadge state={rule.enabled ? rule.state.state : 'ok'} />
      </View>
      <Text style={[mono, { color: t.textDim, fontSize: 12 }]} numberOfLines={2}>
        {rule.query}
      </Text>
      <Text style={{ color: t.textDim, fontSize: 12 }}>
        {describeCondition(rule)} · {rule.severity}
        {rule.enabled ? '' : ' · disabled'}
      </Text>
      {rule.enabled && rule.state.last_eval && (
        <Text style={{ color: t.textDim, fontSize: 12 }}>Last value {formatValue(rule.state.last_value)}</Text>
      )}
      {rule.state.last_error ? <ErrorText>{rule.state.last_error}</ErrorText> : null}
      {error && <ErrorText>{error}</ErrorText>}
      {canEdit && (
        <View style={{ flexDirection: 'row', gap: 8, flexWrap: 'wrap' }}>
          <Chip label="Edit" accessibilityLabel={`Edit ${rule.name}`} onPress={onEdit} />
          <Chip label={rule.enabled ? 'Disable' : 'Enable'} accessibilityLabel={`${rule.enabled ? 'Disable' : 'Enable'} ${rule.name}`} onPress={toggle} />
          <Chip
            label="Delete"
            accessibilityLabel={`Delete ${rule.name}`}
            onPress={() => deleteRule.mutate(rule.id, { onError: (e) => setError(errorMessage(e, 'Could not delete the rule')) })}
          />
        </View>
      )}
    </Card>
  );
}

export default function AlertsScreen() {
  const t = useTheme();
  const canEdit = useCanEdit();
  const rules = useAlertRules();
  const events = useAlertEvents();
  const channels = useChannels(canEdit);
  const { createRule, updateRule } = useAlertMutations();
  const [editing, setEditing] = useState<AlertRule | 'new' | null>(null);

  const chans = channels.data ?? [];
  return (
    <ScrollView style={{ flex: 1, backgroundColor: t.bg }} contentContainerStyle={{ padding: 16, gap: 12 }}>
      {canEdit && editing === null && <Button title="New alert rule" onPress={() => setEditing('new')} />}
      {editing === 'new' && (
        <AlertRuleForm channels={chans} submitLabel="Create rule" onCancel={() => setEditing(null)} onSubmit={async (body) => { await createRule.mutateAsync(body); setEditing(null); }} />
      )}
      {rules.isError && <ErrorText>{errorMessage(rules.error, 'Could not load alert rules')}</ErrorText>}
      {rules.data?.length === 0 && editing === null && <Text style={{ color: t.textDim }}>No alert rules yet.</Text>}
      {rules.data?.map((r) =>
        typeof editing === 'object' && editing?.id === r.id ? (
          <AlertRuleForm
            key={r.id}
            initial={r}
            channels={chans}
            submitLabel="Save rule"
            onCancel={() => setEditing(null)}
            onSubmit={async (body) => { await updateRule.mutateAsync({ id: r.id, body }); setEditing(null); }}
          />
        ) : (
          <RuleCard key={r.id} rule={r} canEdit={canEdit} onEdit={() => setEditing(r)} />
        ),
      )}

      <Text style={{ color: t.text, fontSize: 16, fontWeight: '700', marginTop: 8 }}>History</Text>
      {events.data?.length === 0 && <Text style={{ color: t.textDim }}>No alert activity yet.</Text>}
      {events.data?.map((e) => (
        <Card key={e.id} style={{ gap: 4 }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
            <StateBadge state={e.state} />
            <Text style={{ color: t.text, flex: 1 }} numberOfLines={1}>
              {e.rule_name || 'Deleted rule'}
            </Text>
          </View>
          <Text style={{ color: t.textDim, fontSize: 12 }}>
            {new Date(e.at).toLocaleString()} · value {formatValue(e.value)}
            {e.notified ? ' · notified' : ''}
          </Text>
          {e.notify_error ? <ErrorText>{e.notify_error}</ErrorText> : null}
        </Card>
      ))}
    </ScrollView>
  );
}
