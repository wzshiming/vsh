package builtin

import (
	"fmt"
	"strconv"
	"time"

	"github.com/wzshiming/vsh"
)

func Sleep(hc vsh.RunnerContext, args []string) error {
	for _, arg := range args {
		d, err := time.ParseDuration(arg)
		if err != nil {
			i, err := strconv.ParseInt(arg, 0, 0)
			if err != nil {
				fmt.Fprintf(hc.Stderr, "sleep: invalid time interval '%s'", arg)
				return nil
			}
			d = time.Duration(i) * time.Second
		}
		time.Sleep(d)
	}
	return nil
}
