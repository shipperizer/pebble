// Copyright (c) 2014-2020 Canonical Ltd
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License version 3 as
// published by the Free Software Foundation.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package testutil

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/check.v1"

	"github.com/canonical/pebble/internals/reaper"
)

var shellcheckPath string

func init() {
	if p, err := exec.LookPath("shellcheck"); err == nil {
		shellcheckPath = p
	}
}

var (
	shellchecked   = make(map[string]bool, 16)
	shellcheckedMu sync.Mutex
)

func shellcheckSeenAlready(script string) bool {
	shellcheckedMu.Lock()
	defer shellcheckedMu.Unlock()
	if shellchecked[script] {
		return true
	}
	shellchecked[script] = true
	return false
}

func maybeShellcheck(c *check.C, script string, wholeScript io.Reader, withReaper bool) {
	// FakeCommand is used sometimes in SetUptTest, so it adds up
	// even for the empty script, don't recheck the essentially same
	// thing again and again!
	if shellcheckSeenAlready(script) {
		return
	}
	c.Logf("using shellcheck: %q", shellcheckPath)
	if shellcheckPath == "" {
		// no shellcheck, nothing to do
		return
	}
	cmd := exec.Command(shellcheckPath, "-s", "bash", "-")
	cmd.Stdin = wholeScript

	var out []byte
	var err error
	if withReaper {
		out, err = reaper.CommandCombinedOutput(cmd)
	} else {
		out, err = cmd.CombinedOutput()
	}

	c.Check(err, check.IsNil, check.Commentf("shellcheck failed:\n%s", string(out)))
}

// FakeCmd allows faking commands for testing.
type FakeCmd struct {
	binDir  string
	exeFile string
	logFile string
}

// The top of the script generate the output to capture the
// command that was run and the arguments used. To support
// faking commands that need "\n" in their args (like zenity)
// we use the following convention:
// - generate \0 to separate args
// - generate \0\0 to separate commands
var scriptTpl = `#!/bin/bash
printf "%%s" "$(basename "$0")" >> %[1]q
printf '\0' >> %[1]q

for arg in "$@"; do
     printf "%%s" "$arg" >> %[1]q
     printf '\0'  >> %[1]q
done

printf '\0' >> %[1]q
%s
`

// FakeCommand adds a faked command. If the basename argument is a command
// it is added to PATH. If it is an absolute path it is just created there.
// the caller is responsible for the cleanup in this case.
//
// The command logs all invocations to a dedicated log file. If script is
// non-empty then it is used as is and the caller is responsible for how the
// script behaves (exit code and any extra behavior). If script is empty then
// the command exits successfully without any other side-effect.
func FakeCommand(c *check.C, basename, script string, withReaper bool) *FakeCmd {
	var wholeScript bytes.Buffer
	var binDir, exeFile, logFile string
	if filepath.IsAbs(basename) {
		binDir = filepath.Dir(basename)
		exeFile = basename
		logFile = basename + ".log"
	} else {
		binDir = c.MkDir()
		exeFile = path.Join(binDir, basename)
		logFile = path.Join(binDir, basename+".log")
		os.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	}
	fmt.Fprintf(&wholeScript, scriptTpl, logFile, script)
	err := ioutil.WriteFile(exeFile, wholeScript.Bytes(), 0700)
	if err != nil {
		panic(err)
	}

	maybeShellcheck(c, script, &wholeScript, withReaper)

	return &FakeCmd{binDir: binDir, exeFile: exeFile, logFile: logFile}
}

// Also fake this command, using the same bindir and log.
// Useful when you want to check the ordering of things.
func (cmd *FakeCmd) Also(basename, script string) *FakeCmd {
	exeFile := path.Join(cmd.binDir, basename)
	err := ioutil.WriteFile(exeFile, []byte(fmt.Sprintf(scriptTpl, cmd.logFile, script)), 0700)
	if err != nil {
		panic(err)
	}
	return &FakeCmd{binDir: cmd.binDir, exeFile: exeFile, logFile: cmd.logFile}
}

// Restore removes the faked command from PATH
func (cmd *FakeCmd) Restore() {
	entries := strings.Split(os.Getenv("PATH"), ":")
	for i, entry := range entries {
		if entry == cmd.binDir {
			entries = append(entries[:i], entries[i+1:]...)
			break
		}
	}
	os.Setenv("PATH", strings.Join(entries, ":"))
}

// Calls returns a list of calls that were made to the fake command.
// of the form:
//
//	[][]string{
//	    {"cmd", "arg1", "arg2"}, // first invocation of "cmd"
//	    {"cmd", "arg1", "arg2"}, // second invocation of "cmd"
//	}
func (cmd *FakeCmd) Calls() [][]string {
	raw, err := ioutil.ReadFile(cmd.logFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		panic(err)
	}
	logContent := strings.TrimSuffix(string(raw), "\000")

	allCalls := [][]string{}
	calls := strings.Split(logContent, "\000\000")
	for _, call := range calls {
		call = strings.TrimSuffix(call, "\000")
		allCalls = append(allCalls, strings.Split(call, "\000"))
	}
	return allCalls
}

// ForgetCalls purges the list of calls made so far
func (cmd *FakeCmd) ForgetCalls() {
	err := os.Remove(cmd.logFile)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		panic(err)
	}
}

// BinDir returns the location of the directory holding overridden commands.
func (cmd *FakeCmd) BinDir() string {
	return cmd.binDir
}

// Exe return the full path of the fake binary
func (cmd *FakeCmd) Exe() string {
	return filepath.Join(cmd.exeFile)
}
