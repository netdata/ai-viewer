package presenter

import "strings"

const defaultAssetType = "application/octet-stream"

// contentTypeForAsset returns a sensible Content-Type for embedded frontend
// assets. Unknown extensions get application/octet-stream.
func contentTypeForAsset(p string) string {
	if contentType, ok := assetContentTypeForExt(lowerExt(p)); ok {
		return contentType
	}
	return defaultAssetType
}

type assetContentType struct {
	ext         string
	contentType string
}

var assetContentTypes = [...]assetContentType{
	{ext: ".html", contentType: "text/html; charset=utf-8"},
	{ext: ".css", contentType: "text/css; charset=utf-8"},
	{ext: ".js", contentType: "application/javascript; charset=utf-8"},
	{ext: ".mjs", contentType: "application/javascript; charset=utf-8"},
	{ext: ".json", contentType: "application/json; charset=utf-8"},
	{ext: ".svg", contentType: "image/svg+xml"},
	{ext: ".png", contentType: "image/png"},
	{ext: ".jpg", contentType: "image/jpeg"},
	{ext: ".jpeg", contentType: "image/jpeg"},
	{ext: ".webp", contentType: "image/webp"},
	{ext: ".ico", contentType: "image/x-icon"},
	{ext: ".woff", contentType: "font/woff"},
	{ext: ".woff2", contentType: "font/woff2"},
	{ext: ".ttf", contentType: "font/ttf"},
	{ext: ".map", contentType: "application/json; charset=utf-8"},
}

func assetContentTypeForExt(ext string) (string, bool) {
	for _, candidate := range assetContentTypes {
		if ext == candidate.ext {
			return candidate.contentType, true
		}
	}
	return "", false
}

func lowerExt(p string) string {
	dot := strings.LastIndexByte(p, '.')
	if dot < 0 {
		return ""
	}
	return strings.ToLower(p[dot:])
}
