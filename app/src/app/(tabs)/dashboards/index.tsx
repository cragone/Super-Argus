import { router } from 'expo-router';
import { useState } from 'react';
import { Pressable, ScrollView, Text, TextInput, View } from 'react-native';

import { errorMessage } from '@/api/client';
import { Button, Card, ErrorText } from '@/components/ui';
import { useCanEdit, useDashboardMutations, useDashboards } from '@/hooks/useDashboards';
import { useTheme } from '@/theme';

export default function DashboardListScreen() {
  const t = useTheme();
  const list = useDashboards();
  const canEdit = useCanEdit();
  const { createDashboard } = useDashboardMutations();
  const [title, setTitle] = useState('');
  const [error, setError] = useState<string | null>(null);

  const create = async () => {
    if (!title.trim()) return setError('Give the dashboard a title');
    setError(null);
    try {
      const d = await createDashboard.mutateAsync(title.trim());
      setTitle('');
      router.push(`/dashboards/${d.id}`);
    } catch (e) {
      setError(errorMessage(e, 'Could not create the dashboard'));
    }
  };

  return (
    <ScrollView style={{ flex: 1, backgroundColor: t.bg }} contentContainerStyle={{ padding: 16, gap: 12, maxWidth: 720, width: '100%', alignSelf: 'center' }}>
      {canEdit && (
        <Card style={{ gap: 8 }}>
          <Text style={{ color: t.text, fontWeight: '600' }}>New dashboard</Text>
          <TextInput
            accessibilityLabel="Dashboard title"
            placeholder="Title, e.g. API health"
            placeholderTextColor={t.textDim}
            value={title}
            onChangeText={setTitle}
            onSubmitEditing={() => void create()}
            style={{ color: t.text, backgroundColor: t.surface, borderColor: t.border, borderWidth: 1, borderRadius: 8, paddingHorizontal: 10, paddingVertical: 8, fontSize: 15 }}
          />
          {error && <ErrorText>{error}</ErrorText>}
          <Button title="Create dashboard" onPress={() => void create()} loading={createDashboard.isPending} />
        </Card>
      )}

      {list.isLoading && <Text style={{ color: t.textDim }}>Loading…</Text>}
      {list.isError && <ErrorText>{errorMessage(list.error)}</ErrorText>}
      {list.data?.length === 0 && (
        <Text style={{ color: t.textDim }}>{canEdit ? 'No dashboards yet. Create one above.' : 'No dashboards yet. Ask an editor to create one.'}</Text>
      )}
      {list.data?.map((d) => (
        <Pressable key={d.id} accessibilityRole="button" accessibilityLabel={`Open dashboard ${d.title}`} onPress={() => router.push(`/dashboards/${d.id}`)}>
          <Card style={{ gap: 4 }}>
            <Text style={{ color: t.text, fontSize: 16, fontWeight: '600' }}>{d.title}</Text>
            {!!d.description && <Text style={{ color: t.textDim }}>{d.description}</Text>}
          </Card>
        </Pressable>
      ))}
      <View style={{ height: 24 }} />
    </ScrollView>
  );
}
