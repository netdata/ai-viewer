package paritycheck

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const sourceSnapshotHashBufferSize = 128 * 1024

type sourceSnapshot struct {
	Root  string                        `json:"root"`
	Files map[string]sourceSnapshotFile `json:"files"`
}

type sourceSnapshotFile struct {
	Size      int64  `json:"size"`
	MtimeNS   int64  `json:"mtime_ns"`
	SHA256Hex string `json:"sha256"`
}

func captureSourceSnapshot(ctx context.Context, source Source) (sourceSnapshot, error) {
	if source.Location == "" {
		return sourceSnapshot{}, fmt.Errorf("missing source location")
	}
	root, err := filepath.EvalSymlinks(source.Location)
	if err != nil {
		return sourceSnapshot{}, fmt.Errorf("resolve source snapshot root: %w", err)
	}
	files, err := collectSourceSnapshotFiles(ctx, root)
	if err != nil {
		return sourceSnapshot{}, err
	}
	return sourceSnapshot{
		Root:  root,
		Files: files,
	}, nil
}

func (s sourceSnapshot) Verify(ctx context.Context) error {
	current, err := collectSourceSnapshotFiles(ctx, s.Root)
	if err != nil {
		return err
	}

	var added, removed, modified int
	for path, before := range s.Files {
		after, ok := current[path]
		if !ok {
			removed++
			continue
		}
		if before != after {
			modified++
		}
	}
	for path := range current {
		if _, ok := s.Files[path]; !ok {
			added++
		}
	}
	if added == 0 && removed == 0 && modified == 0 {
		return nil
	}
	return fmt.Errorf("source snapshot mutated: added=%d removed=%d modified=%d", added, removed, modified)
}

func collectSourceSnapshotFiles(ctx context.Context, root string) (map[string]sourceSnapshotFile, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat source snapshot root: %w", err)
	}
	files := map[string]sourceSnapshotFile{}
	if info.Mode().IsRegular() {
		rootDir, err := os.OpenRoot(filepath.Dir(root))
		if err != nil {
			return nil, fmt.Errorf("open source snapshot parent root: %w", err)
		}
		defer func() { _ = rootDir.Close() }()
		file, err := rootDir.Open(filepath.Base(root))
		if err != nil {
			return nil, fmt.Errorf("open source snapshot file read-only: %w", err)
		}
		fingerprint, err := fingerprintSourceSnapshotFile(ctx, file, info)
		if err != nil {
			return nil, err
		}
		files["."] = fingerprint
		return files, nil
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source snapshot root is not a regular file or directory")
	}
	rootDir, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open source snapshot root: %w", err)
	}
	defer func() { _ = rootDir.Close() }()

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
		file, err := rootDir.Open(rel)
		if err != nil {
			return fmt.Errorf("open source snapshot file read-only: %w", err)
		}
		fingerprint, err := fingerprintSourceSnapshotFile(ctx, file, info)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = fingerprint
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func fingerprintSourceSnapshotFile(ctx context.Context, file *os.File, info os.FileInfo) (sourceSnapshotFile, error) {
	defer func() { _ = file.Close() }()

	hash := sha256.New()
	buf := make([]byte, sourceSnapshotHashBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return sourceSnapshotFile{}, err
		}
		n, readErr := file.Read(buf)
		if n > 0 {
			if _, err := hash.Write(buf[:n]); err != nil {
				return sourceSnapshotFile{}, fmt.Errorf("hash source snapshot file: %w", err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return sourceSnapshotFile{}, fmt.Errorf("read source snapshot file: %w", readErr)
		}
	}
	return sourceSnapshotFile{
		Size:      info.Size(),
		MtimeNS:   info.ModTime().UnixNano(),
		SHA256Hex: fmt.Sprintf("%x", hash.Sum(nil)),
	}, nil
}
