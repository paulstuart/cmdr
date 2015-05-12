package cmdr

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Param [2]string

// only the data needed to know how run a local command
type Command struct {
	Path, Params string // path to executable, param template
	Dir, User    string // optional working dir, user to run as
	Async        bool
}

func (c Command) Render(params ...Param) (string, error) {
	text := c.Params
	for _, p := range params {
		if len(p[0]) > 0 {
			t := "{{" + p[0] + "}}"
			text = strings.Replace(text, t, p[1], -1)
		} else {
			text += " " + p[1]
		}
	}
	return optional(text)
}

var (
	mtx       sync.Mutex
	sessionID int64
	pmatch, _ = regexp.Compile("{{[A-Z]+}}")
	globs, _  = regexp.Compile("[\\w.-]*\\*[\\w.-]*")
)

var (
	ErrIncomplete  = errors.New("missing required parameter")
	ErrUserDenied  = errors.New("user denied runtime access")
	ErrSyntaxError = errors.New("invalid command syntax")
	ErrNoSuchFile  = errors.New("no such file or directory")
	ErrMustBeRoot  = errors.New("must be run as root")
)

func nextID() int64 {
	mtx.Lock()
	sessionID++
	mtx.Unlock()
	return sessionID
}

type Runtime struct {
	SID                  int64
	PID, RC              int
	Cmd, Stdout, Stderr  string
	SystemTime, UserTime time.Duration
	Started, Finished    time.Time
}

var rFormat = `
CMD: %s 
SID: %d 
PID: %d 
RC : %d
OUT: %s
ERR: %s
SYS: %s
USR: %s
CLK: %s
`

func readString(r io.Reader) (string, error) {
	b, err := ioutil.ReadAll(r)
	return string(b), err
}

func (r Runtime) String() string {
	return fmt.Sprintf(rFormat, r.Cmd, r.SID, r.PID, r.RC, r.Stdout, r.Stderr, r.SystemTime, r.UserTime, r.Finished.Sub(r.Started))
}

func (c Command) Runner(rt chan Runtime, params ...Param) error {
	r := Runtime{}
	defer func() {
		rt <- r
	}()
	path, err := exec.LookPath(c.Path)
	if err != nil {
		return err
	}
	text, err := c.Render(params...)
	if err != nil {
		return err
	}
	r.Cmd = path
	if len(text) > 0 {
		r.Cmd += " " + text
	}

	errOut, errIn, err := os.Pipe()
	if err != nil {
		return err
	}

	outOut, outIn, err := os.Pipe()
	if err != nil {
		return err
	}
	if !c.Async {
		defer errOut.Close()
		defer outOut.Close()
	}

	attr := &os.ProcAttr{}
	attr.Files = []*os.File{nil, outIn, errIn}
	if len(c.Dir) > 0 {
		attr.Dir = c.Dir
	}
	if len(c.User) > 0 {
		if os.Getuid() != 0 {
			return ErrMustBeRoot
		}
		u, err := user.Lookup(c.User)
		if err != nil {
			return err
		}
		uid, err := strconv.ParseUint(u.Uid, 0, 32)
		if err != nil {
			return err
		}
		creds := &syscall.Credential{Uid: uint32(uid)}
		attr.Sys = &syscall.SysProcAttr{Credential: creds}
	}
	r.SID = nextID()
	p, err := os.StartProcess(path, strings.Fields(r.Cmd), attr)
	if err != nil {
		return err
	}
	r.Started = time.Now()

	finisher := func() error {
		s, err := p.Wait()
		r.Finished = time.Now()
		if c.Async {
			defer errOut.Close()
			defer outOut.Close()
		}
		outIn.Close()
		errIn.Close()
		if r.Stdout, err = readString(outOut); err != nil {
			return err
		}
		if r.Stderr, err = readString(errOut); err != nil {
			return err
		}
		r.PID = s.Pid()
		e := s.Sys().(syscall.WaitStatus)
		r.RC = e.ExitStatus()
		r.UserTime = s.UserTime()
		if c.Async {
			rt <- r
		}
		return nil
	}

	if c.Async {
		go finisher()
	} else {
		err = finisher()
	}
	return err
}

func (c Command) Run(params ...Param) (Runtime, error) {
	rt := make(chan Runtime, 1)
	err := c.Runner(rt, params...)
	return <-rt, err
}

func (c Command) RunAsync(rt chan Runtime, params ...Param) error {
	c.Async = true
	return c.Runner(rt, params...)
}

func (c Command) Background(params ...Param) (int, error) {
	cmd := exec.Command(c.Path)
	err := cmd.Start()
	var pid int
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	return pid, err
}

// remove 'optional' syntax and unused optional parameters
func optional(text string) (string, error) {
	for {
		start := strings.Index(text, "[")
		if start < 0 {
			break
		}
		end := strings.Index(text, "]")
		if end < 0 || end < start {
			return text, ErrSyntaxError
		}
		if pmatch.MatchString(text) {
			// remove unused args
			text = text[:start] + text[end+1:]
		} else {
			// remove opt brackets
			text = text[:start] + text[start+1:end] + text[end+1:]
		}
	}
	if pmatch.MatchString(text) {
		return text, ErrIncomplete
	}
	text = os.ExpandEnv(text)
	for _, g := range globs.FindAllString(text, -1) {
		files, err := filepath.Glob(g)
		if err != nil {
			return text, err
		}
		if len(files) == 0 {
			return text, ErrNoSuchFile
		}
		text = strings.Replace(text, g, strings.Join(files, " "), -1)
	}
	return text, nil
}
