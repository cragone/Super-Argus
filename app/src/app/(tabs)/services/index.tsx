import { router } from 'expo-router';
import { Pressable, ScrollView, Text, View } from 'react-native';

import { errorMessage } from '@/api/client';
import { Card, ErrorText } from '@/components/ui';
import { useServices } from '@/hooks/useLogs';
import { formatDateTime } from '@/lib/time';
import { useTheme } from '@/theme';

function ago(iso: string): string {
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (s < 5) return 'just now';
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return formatDateTime(String(new Date(iso).getTime() * 1_000_000));
}

export default function ServicesScreen() {
  const t = useTheme();
  const list = useServices();

  return (
    <ScrollView style={{ flex: 1, backgroundColor: t.bg }} contentContainerStyle={{ padding: 16, gap: 12, maxWidth: 720, width: '100%', alignSelf: 'center' }}>
      {list.isLoading && <Text style={{ color: t.textDim }}>Loading…</Text>}
      {list.isError && <ErrorText>{errorMessage(list.error)}</ErrorText>}
      {list.data?.length === 0 && (
        <Card style={{ gap: 6 }}>
          <Text style={{ color: t.text, fontWeight: '600' }}>No services yet</Text>
          <Text style={{ color: t.textDim }}>
            Services appear automatically once something ships logs with a service label. Run the Argus agent on a host or node, or point an SDK or Loki client at Argus with an API key.
          </Text>
        </Card>
      )}
      {list.data?.map((s) => (
        <Pressable key={s.name} accessibilityRole="button" accessibilityLabel={`Open service ${s.name}`} onPress={() => router.push(`/services/${encodeURIComponent(s.name)}`)}>
          <Card style={{ gap: 4 }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
              <View style={{ width: 10, height: 10, borderRadius: 5, backgroundColor: s.active ? t.levels.info : t.textDim }} />
              <Text style={{ color: t.text, fontSize: 16, fontWeight: '600', flex: 1 }}>{s.name}</Text>
              <Text style={{ color: t.textDim, fontSize: 12 }}>{s.active ? 'active' : `seen ${ago(s.last_seen)}`}</Text>
            </View>
            <Text style={{ color: t.textDim, fontSize: 13 }}>
              {s.streams} {s.streams === 1 ? 'stream' : 'streams'} · {s.lines.toLocaleString()} lines stored
            </Text>
          </Card>
        </Pressable>
      ))}
      <View style={{ height: 24 }} />
    </ScrollView>
  );
}
