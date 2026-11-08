package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"

	resticrestore "github.com/1k-off/reksicherung/internal/reksicherung/restore/restic"
)

type resticProcessRunner struct{}

func (resticProcessRunner) Run(ctx context.Context, invocation resticrestore.Invocation) ([]byte, error) {
	if ctx == nil || invocation.Executable == "" || invocation.MaxOutputBytes < 1 ||
		invocation.MaxOutputBytes > 16<<20 {
		return nil, resticrestore.ErrInvalidConfig
	}
	command := exec.CommandContext(ctx, invocation.Executable, invocation.Args...)
	command.Env = resticCommandEnvironment(invocation.Environment)
	var output bytes.Buffer
	command.Stdout = &boundedResticOutput{buffer: &output, limit: int(invocation.MaxOutputBytes)}
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return nil, errors.New("restic command failed")
	}
	return output.Bytes(), nil
}

func (resticProcessRunner) Stream(ctx context.Context, invocation resticrestore.Invocation) (io.ReadCloser, error) {
	if ctx == nil || invocation.Executable == "" || invocation.MaxOutputBytes < 1 {
		return nil, resticrestore.ErrInvalidConfig
	}
	command := exec.CommandContext(ctx, invocation.Executable, invocation.Args...)
	command.Env = resticCommandEnvironment(invocation.Environment)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, errors.New("restic output stream unavailable")
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, errors.New("restic command failed")
	}
	return &resticProcessStream{
		command: command, stdout: stdout, remaining: invocation.MaxOutputBytes,
	}, nil
}

func resticCommandEnvironment(values []resticrestore.EnvironmentVariable) []string {
	environment := []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}
	for _, value := range values {
		environment = append(environment, value.Name+"="+value.Value)
	}
	return environment
}

type boundedResticOutput struct {
	buffer *bytes.Buffer
	limit  int
}

func (output *boundedResticOutput) Write(data []byte) (int, error) {
	if len(data) > output.limit-output.buffer.Len() {
		return 0, resticrestore.ErrOutputLimit
	}
	return output.buffer.Write(data)
}

type resticProcessStream struct {
	command   *exec.Cmd
	stdout    io.ReadCloser
	remaining int64
	waitErr   error
	waitOnce  sync.Once
	stopOnce  sync.Once
}

func (stream *resticProcessStream) Read(buffer []byte) (int, error) {
	if int64(len(buffer)) > stream.remaining+1 {
		buffer = buffer[:stream.remaining+1]
	}
	n, err := stream.stdout.Read(buffer)
	stream.remaining -= int64(n)
	if stream.remaining < 0 {
		stream.stop()
		return n - 1, resticrestore.ErrOutputLimit
	}
	if errors.Is(err, io.EOF) {
		stream.wait()
		if stream.waitErr != nil {
			return n, resticrestore.ErrCommandFailed
		}
		return n, io.EOF
	}
	if err != nil {
		stream.stop()
		return n, resticrestore.ErrCommandFailed
	}
	return n, nil
}

func (stream *resticProcessStream) Close() error {
	stream.stop()
	return nil
}

func (stream *resticProcessStream) stop() {
	stream.stopOnce.Do(func() {
		_ = stream.stdout.Close()
		if stream.command.Process != nil {
			_ = stream.command.Process.Kill()
		}
		stream.wait()
	})
}

func (stream *resticProcessStream) wait() {
	stream.waitOnce.Do(func() {
		stream.waitErr = stream.command.Wait()
	})
}
