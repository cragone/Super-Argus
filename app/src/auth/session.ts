import { create } from 'zustand';

import { api, configureClient, ApiError, unwrap, type Me } from '@/api/client';

import { isWeb, storage } from './storage';

export type SessionStatus = 'loading' | 'signedOut' | 'signedIn';

interface SessionState {
  status: SessionStatus;
  /** Server origin. On web this is empty (same origin) unless EXPO_PUBLIC_ARGUS_URL is set for development. */
  origin: string;
  accessToken: string | null;
  user: Me | null;
  bootstrap(): Promise<void>;
  signIn(origin: string, email: string, password: string): Promise<void>;
  signOut(): Promise<void>;
}

/** Normalises user input like "10.0.0.5:8080" into "http://10.0.0.5:8080". */
export function normalizeServerUrl(input: string): string {
  let s = input.trim().replace(/\/+$/, '');
  if (!s) return '';
  if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(s)) s = `http://${s}`;
  return s;
}

const devOrigin = (process.env.EXPO_PUBLIC_ARGUS_URL ?? '').replace(/\/+$/, '');

let refreshInFlight: Promise<string | null> | null = null;

export const useSession = create<SessionState>((set, get) => ({
  status: 'loading',
  origin: isWeb ? devOrigin : '',
  accessToken: null,
  user: null,

  async bootstrap() {
    if (!isWeb) {
      const saved = await storage.getServerUrl();
      if (saved) set({ origin: saved });
    }
    const token = await refreshAccessToken();
    if (!token) {
      set({ status: 'signedOut' });
      return;
    }
    try {
      const user = unwrap(await api.GET('/me'));
      set({ user, status: 'signedIn' });
    } catch {
      set({ status: 'signedOut', accessToken: null });
    }
  },

  async signIn(origin, email, password) {
    const normalized = isWeb ? get().origin : normalizeServerUrl(origin);
    if (!isWeb && !normalized) throw new ApiError('Enter your Argus server address');
    set({ origin: normalized });
    let res;
    try {
      res = await api.POST('/auth/login', { body: { email, password } });
    } catch {
      throw new ApiError(`Could not reach ${normalized || 'the server'}. Check the address and your network.`);
    }
    const tokens = unwrap(res);
    set({ accessToken: tokens.access_token });
    if (!isWeb) {
      await storage.setServerUrl(normalized);
      await storage.setRefreshToken(tokens.refresh_token);
    }
    const user = unwrap(await api.GET('/me'));
    set({ user, status: 'signedIn' });
  },

  async signOut() {
    try {
      await api.POST('/auth/logout'); // clears the web cookie; harmless on native
    } catch {
      /* offline: still sign out locally */
    }
    await storage.setRefreshToken(null);
    set({ accessToken: null, user: null, status: 'signedOut' });
  },
}));

/** Exchanges the refresh token (native: stored; web: cookie) for a new access token. Single-flight. */
export function refreshAccessToken(): Promise<string | null> {
  refreshInFlight ??= (async () => {
    try {
      const stored = await storage.getRefreshToken();
      if (!isWeb && !stored) return null;
      const res = await api.POST('/auth/refresh', { body: stored ? { refresh_token: stored } : undefined });
      if (res.error !== undefined || !res.data) return null;
      useSession.setState({ accessToken: res.data.access_token });
      if (!isWeb) await storage.setRefreshToken(res.data.refresh_token);
      return res.data.access_token;
    } catch {
      return null;
    } finally {
      refreshInFlight = null;
    }
  })();
  return refreshInFlight;
}

configureClient({
  getOrigin: () => useSession.getState().origin,
  getAccessToken: () => useSession.getState().accessToken,
  async refresh() {
    const token = await refreshAccessToken();
    if (!token) useSession.setState({ status: 'signedOut', accessToken: null, user: null });
    return token;
  },
});
