package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func guardClock(g *loginGuard, start time.Time) *time.Time {
	now := start
	g.now = func() time.Time { return now }
	return &now
}

func TestGuardBackoffAndDecay(t *testing.T) {
	g := newLoginGuard()
	now := guardClock(g, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	for i := 0; i < emailFailLimit-1; i++ {
		g.fail("e:a", emailFailLimit)
	}
	require.Zero(t, g.retryAfter("e:a"), "below the limit nothing is locked")
	g.fail("e:a", emailFailLimit)
	require.Equal(t, 30*time.Second, g.retryAfter("e:a"))
	require.Zero(t, g.retryAfter("e:other"), "other keys are unaffected")

	*now = now.Add(31 * time.Second)
	require.Zero(t, g.retryAfter("e:a"), "lock expires")
	g.fail("e:a", emailFailLimit)
	require.Equal(t, time.Minute, g.retryAfter("e:a"), "next failure doubles the lock")
	*now = now.Add(2 * time.Minute)
	g.fail("e:a", emailFailLimit)
	require.Equal(t, 2*time.Minute, g.retryAfter("e:a"))

	for i := 0; i < 20; i++ {
		*now = now.Add(20 * time.Minute)
		g.fail("e:a", emailFailLimit)
	}
	require.LessOrEqual(t, g.retryAfter("e:a"), 15*time.Minute, "capped")

	*now = now.Add(time.Hour)
	g.fail("e:a", emailFailLimit)
	require.Zero(t, g.retryAfter("e:a"), "counts decay after quiet time")

	g.succeed("e:a")
	require.Empty(t, g.m)
}

func TestGuardBoundsMemory(t *testing.T) {
	g := newLoginGuard()
	g.maxKeys = 3
	now := guardClock(g, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for i := 0; i < 10; i++ {
		g.fail("k"+strconv.Itoa(i), 5)
	}
	require.LessOrEqual(t, len(g.m), 3)
	*now = now.Add(time.Hour)
	g.fail("fresh", 5)
	require.Contains(t, g.m, "fresh", "idle entries are swept to make room")
}

func TestLoginLockout(t *testing.T) {
	e := newEnv(t)
	now := guardClock(e.srv.guard, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	for i := 0; i < emailFailLimit; i++ {
		_, _, code := e.login("admin@x.io", "wrong")
		require.Equal(t, 401, code)
	}
	rec := e.do("POST", "/api/v1/auth/login", "", map[string]string{"email": "admin@x.io", "password": "adminpw"})
	require.Equal(t, 429, rec.Code, "even the right password is refused while locked")
	require.Equal(t, "30", rec.Header().Get("Retry-After"))
	require.Contains(t, rec.Body.String(), "too_many_attempts")

	_, _, code := e.login("ADMIN@x.io ", "adminpw")
	require.Equal(t, 429, code, "case and whitespace do not dodge the lock")

	*now = now.Add(31 * time.Second)
	_, _, code = e.login("admin@x.io", "adminpw")
	require.Equal(t, 200, code, "works once the lock ends")
	_, _, code = e.login("admin@x.io", "wrong")
	require.Equal(t, 401, code, "a successful login reset the email counter")
}

func TestLoginLockoutAppliesToUnknownEmailsAndIPs(t *testing.T) {
	e := newEnv(t)
	guardClock(e.srv.guard, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// One IP spraying many emails hits the per-IP limit even though no single email does.
	for i := 0; i < ipFailLimit; i++ {
		_, _, code := e.login("user"+strconv.Itoa(i)+"@x.io", "guess")
		require.Equal(t, 401, code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = "192.0.2.1:5555" // httptest's default peer
	require.Equal(t, "192.0.2.1", clientIP(req))
	_, _, code := e.login("admin@x.io", "adminpw")
	require.Equal(t, 429, code)
}
