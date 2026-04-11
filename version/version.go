package version

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"runtime/debug"
)

// Version, Commit, and Date are package-level build metadata variables.
//
// They are typically set at build time via -ldflags. When left at their
// defaults, init attempts to populate them from runtime/debug.ReadBuildInfo so
// local builds and go install binaries still expose useful metadata.
//
// Example ldflags for this module. The values below are illustrative. Replace
// them with your real build metadata:
//
//	go build -ldflags "\
//	  -X 'github.com/jaredjakacky/servekit/version.Version=v1.2.3' \
//	  -X 'github.com/jaredjakacky/servekit/version.Commit=abc1234' \
//	  -X 'github.com/jaredjakacky/servekit/version.Date=2026-04-06T00:00:00Z'"
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func init() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}

	// Use the module version as a fallback when Version was not set via ldflags.
	// "(devel)" is what Go reports for a local, untagged module build.
	if Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		Version = bi.Main.Version
	}

	// Extract VCS metadata from embedded build settings.
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if Commit == "unknown" && s.Value != "" {
				Commit = shortHash(s.Value)
			}
		case "vcs.time":
			if Date == "unknown" && s.Value != "" {
				Date = s.Value
			}
		}
	}
}

// Info is the serialized version payload exposed by this package.
type Info struct {
	Version   string `json:"version"`   // Version is the application or module version.
	Commit    string `json:"commit"`    // Commit is the short VCS revision, when available.
	Date      string `json:"date"`      // Date is the build or VCS timestamp.
	GoVersion string `json:"goVersion"` // GoVersion is the runtime toolchain version.
}

// Get snapshots current build metadata into an Info value.
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
	}
}

// String formats Info as a compact single-line summary.
func (i Info) String() string {
	return fmt.Sprintf("%s commit=%s date=%s go=%s",
		i.Version, i.Commit, i.Date, i.GoVersion)
}

// Handler returns an http.Handler that serves this Info as JSON.
//
// The JSON body is pre-encoded once when Handler is created and reused for each
// request. Responses are returned with HTTP 200, content type
// application/json with charset=utf-8 and no-cache headers.
func (i Info) Handler() http.Handler {
	body, err := json.Marshal(i)
	if err != nil {
		// Info only contains plain strings, so marshalling cannot fail.
		panic("servekit/version: failed to marshal build info: " + err.Error())
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}

// shortHash returns the first 12 characters of a VCS hash.
func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
