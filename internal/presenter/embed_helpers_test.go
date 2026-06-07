package presenter

import "testing"

func TestServePublicFile_PublicRootFileName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "/favicon.svg", want: "favicon.svg", ok: true},
		{raw: "favicon.svg", want: "favicon.svg", ok: true},
		{raw: "/", ok: false},
		{raw: "/nested/favicon.svg", ok: false},
		{raw: "/../favicon.svg", ok: false},
		{raw: "/fav..icon.svg", ok: false},
	}

	for _, tc := range cases {
		got, ok := publicRootFileName(tc.raw)
		if ok != tc.ok {
			t.Errorf("publicRootFileName(%q) ok = %v, want %v", tc.raw, ok, tc.ok)
			continue
		}
		if got != tc.want {
			t.Errorf("publicRootFileName(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestContentTypeForAsset_TableHasViteExtensions(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		".html":  "text/html; charset=utf-8",
		".css":   "text/css; charset=utf-8",
		".js":    "application/javascript; charset=utf-8",
		".mjs":   "application/javascript; charset=utf-8",
		".json":  "application/json; charset=utf-8",
		".svg":   "image/svg+xml",
		".png":   "image/png",
		".jpg":   "image/jpeg",
		".jpeg":  "image/jpeg",
		".webp":  "image/webp",
		".ico":   "image/x-icon",
		".woff":  "font/woff",
		".woff2": "font/woff2",
		".ttf":   "font/ttf",
		".map":   "application/json; charset=utf-8",
	}

	for ext, want := range cases {
		got, ok := assetContentTypeForExt(ext)
		if !ok {
			t.Errorf("assetContentTypeForExt(%q) ok = false, want true", ext)
			continue
		}
		if got != want {
			t.Errorf("assetContentTypeForExt(%q) = %q, want %q", ext, got, want)
		}
	}
	if _, ok := assetContentTypeForExt(""); ok {
		t.Fatal("assetContentTypeForExt must not define the default no-extension type")
	}
}
