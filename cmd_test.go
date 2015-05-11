package cmdr

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"testing"
)

type Brief struct {
	Path, Params string
}

type Permutation struct {
	Cmd    int64
	Params *[]Param
	RC     int
	Error  error
}

var briefs = map[int64]Brief{
	123: {"/bin/ls", "-l"},
	124: {"/bin/ls", "-l {{WHAT}}"},
	125: {"/bin/ls", "-l {{WHAT}} [{{EVER}}]"},
	126: {"echo", "{{WHAT}}"},
	127: {"/bin/ls", "-l cmd.go"},
	128: {"./failure", ""},
	129: {"sleep", "2"},
	130: {"whoami", ""},
	131: {"false", ""},
	132: {"/does/not/exist", ""},
	133: {"pwd", ""},
}

var perms = []Permutation{
	{132, nil, 0, ErrNoSuchFile},
	{123, nil, 0, nil},
	{124, &[]Param{{"WHAT", "*.blah"}}, 0, ErrNoSuchFile},
	{124, &[]Param{{"WHAT", "*.go"}}, 0, nil},
	{125, &[]Param{{"WHAT", "-r"}, {"EVER", "*.go"}}, 0, nil},
	{125, &[]Param{{"EVER", "-r"}}, 0, ErrIncomplete},
	{126, &[]Param{{"WHAT", "$LOGNAME"}}, 0, nil},
	{127, nil, 0, nil},
	{128, nil, 23, nil},
	{129, nil, 0, nil},
	{130, nil, 0, nil},
	{131, nil, 1, nil},
	{133, nil, 0, nil},
}

var cmds = make(map[int64]Command)

const (
	forever = `#!/bin/bash
LOG=${LOG:-flog}

while true
do
    let "CNT++"
    echo $(date) $LOGNAME $CNT >> $LOG
    sleep 1
done
`
	failure = `#!/bin/bash
ERR=${1:-23}
echo >&2 "all I got was a rock"
exit $ERR
`
)

func init() {
	for k, v := range briefs {
		c := Command{Path: v.Path, Params: v.Params}
		if k == 133 {
			c.Dir = "/tmp"
		}
		if k == 130 && os.Getuid() == 0 {
			c.User = "Paul.Stuart"
		}
		cmds[k] = c
	}
	ioutil.WriteFile("failure", []byte(failure), 0755)
	ioutil.WriteFile("forever", []byte(forever), 0755)
}

func getCmd(t *testing.T, id int64) Command {
	c, exists := cmds[id]
	if !exists {
		t.Fatal(c)
	}
	return c
}

func runner(t *testing.T, id int64, rc int, wants error, p ...Param) {
	c := getCmd(t, id)
	r, err := c.Run(p...)
	same := err != nil && wants != nil && strings.HasSuffix(err.Error(), wants.Error())

	switch {
	case wants == nil && err != nil:
		t.Error(err)
	case wants != nil && err == nil:
		t.Error("Should be error")
	case wants != nil && !same:
		t.Errorf("\nWant: %s\nHave: %s\n", wants.Error(), err.Error())
	case wants != nil && same:
		t.Log("Got expected error: " + wants.Error())
	}

	if rc != r.RC {
		t.Error("expected return code:", rc, "got:", r.RC)
	}
	t.Log(r)
}

func TestPermutations(t *testing.T) {
	for _, p := range perms {
		if p.Params == nil {
			runner(t, p.Cmd, p.RC, p.Error)
		} else {
			runner(t, p.Cmd, p.RC, p.Error, *p.Params...)
		}
	}
}

func TestBackground(t *testing.T) {
	cmd := Command{Path: "./forever"}
	pid, err := cmd.Background()
	if err != nil {
		t.Error(err)
	} else {
		// don't let user forget about this!
		fmt.Printf("\nBackground PID: %d\n\n", pid)
	}
}
