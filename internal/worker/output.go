package worker

import "bytes"

const outputLimit = 4 << 20

type limitedOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *limitedOutput) String() string { return b.buffer.String() }

func (b *limitedOutput) Write(data []byte) (int, error) {
	n := min(len(data), outputLimit-b.buffer.Len())
	if n < len(data) {
		b.truncated = true
	}
	_, _ = b.buffer.Write(data[:n])
	return len(data), nil
}
