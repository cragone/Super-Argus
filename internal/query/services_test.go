package query

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServices(t *testing.T) {
	f := newFixture(t)
	f.load()
	require.NoError(t, f.ix.FlushSeen())
	ctx := context.Background()

	got, err := f.svc.Services(ctx, "service", 0, 0, time.Now())
	require.NoError(t, err)
	require.Len(t, got, 3)
	for i, name := range []string{"api", "web", "worker"} {
		require.Equal(t, name, got[i].Name)
		require.True(t, got[i].Active, name)
		require.Equal(t, 2, got[i].Streams, name) // prod + dev
		require.EqualValues(t, 120, got[i].Lines, name)
		require.Positive(t, got[i].Bytes, name)
	}

	// long-quiet services are listed as inactive
	got, err = f.svc.Services(ctx, "service", 0, 0, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, got, 3)
	for _, s := range got {
		require.False(t, s.Active, s.Name)
	}

	// a window after every stream's last_seen excludes everything
	got, err = f.svc.Services(ctx, "service", time.Now().Add(time.Hour).UnixNano(), 0, time.Now())
	require.NoError(t, err)
	require.Empty(t, got)

	// a label key nothing carries yields no services
	got, err = f.svc.Services(ctx, "nosuch", 0, 0, time.Now())
	require.NoError(t, err)
	require.Empty(t, got)

	_, err = f.svc.Services(ctx, "", 0, 0, time.Now())
	require.ErrorIs(t, err, ErrBadRequest)
}
