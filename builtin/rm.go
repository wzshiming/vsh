package builtin

import (
	"fmt"
	"path"

	"github.com/wzshiming/vsh"
)

func Rm(hc vsh.RunnerContext, args []string) error {
	for _, arg := range args {
		if arg == "-r" {
			continue
		}
		if err := hc.FileSytem.RemoveAll(path.Join(hc.Dir, arg)); err != nil {
			fmt.Fprintf(hc.Stderr, "rm: %s: %v\n", arg, err)
			return nil
		}
	}
	return nil
}
