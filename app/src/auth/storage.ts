import * as SecureStore from 'expo-secure-store';
import { Platform } from 'react-native';

/**
 * Persistent session storage.
 *
 * Native: expo-secure-store (Keychain / Keystore) holds the refresh token and server URL.
 * Web: nothing is persisted from JavaScript. The refresh token is an HttpOnly cookie set by
 * the server, and access tokens only ever live in memory.
 */
export interface SessionStorage {
  getServerUrl(): Promise<string | null>;
  setServerUrl(url: string): Promise<void>;
  getRefreshToken(): Promise<string | null>;
  setRefreshToken(token: string | null): Promise<void>;
}

const URL_KEY = 'argus.serverUrl';
const REFRESH_KEY = 'argus.refreshToken';

const nativeStorage: SessionStorage = {
  getServerUrl: () => SecureStore.getItemAsync(URL_KEY),
  setServerUrl: (url) => SecureStore.setItemAsync(URL_KEY, url),
  getRefreshToken: () => SecureStore.getItemAsync(REFRESH_KEY),
  setRefreshToken: (t) => (t ? SecureStore.setItemAsync(REFRESH_KEY, t) : SecureStore.deleteItemAsync(REFRESH_KEY)),
};

const webStorage: SessionStorage = {
  getServerUrl: async () => null,
  setServerUrl: async () => {},
  getRefreshToken: async () => null,
  setRefreshToken: async () => {},
};

export const storage: SessionStorage = Platform.OS === 'web' ? webStorage : nativeStorage;

export const isWeb = Platform.OS === 'web';
