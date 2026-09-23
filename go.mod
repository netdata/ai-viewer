module github.com/netdata/ai-viewer

go 1.26

// Pin the build toolchain to a patched stdlib release so the govulncheck gate
// stays green. Bump this as new stdlib advisories land. GOTOOLCHAIN=auto and
// setup-go's go-version-file both honor this directive. See SOW-0038 for the
// rationale (go1.26.4 closed GO-2026-5039 + GO-2026-5037).
toolchain go1.26.4

require (
	github.com/fsnotify/fsnotify v1.10.1
	github.com/google/go-cmp v0.7.0
	modernc.org/sqlite v1.53.0
	pgregory.net/rapid v1.3.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.44.0 // indirect
	modernc.org/libc v1.73.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
