package builtin

import (
	"fmt"
	"io"
	"path"

	"github.com/wzshiming/vsh"
)

func Cat(hc vsh.RunnerContext, args []string) error {
	if len(args) == 0 {
		if hc.Stdin == nil || hc.Stdout == nil {
			return nil
		}
		_, err := io.Copy(hc.Stdout, hc.Stdin)
		return err
	}
	for _, arg := range args {
		f, err := hc.FileSytem.Open(path.Join(hc.Dir, arg))
		if err != nil {
			fmt.Fprintf(hc.Stderr, "cat: %s: %v\n", arg, err)
			return nil
		}
		fi, err := f.Stat()
		if err != nil {
			fmt.Fprintf(hc.Stderr, "cat: %s: %v\n", arg, err)
			f.Close()
			return nil
		}
		if fi.IsDir() {
			fmt.Fprintf(hc.Stderr, "cat: %s: is a directory\n", arg)
			f.Close()
			return nil
		}

		_, err = io.Copy(hc.Stdout, f)
		f.Close()
		if err != nil {
			fmt.Fprintf(hc.Stderr, "cat file: %s: %v\n", arg, err)
			return nil
		}
	}
	return nil
}
