import { Stack } from 'expo-router';

import { useTheme } from '@/theme';

export default function DashboardsLayout() {
  const t = useTheme();
  return (
    <Stack
      screenOptions={{
        headerStyle: { backgroundColor: t.surface },
        headerTintColor: t.text,
        contentStyle: { backgroundColor: t.bg },
      }}>
      <Stack.Screen name="index" options={{ title: 'Dashboards' }} />
      <Stack.Screen name="[id]" options={{ title: 'Dashboard' }} />
    </Stack>
  );
}
