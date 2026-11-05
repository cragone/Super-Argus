import { ApiError, apiUrl, errorMessage, unwrap } from '@/api/client';
import { normalizeServerUrl } from '@/auth/session';

describe('normalizeServerUrl', () => {
  it('adds http:// and trims slashes and whitespace', () => {
    expect(normalizeServerUrl(' 10.0.0.5:8080/ ')).toBe('http://10.0.0.5:8080');
    expect(normalizeServerUrl('https://logs.example.com//')).toBe('https://logs.example.com');
    expect(normalizeServerUrl('')).toBe('');
  });
});

describe('api helpers', () => {
  it('builds API urls', () => {
    expect(apiUrl('/me', 'http://h:1/')).toBe('http://h:1/api/v1/me');
    expect(apiUrl('/me', '')).toBe('/api/v1/me');
  });

  it('unwraps data and throws ApiError from the error envelope', () => {
    const response = { status: 400 } as Response;
    expect(unwrap({ data: { ok: true }, response })).toEqual({ ok: true });
    expect.assertions(6);
    try {
      unwrap({ error: { error: { code: 'bad_query', message: 'nope' } }, response });
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as ApiError).message).toBe('nope');
      expect((e as ApiError).code).toBe('bad_query');
      expect((e as ApiError).status).toBe(400);
    }
    expect(errorMessage({ error: { message: 'm' } })).toBe('m');
  });
});
