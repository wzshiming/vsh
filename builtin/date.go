package builtin

import (
	"io"
	"time"

	"github.com/wzshiming/vsh"
)

func Date(hc vsh.RunnerContext, s []string) error {
	_, _ = io.WriteString(hc.Stdout, time.Now().UTC().Format(time.UnixDate)+"\n")
	return nil
}
