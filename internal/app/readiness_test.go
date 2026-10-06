package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReadinessRequiresDatabaseAndFrontend(t *testing.T) {
	store := testStore(t)
	directory := t.TempDir()
	a := &App{store: store, cfg: Config{StaticDir: directory}}
	check := func(want int) {
		t.Helper()
		response := httptest.NewRecorder()
		a.handleReady(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != want {
			t.Fatalf("readiness: %d, want %d", response.Code, want)
		}
	}
	check(http.StatusServiceUnavailable)
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("<html></html>"), 0600); err != nil {
		t.Fatal(err)
	}
	check(http.StatusOK)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	check(http.StatusServiceUnavailable)
}
