import { Stack, useLocalSearchParams } from 'expo-router';
import { useState } from 'react';
import { TextInput, View } from 'react-native';

import { ServiceHistory } from '@/components/ServiceHistory';
import { ServiceLive } from '@/components/ServiceLive';
import { Chip } from '@/components/ui';
import { serviceQuery } from '@/lib/serviceQuery';
import { useTheme } from '@/theme';

type Mode = 'live' | 'history';

export default function ServiceScreen() {
  const t = useTheme();
  const params = useLocalSearchParams<{ name: string }>();
  const name = decodeURIComponent(params.name ?? '');
  const [mode, setMode] = useState<Mode>('live');
  const [draft, setDraft] = useState('');
  const [filter, setFilter] = useState('');
  const query = serviceQuery(name, filter);

  return (
    <View style={{ flex: 1, backgroundColor: t.bg }}>
      <Stack.Screen options={{ title: name }} />
      <View style={{ padding: 12, gap: 8, backgroundColor: t.surface, borderBottomWidth: 1, borderBottomColor: t.border }}>
        <View style={{ flexDirection: 'row' }}>
          <Chip label="Live" selected={mode === 'live'} onPress={() => setMode('live')} />
          <Chip label="History" selected={mode === 'history'} onPress={() => setMode('history')} />
        </View>
        <TextInput
          accessibilityLabel="Filter lines"
          placeholder={'Filter lines, e.g. timeout  (or | json | level="error")'}
          placeholderTextColor={t.textDim}
          value={draft}
          onChangeText={setDraft}
          onSubmitEditing={() => setFilter(draft)}
          autoCapitalize="none"
          autoCorrect={false}
          returnKeyType="search"
          style={{ color: t.text, backgroundColor: t.bg, borderColor: t.border, borderWidth: 1, borderRadius: 8, paddingHorizontal: 10, paddingVertical: 8, fontSize: 15 }}
        />
      </View>
      {mode === 'live' ? <ServiceLive key={query} query={query} /> : <ServiceHistory key={query} query={query} />}
    </View>
  );
}
