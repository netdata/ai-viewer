package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	// Side-effect import: the codex adapter registers its factory with
	// internal/adapters via init() so the auto-discovery probe can construct it.
	_ "github.com/netdata/ai-viewer/internal/adapters/codex"
	"github.com/netdata/ai-viewer/internal/adapters/opencode"
)

// opencodeProbeTimeout bounds the one-time opencode auto-discovery ProbeStatus
// COUNT(*) (SOW-0005 round-4 P3-1). The probe is best-effort observability; a
// slow or locked opencode database must not stall startup discovery, so the probe
// runs under this short deadline and discovery proceeds on timeout.
const opencodeProbeTimeout = 10 * time.Second

// opencodeMetaJSON marshals an opencode ProbeStatus result into the JSON
// blob persisted on sources.meta_json (SOW-0024).
func opencodeMetaJSON(sessions, messages, parts int64, latestMigration string) string {
	meta := opencodeSourceMeta{
		SessionCount:    sessions,
		MessageCount:    messages,
		PartCount:       parts,
		LatestMigration: latestMigration,
	}
	blob, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(blob)
}

// opencodeSourceMeta is the JSON shape persisted on sources.meta_json for an
// opencode source (SOW-0024).
type opencodeSourceMeta struct {
	SessionCount    int64  `json:"session_count"`
	MessageCount    int64  `json:"message_count"`
	PartCount       int64  `json:"part_count"`
	LatestMigration string `json:"latest_migration"`
}

// autoDiscoverSources probes the default locations from deployment.md
// §"Source Auto-Discovery". Each existing location becomes a source;
// missing locations are silently skipped.
func autoDiscoverSources(logger *slog.Logger) []configuredSource {
	home, err := os.UserHomeDir()
	if err != nil {
		logger.Warn("ai-viewer-ingest: auto-discovery skipped — cannot resolve $HOME", "err", err)
		return nil
	}

	probes := []struct {
		format         string
		location       string
		probe          string
		requireRegular bool
	}{
		{
			format:   "aiagent_v3",
			location: filepath.Join(home, ".ai-agent", "sessions"),
			probe:    filepath.Join(home, ".ai-agent", "sessions", "session"),
		},
		{
			format:   "aiagent_v2",
			location: filepath.Join(home, ".ai-agent", "sessions"),
			probe:    filepath.Join(home, ".ai-agent", "sessions"),
		},
		{
			format:   "claude-code",
			location: claudeProjectsDir(home),
			probe:    claudeProjectsDir(home),
		},
		{
			format:   "codex",
			location: codexSessionsDir(home),
			probe:    codexSessionsDir(home),
		},
		{
			format:         "opencode",
			location:       opencodeDBPath(home),
			probe:          opencodeDBPath(home),
			requireRegular: true,
		},
	}

	var out []configuredSource
	seen := make(map[string]struct{}, len(probes))
	for _, p := range probes {
		info, err := os.Stat(p.probe)
		if err != nil {
			continue
		}
		if p.requireRegular && !info.Mode().IsRegular() {
			logger.Warn("ai-viewer-ingest: skipping opencode source — path is not a regular file",
				"format", p.format, "location", p.location)
			continue
		}
		key := p.format + ":" + p.location
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, configuredSource{id: key, format: p.format, location: p.location})
		attrs := []any{"format", p.format, "location", p.location}
		switch p.format {
		case "claude-code":
			attrs = append(attrs, "project_dirs", countProjectDirs(p.location))
		case "codex":
			attrs = append(attrs,
				"modern_rollouts", countRolloutFiles(p.location),
				"legacy_json", countLegacyJSON(p.location))
		case "opencode":
			probeCtx, cancelProbe := context.WithTimeout(context.Background(), opencodeProbeTimeout)
			sessions, messages, parts, latest, perr := opencode.ProbeStatus(probeCtx, p.location)
			cancelProbe()
			attrs = append(attrs,
				"sessions", sessions,
				"messages", messages,
				"parts", parts,
				"latest_migration", latest)
			if perr != nil {
				attrs = append(attrs, "probe_error", perr.Error())
			} else {
				out[len(out)-1].metaJSON = opencodeMetaJSON(sessions, messages, parts, latest)
			}
		}
		logger.Info("ai-viewer-ingest: auto-discovered source", attrs...)
	}
	return out
}
