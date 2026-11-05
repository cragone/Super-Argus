import { Tabs } from 'expo-router';

import { useTheme } from '@/theme';

export default function TabsLayout() {
  const t = useTheme();
  return (
    <Tabs
      screenOptions={{
        headerStyle: { backgroundColor: t.surface },
        headerTintColor: t.text,
        tabBarStyle: { backgroundColor: t.surface, borderTopColor: t.border },
        tabBarActiveTintColor: t.accent,
        tabBarInactiveTintColor: t.textDim,
        tabBarIconStyle: { display: 'none' },
        tabBarLabelPosition: 'beside-icon',
        tabBarLabelStyle: { fontSize: 15, fontWeight: '600' },
      }}>
      <Tabs.Screen name="index" options={{ title: 'Explore' }} />
      <Tabs.Screen name="settings" options={{ title: 'Settings' }} />
    </Tabs>
  );
}
