package parity

import (
	"bufio"
	"errors"
	"fmt"
)

const canonicalLineSelectorMax = 16 * 1024 * 1024

func readCanonicalLineSelectorLine(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 256)
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > canonicalLineSelectorMax {
			return nil, fmt.Errorf("line exceeds %d bytes", canonicalLineSelectorMax)
		}
		if err == nil {
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}
