package version_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/jaredjakacky/servekit/version"
)

func TestGetSnapshotsCurrentBuildMetadata(t *testing.T) {
	oldVersion, oldCommit, oldDate := version.Version, version.Commit, version.Date
	version.Version = "v1.2.3"
	version.Commit = "abc123"
	version.Date = "2026-04-04T00:00:00Z"
	t.Cleanup(func() {
		version.Version = oldVersion
		version.Commit = oldCommit
		version.Date = oldDate
	})

	got := version.Get()

	if got.Version != "v1.2.3" {
		t.Fatalf("Version = %q, want %q", got.Version, "v1.2.3")
	}
	if got.Commit != "abc123" {
		t.Fatalf("Commit = %q, want %q", got.Commit, "abc123")
	}
	if got.Date != "2026-04-04T00:00:00Z" {
		t.Fatalf("Date = %q, want %q", got.Date, "2026-04-04T00:00:00Z")
	}
	if got.GoVersion != runtime.Version() {
		t.Fatalf("GoVersion = %q, want %q", got.GoVersion, runtime.Version())
	}
}

func TestInfoStringFormatsCompactSummary(t *testing.T) {
	info := version.Info{
		Version:   "v1.2.3",
		Commit:    "abc123",
		Date:      "2026-04-04T00:00:00Z",
		GoVersion: "go1.test",
	}

	if got := info.String(); got != "v1.2.3 commit=abc123 date=2026-04-04T00:00:00Z go=go1.test" {
		t.Fatalf("String() = %q, want compact summary", got)
	}
}

func TestInfoHandlerServesStableJSONResponse(t *testing.T) {
	info := version.Info{
		Version:   "v1.2.3",
		Commit:    "abc123",
		Date:      "2026-04-04T00:00:00Z",
		GoVersion: "go1.test",
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	info.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json; charset=utf-8")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache, no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-cache, no-store")
	}

	var body version.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode JSON body: %v", err)
	}
	if body != info {
		t.Fatalf("body = %+v, want %+v", body, info)
	}
}
