package builtin

import (
	"fmt"
	"io/fs"

	"github.com/wzshiming/vsh"
)

func Ls(hc vsh.RunnerContext, arg []string) error {
	dir := "."
	args := arg

	if len(args) > 0 {
		dir = args[0]
	}

	entries, err := fs.ReadDir(hc.FileSytem, dir)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "ls: %s: %v\n", dir, err)
		return nil
	}

	for _, entry := range entries {
		name := entry.Name()
		fmt.Fprintln(hc.Stdout, name)
	}
	return nil
}
