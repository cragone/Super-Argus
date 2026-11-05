package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func get(h http.Handler, p string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
	return rec
}

func TestSPAFallbackAndAssets(t *testing.T) {
	h := handlerFor(fstest.MapFS{
		"index.html":                    {Data: []byte("<html>app</html>")},
		"_expo/static/js/web/app-ab.js": {Data: []byte("js")},
		"favicon.ico":                   {Data: []byte("ico")},
	})
	require.Contains(t, get(h, "/").Body.String(), "app")
	require.Contains(t, get(h, "/explore/deep/link").Body.String(), "app", "client route falls back to index.html")
	require.Equal(t, "no-cache", get(h, "/explore").Header().Get("Cache-Control"))

	js := get(h, "/_expo/static/js/web/app-ab.js")
	require.Equal(t, "js", js.Body.String())
	require.Contains(t, js.Header().Get("Cache-Control"), "immutable")
	require.Equal(t, "ico", get(h, "/favicon.ico").Body.String())

	require.Equal(t, 404, get(h, "/missing.js").Code, "missing asset is a 404, not HTML")
	require.Equal(t, "ico", get(h, "/../favicon.ico").Body.String(), "path is cleaned")
}

func TestNotBuilt(t *testing.T) {
	rec := get(handlerFor(fstest.MapFS{".gitkeep": {}}), "/")
	require.Equal(t, 404, rec.Code)
	require.Contains(t, rec.Body.String(), "npm run build:web")
}

func TestEmbeddedHandlerBuilds(t *testing.T) {
	require.NotNil(t, Handler())
}
