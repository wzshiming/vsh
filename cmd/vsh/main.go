package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/wzshiming/vsh"
	"github.com/wzshiming/vsh/builtin"
	"golang.org/x/term"
	"mvdan.cc/sh/v3/syntax"
)

var command = flag.String("c", "", "command to be executed")

func main() {
	flag.Parse()
	err := runAll()
	var es vsh.ExitStatus
	if errors.As(err, &es) {
		os.Exit(int(es))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runAll() error {
	r, err := vsh.NewRunner(
		vsh.WithStdIO(os.Stdin, os.Stdout, os.Stderr),
		vsh.WithCommand("ls", builtin.Ls),
		vsh.WithCommand("cat", builtin.Cat),
		vsh.WithCommand("mkdir", builtin.Mkdir),
		vsh.WithCommand("rm", builtin.Rm),
		vsh.WithCommand("date", builtin.Date),
		vsh.WithCommand("sleep", builtin.Sleep),
	)
	if err != nil {
		return err
	}
	ctx := context.Background()

	if *command != "" {
		return run(ctx, r, strings.NewReader(*command), "")
	}
	if flag.NArg() == 0 {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			return runInteractive(ctx, r, os.Stdin, os.Stdout, os.Stderr)
		}
		return run(ctx, r, os.Stdin, "")
	}
	for _, path := range flag.Args() {
		if err := runPath(ctx, r, path); err != nil {
			return err
		}
	}
	return nil
}

func run(ctx context.Context, r *vsh.Runner, reader io.Reader, name string) error {
	prog, err := syntax.NewParser().Parse(reader, name)
	if err != nil {
		return err
	}
	r.Reset()
	return r.Run(ctx, prog)
}

func runPath(ctx context.Context, r *vsh.Runner, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return run(ctx, r, f, path)
}

func runInteractive(ctx context.Context, r *vsh.Runner, stdin io.Reader, stdout, stderr io.Writer) error {
	parser := syntax.NewParser()
	fmt.Fprintf(stdout, "$ ")
	var runErr error
	fn := func(stmts []*syntax.Stmt) bool {
		if parser.Incomplete() {
			fmt.Fprintf(stdout, "> ")
			return true
		}
		ctx := context.Background()
		for _, stmt := range stmts {
			runErr = r.Run(ctx, stmt)
			if r.Exited() {
				return false
			}

			if err := r.FatalErr(); err != nil {
				fmt.Fprintf(stderr, "%s", err.Error())
				return false
			}

		}
		fmt.Fprintf(stdout, "$ ")
		return true
	}
	if err := parser.Interactive(stdin, fn); err != nil {
		return err
	}
	return runErr
}
