import { Text, View } from 'react-native';

import { useSession } from '@/auth/session';
import { Button, Card } from '@/components/ui';
import { useTheme } from '@/theme';

export default function SettingsScreen() {
  const t = useTheme();
  const user = useSession((s) => s.user);
  const origin = useSession((s) => s.origin);
  const signOut = useSession((s) => s.signOut);

  return (
    <View style={{ flex: 1, padding: 16, gap: 16, backgroundColor: t.bg }}>
      <Card style={{ gap: 6 }}>
        <Text style={{ color: t.textDim }}>Signed in as</Text>
        <Text style={{ color: t.text, fontSize: 17, fontWeight: '600' }}>{user?.email}</Text>
        <Text style={{ color: t.textDim }}>Role: {user?.role}</Text>
        <Text style={{ color: t.textDim }}>Server: {origin || 'this site'}</Text>
      </Card>
      <Button title="Sign out" variant="ghost" onPress={() => void signOut()} />
    </View>
  );
}
