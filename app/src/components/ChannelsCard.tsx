import { useState } from 'react';
import { Text, TextInput, View } from 'react-native';

import { errorMessage } from '@/api/client';
import { useAlertMutations, useChannels } from '@/hooks/useAlerts';
import { useTheme } from '@/theme';

import { Button, Card, Chip, ErrorText } from './ui';

/** Admin-only management of notification channels (webhooks). */
export function ChannelsCard() {
  const t = useTheme();
  const channels = useChannels();
  const { createChannel, deleteChannel, testChannel } = useAlertMutations();
  const [name, setName] = useState('');
  const [url, setUrl] = useState('');
  const [secret, setSecret] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [results, setResults] = useState<Record<string, string>>({});

  const add = () => {
    setError(null);
    if (!name.trim() || !url.trim()) return setError('Name and URL are required');
    createChannel.mutate(
      { name: name.trim(), type: 'webhook', url: url.trim(), ...(secret ? { secret } : {}) },
      {
        onSuccess: () => { setName(''); setUrl(''); setSecret(''); },
        onError: (e) => setError(errorMessage(e, 'Could not add the channel')),
      },
    );
  };
  const test = (id: string) =>
    testChannel.mutate(id, {
      onSuccess: (r) => setResults((m) => ({ ...m, [id]: r.ok ? 'Delivered' : `Failed: ${r.error ?? 'unknown error'}` })),
      onError: (e) => setResults((m) => ({ ...m, [id]: errorMessage(e, 'Test failed') })),
    });

  const field = { color: t.text, backgroundColor: t.surface, borderColor: t.border, borderWidth: 1, borderRadius: 8, paddingHorizontal: 10, paddingVertical: 8, fontSize: 14 } as const;
  return (
    <Card style={{ gap: 10 }}>
      <Text style={{ color: t.text, fontSize: 16, fontWeight: '700' }}>Notification channels</Text>
      {channels.data?.length === 0 && <Text style={{ color: t.textDim }}>No channels yet.</Text>}
      {channels.data?.map((c) => (
        <View key={c.id} style={{ gap: 4 }}>
          <Text style={{ color: t.text, fontWeight: '600' }}>{c.name}</Text>
          <Text style={{ color: t.textDim, fontSize: 12 }}>
            webhook · {c.url_host}
            {c.has_secret ? ' · signed' : ''}
          </Text>
          <View style={{ flexDirection: 'row', gap: 8 }}>
            <Chip label="Send test" accessibilityLabel={`Test ${c.name}`} onPress={() => test(c.id)} />
            <Chip
              label="Delete"
              accessibilityLabel={`Delete channel ${c.name}`}
              onPress={() => deleteChannel.mutate(c.id, { onError: (e) => setError(errorMessage(e, 'Could not delete the channel')) })}
            />
          </View>
          {results[c.id] && <Text style={{ color: t.textDim, fontSize: 12 }}>{results[c.id]}</Text>}
        </View>
      ))}
      <TextInput accessibilityLabel="Channel name" placeholder="Name" placeholderTextColor={t.textDim} value={name} onChangeText={setName} style={field} />
      <TextInput accessibilityLabel="Webhook URL" placeholder="https://example.com/hook" placeholderTextColor={t.textDim} value={url} onChangeText={setUrl} autoCapitalize="none" autoCorrect={false} style={field} />
      <TextInput accessibilityLabel="Signing secret" placeholder="Signing secret (optional)" placeholderTextColor={t.textDim} value={secret} onChangeText={setSecret} autoCapitalize="none" secureTextEntry style={field} />
      {error && <ErrorText>{error}</ErrorText>}
      <Button title="Add webhook" onPress={add} loading={createChannel.isPending} />
    </Card>
  );
}
