package main

import (
	"bufio"
	"errors"
	"io"
)

var errRestoreFrameTooLarge = errors.New("MCP frame exceeds configured size limit")

type restoreFrameReader struct {
	reader     *bufio.Reader
	closer     io.Closer
	maxBytes   int
	frameBytes int
	pending    []byte
	pendingErr error
}

func newRestoreFrameReader(source io.ReadCloser, maxBytes int) *restoreFrameReader {
	return &restoreFrameReader{reader: bufio.NewReader(source), closer: source, maxBytes: maxBytes}
}

func (reader *restoreFrameReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if len(reader.pending) > 0 {
		return reader.copyPending(buffer)
	}
	if reader.pendingErr != nil {
		err := reader.pendingErr
		reader.pendingErr = nil
		return 0, err
	}
	chunk, err := reader.reader.ReadSlice('\n')
	if len(chunk) > reader.maxBytes-reader.frameBytes {
		return 0, errRestoreFrameTooLarge
	}
	reader.frameBytes += len(chunk)
	if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
		reader.frameBytes = 0
	}
	if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
		reader.pendingErr = err
	}
	if len(chunk) == 0 {
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			reader.pendingErr = nil
			return 0, err
		}
		return 0, nil
	}
	reader.pending = chunk
	return reader.copyPending(buffer)
}

func (reader *restoreFrameReader) copyPending(buffer []byte) (int, error) {
	n := copy(buffer, reader.pending)
	reader.pending = reader.pending[n:]
	return n, nil
}

func (reader *restoreFrameReader) Close() error {
	return reader.closer.Close()
}
