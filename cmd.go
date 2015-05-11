package cmdr

import (
	"errors"
	"fmt"
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

type Command struct {
	Path, Params string // path to executable, param template
	Dir, User    string // optional working dir, user to run as
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
	ErrBadCommand    = errors.New("invalid command ID")
	ErrCommandDenied = errors.New("command access denied")
	ErrHostDenied    = errors.New("host access denied")
	ErrIncomplete    = errors.New("missing required parameter")
	ErrUserDenied    = errors.New("user denied runtime access")
	ErrSyntaxError   = errors.New("invalid command syntax")
	ErrNoSuchFile    = errors.New("no such file or directory")
	ErrMustBeRoot    = errors.New("must be run as root")
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

func (r Runtime) String() string {
	return fmt.Sprintf(rFormat, r.Cmd, r.SID, r.PID, r.RC, r.Stdout, r.Stderr, r.SystemTime, r.UserTime, r.Finished.Sub(r.Started))
}

func (c Command) Run(params ...Param) (Runtime, error) {
	r := Runtime{}
	path, err := exec.LookPath(c.Path)
	if err != nil {
		return r, err
	}
	text, err := c.Render(params...)
	if err != nil {
		return r, err
	}
	r.Cmd = path
	if len(text) > 0 {
		r.Cmd += " " + text
	}

	errOut, errIn, err := os.Pipe()
	if err != nil {
		return r, err
	}
	defer errOut.Close()

	outOut, outIn, err := os.Pipe()
	if err != nil {
		return r, err
	}
	defer outOut.Close()

	attr := &os.ProcAttr{}
	attr.Files = []*os.File{nil, outIn, errIn}
	if len(c.Dir) > 0 {
		attr.Dir = c.Dir
	}
	if len(c.User) > 0 {
		if os.Getuid() != 0 {
			return r, ErrMustBeRoot
		}
		u, err := user.Lookup(c.User)
		if err != nil {
			return r, err
		}
		uid, err := strconv.ParseUint(u.Uid, 0, 32)
		if err != nil {
			return r, err
		}
		creds := &syscall.Credential{Uid: uint32(uid)}
		attr.Sys = &syscall.SysProcAttr{Credential: creds}
	}
	if len(text) > 0 {
		text = path + " " + text
	} else {
		text = path
	}
	p, err := os.StartProcess(path, strings.Fields(text), attr)
	if err != nil {
		return r, err
	}
	r.SID = nextID()
	r.Started = time.Now()
	s, err := p.Wait()
	r.Finished = time.Now()
	outIn.Close()
	errIn.Close()
	stdout, err := ioutil.ReadAll(outOut)
	if err != nil {
		return r, err
	}
	stderr, err := ioutil.ReadAll(errOut)
	if err != nil {
		return r, err
	}
	r.Stdout = string(stdout)
	r.Stderr = string(stderr)
	r.PID = s.Pid()
	e := s.Sys().(syscall.WaitStatus)
	r.RC = e.ExitStatus()
	r.UserTime = s.UserTime()

	return r, err
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
