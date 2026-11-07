import { Stack } from 'expo-router';

import { useTheme } from '@/theme';

export default function ServicesLayout() {
  const t = useTheme();
  return (
    <Stack
      screenOptions={{
        headerStyle: { backgroundColor: t.surface },
        headerTintColor: t.text,
        contentStyle: { backgroundColor: t.bg },
      }}>
      <Stack.Screen name="index" options={{ title: 'Services' }} />
      <Stack.Screen name="[name]" options={{ title: 'Service' }} />
    </Stack>
  );
}
