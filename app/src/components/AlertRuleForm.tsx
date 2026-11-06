import { useState } from 'react';
import { Text, TextInput, View } from 'react-native';

import { errorMessage, type AlertRule, type AlertRuleInput, type Channel } from '@/api/client';
import { COMPARATORS, SEVERITIES, validDuration } from '@/lib/alerts';
import { mono, useTheme } from '@/theme';

import { QueryInput } from './QueryInput';
import { Button, Chip, ErrorText } from './ui';

const EXAMPLE = 'sum(count_over_time({service="api"} |= "error" [5m]))';

/** Create or edit an alert rule. The server re-validates everything. */
export function AlertRuleForm({
  initial,
  channels,
  submitLabel,
  onSubmit,
  onCancel,
}: {
  initial?: AlertRule;
  channels: Channel[];
  submitLabel: string;
  onSubmit: (body: AlertRuleInput) => Promise<unknown>;
  onCancel: () => void;
}) {
  const t = useTheme();
  const [name, setName] = useState(initial?.name ?? '');
  const [query, setQuery] = useState(initial?.query ?? '');
  const [comparator, setComparator] = useState<AlertRule['comparator']>(initial?.comparator ?? 'gt');
  const [threshold, setThreshold] = useState(String(initial?.threshold ?? 0));
  const [windowText, setWindowText] = useState(initial?.window ?? '5m');
  const [forText, setForText] = useState(initial?.for_duration ?? '0s');
  const [cooldown, setCooldown] = useState(initial?.cooldown ?? '0s');
  const [severity, setSeverity] = useState<AlertRule['severity']>(initial?.severity ?? 'warning');
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [picked, setPicked] = useState<string[]>(initial?.notify_channel_ids ?? []);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const save = async () => {
    const th = Number(threshold);
    if (!name.trim()) return setError('Give the rule a name');
    if (!query.trim()) return setError('Enter a metric query');
    if (threshold.trim() === '' || !Number.isFinite(th)) return setError('Threshold must be a number');
    if (!validDuration(windowText, false)) return setError('Window must be a duration such as 5m');
    if (!validDuration(forText, true)) return setError('"For" must be a duration such as 2m, or 0s');
    if (!validDuration(cooldown, true)) return setError('Cooldown must be a duration such as 30m, or 0s');
    setBusy(true);
    setError(null);
    try {
      await onSubmit({
        name: name.trim(),
        query: query.trim(),
        comparator,
        threshold: th,
        window: windowText.trim(),
        for_duration: forText.trim() || '0s',
        cooldown: cooldown.trim() || '0s',
        severity,
        enabled,
        notify_channel_ids: picked,
      });
    } catch (e) {
      setError(errorMessage(e, 'Could not save the rule'));
    } finally {
      setBusy(false);
    }
  };

  const field = {
    color: t.text,
    backgroundColor: t.surface,
    borderColor: t.border,
    borderWidth: 1,
    borderRadius: 8,
    paddingHorizontal: 10,
    paddingVertical: 8,
    fontSize: 14,
  } as const;
  const label = { color: t.textDim, fontSize: 12 } as const;
  const toggle = (id: string) => setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));

  return (
    <View style={{ gap: 10, padding: 12, backgroundColor: t.surfaceAlt, borderRadius: 10, borderWidth: 1, borderColor: t.border }}>
      <TextInput accessibilityLabel="Rule name" placeholder="Rule name" placeholderTextColor={t.textDim} value={name} onChangeText={setName} style={field} />
      <QueryInput value={query} onChange={setQuery} onSubmit={() => void save()} />
      {!query && (
        <Text style={[mono, { color: t.textDim, fontSize: 11 }]} accessibilityLabel="Example query">
          e.g. {EXAMPLE}
        </Text>
      )}
      <Text style={label}>Fire when the query value is</Text>
      <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: 6, alignItems: 'center' }}>
        {COMPARATORS.map((c) => (
          <Chip key={c.id} label={c.symbol} accessibilityLabel={`Comparator ${c.id}`} selected={comparator === c.id} onPress={() => setComparator(c.id)} />
        ))}
        <TextInput
          accessibilityLabel="Threshold"
          placeholder="Threshold"
          placeholderTextColor={t.textDim}
          value={threshold}
          onChangeText={setThreshold}
          keyboardType="numbers-and-punctuation"
          style={[field, { minWidth: 100 }]}
        />
      </View>
      <View style={{ flexDirection: 'row', gap: 8 }}>
        {[
          { l: 'Window', v: windowText, set: setWindowText },
          { l: 'For', v: forText, set: setForText },
          { l: 'Cooldown', v: cooldown, set: setCooldown },
        ].map((f) => (
          <View key={f.l} style={{ flex: 1, gap: 4 }}>
            <Text style={label}>{f.l}</Text>
            <TextInput accessibilityLabel={f.l} value={f.v} onChangeText={f.set} autoCapitalize="none" placeholderTextColor={t.textDim} style={field} />
          </View>
        ))}
      </View>
      <Text style={label}>Severity</Text>
      <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: 6 }}>
        {SEVERITIES.map((s) => (
          <Chip key={s} label={s} accessibilityLabel={`Severity ${s}`} selected={severity === s} onPress={() => setSeverity(s)} />
        ))}
      </View>
      <Text style={label}>Notify</Text>
      {channels.length === 0 ? (
        <Text style={{ color: t.textDim, fontSize: 12 }}>No channels yet. An admin can add one in Settings.</Text>
      ) : (
        <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: 6 }}>
          {channels.map((c) => (
            <Chip key={c.id} label={c.name} accessibilityLabel={`Channel ${c.name}`} selected={picked.includes(c.id)} onPress={() => toggle(c.id)} />
          ))}
        </View>
      )}
      <Chip label={enabled ? 'Enabled' : 'Disabled'} accessibilityLabel="Enabled" selected={enabled} onPress={() => setEnabled((e) => !e)} />
      {error && <ErrorText>{error}</ErrorText>}
      <View style={{ flexDirection: 'row', gap: 8 }}>
        <Button title={submitLabel} onPress={() => void save()} loading={busy} style={{ flex: 1 }} />
        <Button title="Cancel" variant="ghost" onPress={onCancel} />
      </View>
    </View>
  );
}
