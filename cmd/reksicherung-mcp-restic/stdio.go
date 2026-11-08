package main

import (
	"bufio"
	"context"
	"errors"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var errMCPFrameTooLarge = errors.New("MCP stdio frame exceeds the configured size limit")

type boundedFrameReader struct {
	reader     *bufio.Reader
	closer     io.Closer
	maxBytes   int
	frameBytes int
	pending    []byte
	pendingErr error
}

func newBoundedFrameReader(source io.ReadCloser, maxBytes int) *boundedFrameReader {
	return &boundedFrameReader{
		reader:   bufio.NewReader(source),
		closer:   source,
		maxBytes: maxBytes,
	}
}

func (reader *boundedFrameReader) Read(buffer []byte) (int, error) {
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
		return 0, errMCPFrameTooLarge
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

func (reader *boundedFrameReader) copyPending(buffer []byte) (int, error) {
	n := copy(buffer, reader.pending)
	reader.pending = reader.pending[n:]
	return n, nil
}

func (reader *boundedFrameReader) Close() error {
	return reader.closer.Close()
}

func serve(ctx context.Context, instance service, stdin io.ReadCloser, stdout io.WriteCloser) error {
	server := newMCPServer(instance)
	transport := &mcp.IOTransport{
		Reader: newBoundedFrameReader(stdin, maxMCPFrameSize),
		Writer: stdout,
	}
	if err := server.Run(ctx, transport); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("MCP stdio server failed")
	}
	return nil
}
