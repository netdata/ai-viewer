package paritycheck

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const frozenSourceDirName = "source"

type sourceRunCapture struct {
	snapshot              sourceSnapshot
	readSource            Source
	verifyAfterExtraction bool
	cleanup               func()
}

func captureSourceForRun(ctx context.Context, opts Options, source Source, existingDB *sql.DB) (sourceRunCapture, error) {
	capture := sourceRunCapture{
		readSource:            source,
		verifyAfterExtraction: true,
		cleanup:               func() {},
	}
	if existingDB != nil || !filesystemBackedSource(source.Format) {
		snapshot, err := captureSourceSnapshot(ctx, source)
		capture.snapshot = snapshot
		return capture, err
	}

	snapshot, frozenSource, cleanup, err := freezeSourceSnapshot(ctx, opts.WorkDir, source)
	capture.snapshot = snapshot
	capture.readSource = frozenSource
	capture.verifyAfterExtraction = false
	capture.cleanup = cleanup
	return capture, err
}

func filesystemBackedSource(format string) bool {
	switch format {
	case "aiagent_v2", "aiagent_v3", "claude-code", "codex":
		return true
	default:
		return false
	}
}

func freezeSourceSnapshot(ctx context.Context, workDir string, source Source) (sourceSnapshot, Source, func(), error) {
	root, err := filepath.EvalSymlinks(source.Location)
	if err != nil {
		return sourceSnapshot{}, Source{}, func() {}, fmt.Errorf("resolve source snapshot root: %w", err)
	}
	workRoot, cleanup, err := prepareWorkDir(workDir)
	if err != nil {
		return sourceSnapshot{}, Source{}, func() {}, err
	}

	frozenRoot := filepath.Join(workRoot, frozenSourceDirName)
	files, frozenLocation, err := copySourceSnapshot(ctx, root, frozenRoot)
	if err != nil {
		cleanup()
		return sourceSnapshot{}, Source{}, func() {}, err
	}

	frozenSource := source
	frozenSource.Location = frozenLocation
	return sourceSnapshot{
		Root:  root,
		Files: files,
	}, frozenSource, cleanup, nil
}

func copySourceSnapshot(ctx context.Context, root string, frozenRoot string) (map[string]sourceSnapshotFile, string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, "", fmt.Errorf("stat source snapshot root: %w", err)
	}
	files := map[string]sourceSnapshotFile{}
	if info.Mode().IsRegular() {
		if err := os.MkdirAll(frozenRoot, 0o700); err != nil {
			return nil, "", fmt.Errorf("create frozen source snapshot directory: %w", err)
		}
		sourceRoot, err := os.OpenRoot(filepath.Dir(root))
		if err != nil {
			return nil, "", fmt.Errorf("open source snapshot parent root: %w", err)
		}
		defer func() { _ = sourceRoot.Close() }()
		frozenDir, err := os.OpenRoot(frozenRoot)
		if err != nil {
			return nil, "", fmt.Errorf("open frozen source snapshot root: %w", err)
		}
		defer func() { _ = frozenDir.Close() }()
		frozenName := filepath.Base(root)
		fingerprint, err := copySourceSnapshotFile(ctx, sourceRoot, filepath.Base(root), frozenDir, frozenName, ".")
		if err != nil {
			return nil, "", err
		}
		files["."] = fingerprint
		return files, filepath.Join(frozenRoot, frozenName), nil
	}
	if !info.IsDir() {
		return nil, "", fmt.Errorf("source snapshot root is not a regular file or directory")
	}
	if err := os.MkdirAll(frozenRoot, 0o700); err != nil {
		return nil, "", fmt.Errorf("create frozen source snapshot directory: %w", err)
	}
	sourceRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", fmt.Errorf("open source snapshot root: %w", err)
	}
	defer func() { _ = sourceRoot.Close() }()
	frozenDir, err := os.OpenRoot(frozenRoot)
	if err != nil {
		return nil, "", fmt.Errorf("open frozen source snapshot root: %w", err)
	}
	defer func() { _ = frozenDir.Close() }()

	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("walk source snapshot tree: %w", walkErr)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat source snapshot file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("rel source snapshot file: %w", err)
		}
		rel = filepath.ToSlash(rel)
		fingerprint, err := copySourceSnapshotFile(ctx, sourceRoot, filepath.FromSlash(rel), frozenDir, filepath.FromSlash(rel), rel)
		if err != nil {
			return err
		}
		files[rel] = fingerprint
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return files, frozenRoot, nil
}

