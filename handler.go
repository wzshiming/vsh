package vsh

import (
	"context"
	"fmt"
	"io"
	"os"
	filepath "path"
	"strings"

	"github.com/wzshiming/vsh/fs"
	"mvdan.cc/sh/v3/expand"
)

// RunnerContext is the data passed to all the handler functions via [context.WithValue].
// It contains some of the current state of the [Runner].
type RunnerContext struct {
	Context context.Context
	// Env is a read-only version of the interpreter's environment,
	// including environment variables, global variables, and local function
	// variables.
	Env expand.Environ

	FileSytem fs.FileSystem

	Command func(ctx context.Context, args []string)

	TTY bool

	// Dir is the interpreter's current directory.
	Dir string

	// TODO(v4): use an os.File for stdin below directly.

	// Stdin is the interpreter's current standard input reader.
	// It is always an [*os.File], but the type here remains an [io.Reader]
	// due to backwards compatibility.
	Stdin io.Reader
	// Stdout is the interpreter's current standard output writer.
	Stdout io.Writer
	// Stderr is the interpreter's current standard error writer.
	Stderr io.Writer
}

func checkStat(dir, file string) (string, error) {
	if !filepath.IsAbs(file) {
		file = filepath.Join(dir, file)
	}
	info, err := os.Stat(file)
	if err != nil {
		return "", err
	}
	m := info.Mode()
	if m.IsDir() {
		return "", fmt.Errorf("is a directory")
	}
	if m&0o111 == 0 {
		return "", fmt.Errorf("permission denied")
	}
	return file, nil
}

func lookPathDir(cwd string, env expand.Environ, file string) (string, error) {
	pathList := strings.Split(env.Get("PATH").String(), ":")
	if len(pathList) == 0 {
		pathList = []string{""}
	}

	for _, elem := range pathList {
		var path string
		switch elem {
		case "", ".":
			// otherwise "foo" won't be "./foo"
			path = "./" + file
		default:
			path = filepath.Join(elem, file)
		}
		if f, err := checkStat(cwd, path); err == nil {
			return f, nil
		}
	}
	return "", fmt.Errorf("%q: executable file not found in $PATH", file)
}
