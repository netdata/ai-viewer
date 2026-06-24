package codex

import (
	"fmt"
	"os"
)

func skipUnchangedEOFFinalizedRollout(r rollout, cur FileCursor) (bool, error) {
	if cur.Offset == 0 || cur.Size == 0 || cur.EOFFinalizedSize == 0 || cur.MtimeUs == 0 {
		return false, nil
	}
	info, err := os.Stat(r.abs)
	if err != nil {
		return false, fmt.Errorf("stat %s for consumed cursor check: %w", r.rel, err)
	}
	size := info.Size()
	if cur.Offset != size || cur.Size != size || cur.EOFFinalizedSize != size {
		return false, nil
	}
	return cur.MtimeUs == info.ModTime().UnixMicro(), nil
}
