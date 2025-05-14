package builtin

import (
	"fmt"
	"path"

	"github.com/wzshiming/vsh"
)

func Mkdir(hc vsh.RunnerContext, args []string) error {
	for _, arg := range args {
		if arg == "-p" {
			continue
		}
		if err := hc.FileSytem.MkdirAll(path.Join(hc.Dir, arg), 0777); err != nil {
			fmt.Fprintf(hc.Stderr, "mkdir: %s: %v\n", arg, err)
			return nil
		}
	}
	return nil
}
