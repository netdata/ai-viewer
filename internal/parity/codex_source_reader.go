package parity

import (
	"bufio"
	"errors"
	"fmt"
)

const codexSourceLineMax = 16 * 1024 * 1024

func readCodexSourceLine(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 256)
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > codexSourceLineMax {
			return nil, fmt.Errorf("line exceeds %d bytes", codexSourceLineMax)
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
