package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandURLs(t *testing.T) {
	c := Component{
		Download:  "https://host/{os}/{arch}/{version}/data.tar.gz",
		Checksums: "https://host/{os}/{arch}/{version}/data.tar.gz.sha256",
	}
	assert.Equal(t, "https://host/linux/amd64/1.2.3/data.tar.gz", c.DownloadURL("1.2.3", "linux", "amd64"))
	assert.Equal(t, "https://host/darwin/arm64/1.2.3/data.tar.gz.sha256", c.ChecksumsURL("1.2.3", "darwin", "arm64"))
}

func TestResolveVersion(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("0.4.2\n"))
	}))
	defer srv.Close()

	e := &Entry{
		Name:       "infracost/terraform",
		VersionURL: srv.URL + "/{os}/{arch}/latest/version",
	}

	v, err := e.ResolveVersion(context.Background(), srv.Client(), "linux", "amd64")
	require.NoError(t, err)
	assert.Equal(t, "0.4.2", v)
	assert.Equal(t, "/linux/amd64/latest/version", gotPath)
}

func TestResolveVersionErrors(t *testing.T) {
	t.Run("no versionUrl", func(t *testing.T) {
		e := &Entry{Name: "infracost/x"}
		_, err := e.ResolveVersion(context.Background(), http.DefaultClient, "linux", "amd64")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no versionUrl")
	})

	t.Run("non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		e := &Entry{Name: "infracost/x", VersionURL: srv.URL + "/v"}
		_, err := e.ResolveVersion(context.Background(), srv.Client(), "linux", "amd64")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected HTTP status")
	})

	t.Run("empty body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
		defer srv.Close()

		e := &Entry{Name: "infracost/x", VersionURL: srv.URL + "/v"}
		_, err := e.ResolveVersion(context.Background(), srv.Client(), "linux", "amd64")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty version response")
	})
}