func copySourceSnapshotFile(
	ctx context.Context,
	sourceRoot *os.Root,
	sourceName string,
	frozenRoot *os.Root,
	frozenName string,
	rel string,
) (sourceSnapshotFile, error) {
	before, err := sourceRoot.Stat(sourceName)
	if err != nil {
		return sourceSnapshotFile{}, fmt.Errorf("stat source snapshot file %s: %w", rel, err)
	}
	if !before.Mode().IsRegular() {
		return sourceSnapshotFile{}, fmt.Errorf("source snapshot file %s is not regular", rel)
	}
	if err := frozenRoot.MkdirAll(filepath.Dir(frozenName), 0o700); err != nil {
		return sourceSnapshotFile{}, fmt.Errorf("create frozen source snapshot parent %s: %w", rel, err)
	}

	in, err := sourceRoot.Open(sourceName)
	if err != nil {
		return sourceSnapshotFile{}, fmt.Errorf("open source snapshot file %s read-only: %w", rel, err)
	}
	defer func() { _ = in.Close() }()
	out, err := frozenRoot.OpenFile(frozenName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return sourceSnapshotFile{}, fmt.Errorf("create frozen source snapshot file %s: %w", rel, err)
	}
	defer func() { _ = out.Close() }()

	hash := sha256.New()
	buf := make([]byte, sourceSnapshotHashBufferSize)
	remaining := before.Size()
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return sourceSnapshotFile{}, err
		}
		chunk := len(buf)
		if remaining < int64(chunk) {
			chunk = int(remaining)
		}
		n, readErr := in.Read(buf[:chunk])
		if n > 0 {
			if written, err := out.Write(buf[:n]); err != nil {
				return sourceSnapshotFile{}, fmt.Errorf("write frozen source snapshot file %s: %w", rel, err)
			} else if written != n {
				return sourceSnapshotFile{}, fmt.Errorf("write frozen source snapshot file %s: %w", rel, io.ErrShortWrite)
			}
			if _, err := hash.Write(buf[:n]); err != nil {
				return sourceSnapshotFile{}, fmt.Errorf("hash source snapshot file %s: %w", rel, err)
			}
			remaining -= int64(n)
		}
		if readErr != nil {
			if readErr == io.EOF && remaining == 0 {
				break
			}
			if readErr == io.EOF {
				return sourceSnapshotFile{}, fmt.Errorf("source snapshot file changed while freezing: %s", rel)
			}
			return sourceSnapshotFile{}, fmt.Errorf("read source snapshot file %s: %w", rel, readErr)
		}
		if n == 0 {
			return sourceSnapshotFile{}, fmt.Errorf("read source snapshot file %s made no progress", rel)
		}
	}
	if err := out.Close(); err != nil {
		return sourceSnapshotFile{}, fmt.Errorf("close frozen source snapshot file %s: %w", rel, err)
	}
	if err := in.Close(); err != nil {
		return sourceSnapshotFile{}, fmt.Errorf("close source snapshot file %s: %w", rel, err)
	}

	after, err := sourceRoot.Stat(sourceName)
	if err != nil {
		return sourceSnapshotFile{}, fmt.Errorf("stat source snapshot file %s after copy: %w", rel, err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return sourceSnapshotFile{}, fmt.Errorf("source snapshot file changed while freezing: %s", rel)
	}
	return sourceSnapshotFile{
		Size:      before.Size(),
		MtimeNS:   before.ModTime().UnixNano(),
		SHA256Hex: fmt.Sprintf("%x", hash.Sum(nil)),
	}, nil
}
