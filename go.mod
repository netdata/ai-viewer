module github.com/netdata/ai-viewer

go 1.26

// Pin the toolchain to go1.26.4, which fixes the go1.26.3 stdlib CVEs
// GO-2026-5039 (net/textproto) and GO-2026-5037 (crypto/x509) that govulncheck
// flags (SOW-0038). GOTOOLCHAIN=auto + setup-go's go-version-file both honor this.
toolchain go1.26.4

require (
	github.com/fsnotify/fsnotify v1.10.1
	github.com/google/go-cmp v0.7.0
	modernc.org/sqlite v1.50.1
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.42.0 // indirect
	modernc.org/libc v1.72.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
