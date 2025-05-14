package vsh

import (
	"context"
	"fmt"
	"io"
	iofs "io/fs"
	"maps"
	"os"

	"github.com/wzshiming/vsh/fs"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// A Runner interprets shell programs. It can be reused, but it is not safe for
// concurrent use. Use [NewRunner] to build a new Runner.
//
// Note that writes to Stdout and Stderr may be concurrent if background
// commands are used. If you plan on using an [io.Writer] implementation that
// isn't safe for concurrent use, consider a workaround like hiding writes
// behind a mutex.
//
// Runner's exported fields are meant to be configured via [runnerOption];
// once a Runner has been created, the fields should be treated as read-only.
type Runner struct {
	// Env specifies the initial environment for the interpreter, which must
	// not be nil. It can only be set via [Env].
	//
	// If it includes a TMPDIR variable describing an absolute directory,
	// it is used as the directory in which to create temporary files needed
	// for the interpreter's use, such as named pipes for process substitutions.
	Env expand.Environ

	writeEnv expand.WriteEnviron

	// Dir specifies the working directory of the command, which must be an
	// absolute path. It can only be set via [Dir].
	Dir string

	// Params are the current shell parameters, e.g. from running a shell
	// file or calling a function. Accessible via the $@/$* family of vars.
	// It can only be set via [Params].
	Params []string

	Vars  map[string]expand.Variable
	Funcs map[string]*syntax.Stmt

	TTY bool

	FileSystem fs.FileSystem

	Commands map[string]func(RunnerContext, []string) error

	alias map[string]alias

	stdin  *os.File // e.g. the read end of a pipe
	stdout io.Writer
	stderr io.Writer

	ecfg *expand.Config
	ectx context.Context // just so that Runner.Subshell can use it again

	lastExpandExit int // used to surface exit codes while expanding fields

	// didReset remembers whether the runner has ever been reset. This is
	// used so that Reset is automatically called when running any program
	// or node for the first time on a Runner.
	didReset bool

	filename string // only if Node was a File

	// >0 to break or continue out of N enclosing loops
	breakEnclosing, contnEnclosing int

	inLoop       bool
	inFunc       bool
	inSource     bool
	handlingTrap bool // whether we're currently in a trap callback

	// track if a sourced script set positional parameters
	sourceSetParams bool

	noErrExit bool

	fatalErr  error // current fatal error, e.g. from a handler
	returning bool  // whether the current function `return`ed
	exiting   bool  // whether the current shell `exit`ed

	// nonFatalHandlerErr is the current non-fatal error from a handler.
	// Used so that running a single statement with a custom handler
	// which returns a non-fatal Go error, such as a Go error wrapping [NewExitStatus],
	// can be returned by [Runner.Run] without being lost entirely.
	nonFatalHandlerErr error

	// The current and last exit status code. They can only be different if
	// the interpreter is in the middle of running a statement. In that
	// scenario, 'exit' is the status code for the statement being run, and
	// 'lastExit' corresponds to the previous statement that was run.
	exit     int
	lastExit int

	// bgProcs holds all background shells spawned by this runner.
	// Their PIDs are 1-indexed, from 1 to len(bgProcs), with a "g" prefix
	// to distinguish them from real PIDs on the host operating system.
	//
	// Note that each shell only tracks its direct children;
	// subshells do not share nor inherit the background PIDs they can wait for.
	bgProcs []bgProc

	opts runnerOpts

	origDir    string
	origParams []string
	origOpts   runnerOpts
	origStdin  *os.File
	origStdout io.Writer
	origStderr io.Writer

	// Most scripts don't use pushd/popd, so make space for the initial PWD
	// without requiring an extra allocation.
	dirStack     []string
	dirBootstrap [1]string

	optState getopts

	// keepRedirs is used so that "exec" can make any redirections
	// apply to the current shell, and not just the command.
	keepRedirs bool

	// Fake signal callbacks
	callbackErr  string
	callbackExit string
}

type bgProc struct {
	// closed when the background process finishes,
	// after which point the result fields below are set.
	done chan struct{}

	exit *int
}

type alias struct {
	args  []*syntax.Word
	blank bool
}

func (r *Runner) optByFlag(flag byte) *bool {
	for i, opt := range &shellOptsTable {
		if opt.flag == flag {
			return &r.opts[i]
		}
	}
	return nil
}

// NewRunner creates a new Runner, applying a number of options. If applying any of
// the options results in an error, it is returned.
//
// Any unset options fall back to their defaults. For example, not supplying the
// environment falls back to the process's environment, and not supplying the
// standard output writer means that the output will be discarded.
func NewRunner(opts ...runnerOption) (*Runner, error) {
	r := &Runner{
		FileSystem: fs.NewMemFS(),
		Dir:        "/",
		TTY:        true,
		Commands:   map[string]func(RunnerContext, []string) error{},
	}
	r.dirStack = r.dirBootstrap[:0]

	for _, opt := range opts {
		if err := opt(r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// runnerOption can be passed to [NewRunner] to alter a [Runner]'s behaviour.
// It can also be applied directly on an existing Runner,
// such as Params("-e")(runner).
// Note that options cannot be applied once Run or Reset have been called.
type runnerOption func(*Runner) error

// WithCommand adds a command to the interpreter's command table.
// The command will be executed when its name is invoked.
func WithCommand(name string, fn func(RunnerContext, []string) error) runnerOption {
	return func(r *Runner) error {
		r.Commands[name] = fn
		return nil
	}
}

// WithEnv sets the interpreter's environment.
func WithEnv(env expand.Environ) runnerOption {
	return func(r *Runner) error {
		r.Env = env
		return nil
	}
}

// WithDir sets the interpreter's working directory.
func WithDir(f fs.FileSystem, path string) runnerOption {
	return func(r *Runner) error {
		if path == "" {
			return nil
		}
		path := r.absPath(path)
		r.FileSystem = f

		info, err := iofs.Stat(r.FileSystem, path)
		if err != nil {
			return fmt.Errorf("could not stat: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", path)
		}
		r.Dir = path
		return nil
	}
}

// WithParams populates the shell options and parameters. For example, WithParams("-e",
// "--", "foo") will set the "-e" option and the parameters ["foo"], and
// WithParams("+e") will unset the "-e" option and leave the parameters untouched.
//
// This is similar to what the interpreter's "set" builtin does.
func WithParams(args ...string) runnerOption {
	return func(r *Runner) error {
		fp := flagParser{remaining: args}
		for fp.more() {
			flag := fp.flag()
			if flag == "-" {
				// TODO: implement "The -x and -v options are turned off."
				if args := fp.args(); len(args) > 0 {
					r.Params = args
				}
				return nil
			}
			enable := flag[0] == '-'
			if flag[1] != 'o' {
				opt := r.optByFlag(flag[1])
				if opt == nil {
					return fmt.Errorf("invalid option: %q", flag)
				}
				*opt = enable
				continue
			}
			value := fp.value()
			if value == "" && enable {
				for i, opt := range &shellOptsTable {
					r.printOptLine(opt.name, r.opts[i], true)
				}
				continue
			}
			if value == "" && !enable {
				for i, opt := range &shellOptsTable {
					setFlag := "+o"
					if r.opts[i] {
						setFlag = "-o"
					}
					r.outf("set %s %s\n", setFlag, opt.name)
				}
				continue
			}
			_, opt := r.optByName(value)
			if opt == nil {
				return fmt.Errorf("invalid option: %q", value)
			}
			*opt = enable
		}
		if args := fp.args(); args != nil {
			// If "--" wasn't given and there were zero arguments,
			// we don't want to override the current parameters.
			r.Params = args

			// Record whether a sourced script sets the parameters.
			if r.inSource {
				r.sourceSetParams = true
			}
		}
		return nil
	}
}

func stdinFile(r io.Reader) (*os.File, error) {
	switch r := r.(type) {
	case *os.File:
		return r, nil
	case nil:
		return nil, nil
	default:
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		go func() {
			io.Copy(pw, r)
			pw.Close()
		}()
		return pr, nil
	}
}

// WithStdIO configures an interpreter's standard input, standard output, and
// standard error. If out or err are nil, they default to a writer that discards
// the output.
//
// Note that providing a non-nil standard input other than [*os.File] will require
// an [os.Pipe] and spawning a goroutine to copy into it,
// as an [os.File] is the only way to share a reader with subprocesses.
// This may cause the interpreter to consume the entire reader.
// See [os/exec.Cmd.Stdin].
//
// When providing an [*os.File] as standard input, consider using an [os.Pipe]
// as it has the best chance to support cancellable reads via [os.File.SetReadDeadline],
// so that cancelling the runner's context can stop a blocked standard input read.
func WithStdIO(in io.Reader, out, err io.Writer) runnerOption {
	return func(r *Runner) error {
		stdin, _err := stdinFile(in)
		if _err != nil {
			return _err
		}
		r.stdin = stdin
		if out == nil {
			out = io.Discard
		}
		r.stdout = out
		if err == nil {
			err = io.Discard
		}
		r.stderr = err
		return nil
	}
}

// optByName returns the matching runner's option index and status
func (r *Runner) optByName(name string) (index int, status *bool) {
	for i, opt := range &shellOptsTable {
		if opt.name == name {
			return i, &r.opts[i]
		}
	}
	return 0, nil
}

type runnerOpts [len(shellOptsTable)]bool

type shellOpt struct {
	flag byte
	name string
}

var shellOptsTable = [...]shellOpt{
	// sorted alphabetically by name; use a space for the options
	// that have no flag form
	{'a', "allexport"},
	{'e', "errexit"},
	{'n', "noexec"},
	{'f', "noglob"},
	{'u', "nounset"},
	{'x', "xtrace"},
	{' ', "pipefail"},
}

// To access the shell options arrays without a linear search when we
// know which option we're after at compile time.
const (
	// These correspond to indexes in [shellOptsTable]
	optAllExport = iota
	optErrExit
	optNoExec
	optNoGlob
	optNoUnset
	optXTrace
	optPipeFail
)

// Reset returns a runner to its initial state, right before the first call to
// Run or Reset.
//
// Typically, this function only needs to be called if a runner is reused to run
// multiple programs non-incrementally. Not calling Reset between each run will
// mean that the shell state will be kept, including variables, options, and the
// current directory.
func (r *Runner) Reset() {
	if !r.didReset {
		r.origDir = r.Dir
		r.origParams = r.Params
		r.origOpts = r.opts
		r.origStdin = r.stdin
		r.origStdout = r.stdout
		r.origStderr = r.stderr
	}
	// reset the internal state
	*r = Runner{
		Env: r.Env,

		// These can be set by functions like [Dir] or [Params], but
		// builtins can overwrite them; reset the fields to whatever the
		// constructor set up.
		Dir:    r.origDir,
		Params: r.origParams,
		opts:   r.origOpts,
		stdin:  r.origStdin,
		stdout: r.origStdout,
		stderr: r.origStderr,

		origDir:    r.origDir,
		origParams: r.origParams,
		origOpts:   r.origOpts,
		origStdin:  r.origStdin,
		origStdout: r.origStdout,
		origStderr: r.origStderr,

		// emptied below, to reuse the space
		Vars: r.Vars,

		dirStack: r.dirStack[:0],

		TTY:        r.TTY,
		FileSystem: r.FileSystem,
		Commands:   r.Commands,
	}
	// Ensure we stop referencing any pointers before we reuse bgProcs.
	clear(r.bgProcs)
	r.bgProcs = r.bgProcs[:0]

	if r.Vars == nil {
		r.Vars = make(map[string]expand.Variable)
	} else {
		clear(r.Vars)
	}
	// TODO(v4): Use the supplied Env directly if it implements enough methods.
	r.writeEnv = &overlayEnviron{parent: r.Env}
	if !r.writeEnv.Get("HOME").IsSet() {
		r.setVarString("HOME", "/")
	}
	if !r.writeEnv.Get("UID").IsSet() {
		r.setVar("UID", expand.Variable{
			Set:      true,
			Kind:     expand.String,
			ReadOnly: true,
			Str:      "0",
		})
	}
	if !r.writeEnv.Get("EUID").IsSet() {
		r.setVar("EUID", expand.Variable{
			Set:      true,
			Kind:     expand.String,
			ReadOnly: true,
			Str:      "0",
		})
	}
	if !r.writeEnv.Get("GID").IsSet() {
		r.setVar("GID", expand.Variable{
			Set:      true,
			Kind:     expand.String,
			ReadOnly: true,
			Str:      "0",
		})
	}
	r.setVarString("PWD", r.Dir)
	r.setVarString("IFS", " \t\n")
	r.setVarString("OPTIND", "1")

	r.dirStack = append(r.dirStack, r.Dir)

	r.didReset = true
}

// ExitStatus is a non-zero status code resulting from running a shell node.
type ExitStatus uint8

func (s ExitStatus) Error() string { return fmt.Sprintf("exit status %d", s) }

// Run interprets a node, which can be a [*file], [*Stmt], or [Command]. If a non-nil
// error is returned, it will typically contain a command's exit status, which
// can be retrieved with [IsExitStatus].
//
// Run can be called multiple times synchronously to interpret programs
// incrementally. To reuse a [Runner] without keeping the internal shell state,
// call Reset.
//
// Calling Run on an entire [*file] implies an exit, meaning that an exit trap may
// run.
func (r *Runner) Run(ctx context.Context, node syntax.Node) error {
	if !r.didReset {
		r.Reset()
	}
	r.fillExpandConfig(ctx)
	r.fatalErr = nil
	r.returning = false
	r.exiting = false
	r.filename = ""
	switch node := node.(type) {
	case *syntax.File:
		r.filename = node.Name
		r.stmts(ctx, node.Stmts)
		if !r.exiting {
			r.exitShell(ctx, r.exit)
		}
	case *syntax.Stmt:
		r.stmt(ctx, node)
	case syntax.Command:
		r.cmd(ctx, node)
	default:
		return fmt.Errorf("node can only be File, Stmt, or Command: %T", node)
	}
	maps.Insert(r.Vars, r.writeEnv.Each)
	// Return the first of: a fatal error, a non-fatal handler error, or the exit code.
	if r.fatalErr != nil {
		return r.fatalErr
	}
	if r.nonFatalHandlerErr != nil {
		return r.nonFatalHandlerErr
	}
	if r.exit != 0 {
		return ExitStatus(r.exit)
	}
	return nil
}

// Exited reports whether the last Run call should exit an entire shell. This
// can be triggered by the "exit" built-in command, for example.
//
// Note that this state is overwritten at every Run call, so it should be
// checked immediately after each Run call.
func (r *Runner) Exited() bool {
	return r.exiting
}

func (r *Runner) FatalErr() error {
	return r.fatalErr
}

// Subshell makes a copy of the given [Runner], suitable for use concurrently
// with the original. The copy will have the same environment, including
// variables and functions, but they can all be modified without affecting the
// original.
//
// Subshell is not safe to use concurrently with [Run]. Orchestrating this is
// left up to the caller; no locking is performed.
//
// To replace e.g. stdin/out/err, do [WithStdIO](r.stdin, r.stdout, r.stderr)(r) on
// the copy.
func (r *Runner) Subshell() *Runner {
	return r.subshell(true)
}

// subshell is like [Runner.subshell], but allows skipping some allocations and copies
// when creating subshells which will not be used concurrently with the parent shell.
// TODO(v4): we should expose this, e.g. SubshellForeground and SubshellBackground.
func (r *Runner) subshell(background bool) *Runner {
	if !r.didReset {
		r.Reset()
	}
	// Keep in sync with the Runner type. Manually copy fields, to not copy
	// sensitive ones like [errgroup.Group], and to do deep copies of slices.
	r2 := &Runner{
		Dir:      r.Dir,
		Params:   r.Params,
		stdin:    r.stdin,
		stdout:   r.stdout,
		stderr:   r.stderr,
		filename: r.filename,
		opts:     r.opts,
		exit:     r.exit,
		lastExit: r.lastExit,

		origStdout: r.origStdout, // used for process substitutions

		TTY:        r.TTY,
		Commands:   r.Commands,
		FileSystem: r.FileSystem,
	}
	r2.writeEnv = newOverlayEnviron(r.writeEnv, background)
	// Funcs are copied, since they might be modified.
	r2.Funcs = maps.Clone(r.Funcs)
	r2.Vars = make(map[string]expand.Variable)
	r2.alias = maps.Clone(r.alias)

	r2.dirStack = append(r2.dirBootstrap[:0], r.dirStack...)
	r2.fillExpandConfig(r.ectx)
	r2.didReset = true
	return r2
}
