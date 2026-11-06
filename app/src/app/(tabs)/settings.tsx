import { ScrollView, Text } from 'react-native';

import { useSession } from '@/auth/session';
import { ChannelsCard } from '@/components/ChannelsCard';
import { Button, Card } from '@/components/ui';
import { useIsAdmin } from '@/hooks/useAlerts';
import { useTheme } from '@/theme';

export default function SettingsScreen() {
  const t = useTheme();
  const user = useSession((s) => s.user);
  const origin = useSession((s) => s.origin);
  const signOut = useSession((s) => s.signOut);
  const isAdmin = useIsAdmin();

  return (
    <ScrollView style={{ flex: 1, backgroundColor: t.bg }} contentContainerStyle={{ padding: 16, gap: 16 }}>
      <Card style={{ gap: 6 }}>
        <Text style={{ color: t.textDim }}>Signed in as</Text>
        <Text style={{ color: t.text, fontSize: 17, fontWeight: '600' }}>{user?.email}</Text>
        <Text style={{ color: t.textDim }}>Role: {user?.role}</Text>
        <Text style={{ color: t.textDim }}>Server: {origin || 'this site'}</Text>
      </Card>
      {isAdmin && <ChannelsCard />}
      <Button title="Sign out" variant="ghost" onPress={() => void signOut()} />
    </ScrollView>
  );
}
