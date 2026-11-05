import { useState } from 'react';
import { KeyboardAvoidingView, Platform, ScrollView, Text, TextInput, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { errorMessage } from '@/api/client';
import { useSession } from '@/auth/session';
import { isWeb } from '@/auth/storage';
import { Button, ErrorText } from '@/components/ui';
import { useTheme } from '@/theme';

export default function LoginScreen() {
  const t = useTheme();
  const signIn = useSession((s) => s.signIn);
  const savedOrigin = useSession((s) => s.origin);
  const [server, setServer] = useState(savedOrigin);
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const canSubmit = !!email.trim() && !!password && (isWeb || !!server.trim());

  const submit = async () => {
    if (!canSubmit || busy) return;
    setBusy(true);
    setError(null);
    try {
      await signIn(server, email.trim(), password);
    } catch (e) {
      setError(errorMessage(e, 'Sign in failed'));
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
    paddingHorizontal: 12,
    paddingVertical: 10,
    fontSize: 16,
  } as const;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: t.bg }}>
      <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined} style={{ flex: 1 }}>
        <ScrollView contentContainerStyle={{ flexGrow: 1, justifyContent: 'center', padding: 24 }} keyboardShouldPersistTaps="handled">
          <View style={{ width: '100%', maxWidth: 420, alignSelf: 'center', gap: 14 }}>
            <Text accessibilityRole="header" style={{ fontSize: 30, fontWeight: '700', color: t.text }}>
              Argus
            </Text>
            <Text style={{ color: t.textDim }}>Sign in to your log server.</Text>

            {!isWeb && (
              <TextInput
                accessibilityLabel="Server address"
                placeholder="Server, e.g. https://logs.example.com"
                placeholderTextColor={t.textDim}
                value={server}
                onChangeText={setServer}
                autoCapitalize="none"
                autoCorrect={false}
                keyboardType="url"
                style={field}
              />
            )}
            <TextInput
              accessibilityLabel="Email"
              placeholder="Email"
              placeholderTextColor={t.textDim}
              value={email}
              onChangeText={setEmail}
              autoCapitalize="none"
              autoCorrect={false}
              keyboardType="email-address"
              textContentType="username"
              style={field}
            />
            <TextInput
              accessibilityLabel="Password"
              placeholder="Password"
              placeholderTextColor={t.textDim}
              value={password}
              onChangeText={setPassword}
              secureTextEntry
              textContentType="password"
              onSubmitEditing={submit}
              style={field}
            />
            {error && <ErrorText>{error}</ErrorText>}
            <Button title="Sign in" onPress={submit} disabled={!canSubmit} loading={busy} />
          </View>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}
