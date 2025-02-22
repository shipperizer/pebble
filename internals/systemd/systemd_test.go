// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (c) 2014-2015 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package systemd_test

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	. "gopkg.in/check.v1"

	"github.com/canonical/pebble/internals/osutil"
	"github.com/canonical/pebble/internals/osutil/squashfs"
	"github.com/canonical/pebble/internals/testutil"

	"github.com/canonical/pebble/internals/systemd"
)

type testreporter struct {
	msgs []string
}

func (tr *testreporter) Notify(msg string) {
	tr.msgs = append(tr.msgs, msg)
}

// Hook up check.v1 into the "go test" runner
func Test(t *testing.T) { TestingT(t) }

// systemd's testsuite
type SystemdTestSuite struct {
	rootDir string

	i      int
	argses [][]string
	errors []error
	outs   [][]byte

	j        int
	jns      []string
	jsvcs    [][]string
	jouts    [][]byte
	jerrs    []error
	jfollows []bool

	rep *testreporter

	restoreServicesDir func()
	restoreSystemctl   func()
	restoreJournalctl  func()
}

var _ = Suite(&SystemdTestSuite{})

func (s *SystemdTestSuite) SetUpTest(c *C) {
	s.rootDir = c.MkDir()
	s.restoreServicesDir = systemd.FakeServicesDir(filepath.Join(s.rootDir, systemd.ServicesDir))
	err := os.MkdirAll(systemd.ServicesDir, 0755)
	c.Assert(err, IsNil)

	// force UTC timezone, for reproducible timestamps
	os.Setenv("TZ", "")

	s.restoreSystemctl = systemd.FakeSystemctl(s.myRun)
	s.i = 0
	s.argses = nil
	s.errors = nil
	s.outs = nil

	s.restoreJournalctl = systemd.FakeJournalctl(s.myJctl)
	s.j = 0
	s.jns = nil
	s.jsvcs = nil
	s.jouts = nil
	s.jerrs = nil
	s.jfollows = nil

	s.rep = new(testreporter)
}

func (s *SystemdTestSuite) TearDownTest(c *C) {
	s.restoreServicesDir()
	s.restoreSystemctl()
	s.restoreJournalctl()
}

func (s *SystemdTestSuite) myRun(args ...string) (out []byte, err error) {
	s.argses = append(s.argses, args)
	if s.i < len(s.outs) {
		out = s.outs[s.i]
	}
	if s.i < len(s.errors) {
		err = s.errors[s.i]
	}
	s.i++
	return out, err
}

func (s *SystemdTestSuite) myJctl(svcs []string, n int, follow bool) (io.ReadCloser, error) {
	var err error
	var out []byte

	s.jns = append(s.jns, strconv.Itoa(n))
	s.jsvcs = append(s.jsvcs, svcs)
	s.jfollows = append(s.jfollows, follow)

	if s.j < len(s.jouts) {
		out = s.jouts[s.j]
	}
	if s.j < len(s.jerrs) {
		err = s.jerrs[s.j]
	}
	s.j++

	if out == nil {
		return nil, err
	}

	return ioutil.NopCloser(bytes.NewReader(out)), err
}

func (s *SystemdTestSuite) TestDaemonReload(c *C) {
	err := systemd.New("", systemd.SystemMode, s.rep).DaemonReload()
	c.Assert(err, IsNil)
	c.Assert(s.argses, DeepEquals, [][]string{{"daemon-reload"}})
}

func (s *SystemdTestSuite) TestStart(c *C) {
	err := systemd.New("", systemd.SystemMode, s.rep).Start("foo")
	c.Assert(err, IsNil)
	c.Check(s.argses, DeepEquals, [][]string{{"start", "foo"}})
}

func (s *SystemdTestSuite) TestStartMany(c *C) {
	err := systemd.New("", systemd.SystemMode, s.rep).Start("foo", "bar", "baz")
	c.Assert(err, IsNil)
	c.Check(s.argses, DeepEquals, [][]string{{"start", "foo", "bar", "baz"}})
}

func (s *SystemdTestSuite) TestStop(c *C) {
	restore := systemd.FakeStopDelays(time.Millisecond, 25*time.Second)
	defer restore()
	s.outs = [][]byte{
		nil, // for the "stop" itself
		[]byte("ActiveState=whatever\n"),
		[]byte("ActiveState=active\n"),
		[]byte("ActiveState=inactive\n"),
	}
	s.errors = []error{nil, nil, nil, nil, &systemd.Timeout{}}
	err := systemd.New("", systemd.SystemMode, s.rep).Stop("foo", 1*time.Second)
	c.Assert(err, IsNil)
	c.Assert(s.argses, HasLen, 4)
	c.Check(s.argses[0], DeepEquals, []string{"stop", "foo"})
	c.Check(s.argses[1], DeepEquals, []string{"show", "--property=ActiveState", "foo"})
	c.Check(s.argses[1], DeepEquals, s.argses[2])
	c.Check(s.argses[1], DeepEquals, s.argses[3])
}

func (s *SystemdTestSuite) TestStatus(c *C) {
	s.outs = [][]byte{
		[]byte(`
Type=simple
Id=foo.service
ActiveState=active
UnitFileState=enabled

Type=simple
Id=bar.service
ActiveState=reloading
UnitFileState=static

Type=potato
Id=baz.service
ActiveState=inactive
UnitFileState=disabled
`[1:]),
		[]byte(`
Id=some.timer
ActiveState=active
UnitFileState=enabled

Id=other.socket
ActiveState=active
UnitFileState=disabled
`[1:]),
	}
	s.errors = []error{nil}
	out, err := systemd.New("", systemd.SystemMode, s.rep).Status("foo.service", "bar.service", "baz.service", "some.timer", "other.socket")
	c.Assert(err, IsNil)
	c.Check(out, DeepEquals, []*systemd.UnitStatus{
		{
			Daemon:   "simple",
			UnitName: "foo.service",
			Active:   true,
			Enabled:  true,
		}, {
			Daemon:   "simple",
			UnitName: "bar.service",
			Active:   true,
			Enabled:  true,
		}, {
			Daemon:   "potato",
			UnitName: "baz.service",
			Active:   false,
			Enabled:  false,
		}, {
			UnitName: "some.timer",
			Active:   true,
			Enabled:  true,
		}, {
			UnitName: "other.socket",
			Active:   true,
			Enabled:  false,
		},
	})
	c.Check(s.rep.msgs, IsNil)
	c.Assert(s.argses, DeepEquals, [][]string{
		{"show", "--property=Id,ActiveState,UnitFileState,Type", "foo.service", "bar.service", "baz.service"},
		{"show", "--property=Id,ActiveState,UnitFileState", "some.timer", "other.socket"},
	})
}

func (s *SystemdTestSuite) TestStatusBadNumberOfValues(c *C) {
	s.outs = [][]byte{
		[]byte(`
Type=simple
Id=foo.service
ActiveState=active
UnitFileState=enabled

Type=simple
Id=foo.service
ActiveState=active
UnitFileState=enabled
`[1:]),
	}
	s.errors = []error{nil}
	out, err := systemd.New("", systemd.SystemMode, s.rep).Status("foo.service")
	c.Check(err, ErrorMatches, "cannot get unit status: expected 1 results, got 2")
	c.Check(out, IsNil)
	c.Check(s.rep.msgs, IsNil)
}

func (s *SystemdTestSuite) TestStatusBadLine(c *C) {
	s.outs = [][]byte{
		[]byte(`
Type=simple
Id=foo.service
ActiveState=active
UnitFileState=enabled
Potatoes
`[1:]),
	}
	s.errors = []error{nil}
	out, err := systemd.New("", systemd.SystemMode, s.rep).Status("foo.service")
	c.Assert(err, ErrorMatches, `.* bad line "Potatoes" .*`)
	c.Check(out, IsNil)
}

func (s *SystemdTestSuite) TestStatusBadId(c *C) {
	s.outs = [][]byte{
		[]byte(`
Type=simple
Id=bar.service
ActiveState=active
UnitFileState=enabled
`[1:]),
	}
	s.errors = []error{nil}
	out, err := systemd.New("", systemd.SystemMode, s.rep).Status("foo.service")
	c.Assert(err, ErrorMatches, `.* queried status of "foo.service" but got status of "bar.service"`)
	c.Check(out, IsNil)
}

func (s *SystemdTestSuite) TestStatusBadField(c *C) {
	s.outs = [][]byte{
		[]byte(`
Type=simple
Id=foo.service
ActiveState=active
UnitFileState=enabled
Potatoes=false
`[1:]),
	}
	s.errors = []error{nil}
	out, err := systemd.New("", systemd.SystemMode, s.rep).Status("foo.service")
	c.Assert(err, ErrorMatches, `.* unexpected field "Potatoes" .*`)
	c.Check(out, IsNil)
}

func (s *SystemdTestSuite) TestStatusMissingRequiredFieldService(c *C) {
	s.outs = [][]byte{
		[]byte(`
Id=foo.service
ActiveState=active
`[1:]),
	}
	s.errors = []error{nil}
	out, err := systemd.New("", systemd.SystemMode, s.rep).Status("foo.service")
	c.Assert(err, ErrorMatches, `.* missing UnitFileState, Type .*`)
	c.Check(out, IsNil)
}

func (s *SystemdTestSuite) TestStatusMissingRequiredFieldTimer(c *C) {
	s.outs = [][]byte{
		[]byte(`
Id=foo.timer
ActiveState=active
`[1:]),
	}
	s.errors = []error{nil}
	out, err := systemd.New("", systemd.SystemMode, s.rep).Status("foo.timer")
	c.Assert(err, ErrorMatches, `.* missing UnitFileState .*`)
	c.Check(out, IsNil)
}

func (s *SystemdTestSuite) TestStatusDupeField(c *C) {
	s.outs = [][]byte{
		[]byte(`
Type=simple
Id=foo.service
ActiveState=active
ActiveState=active
UnitFileState=enabled
`[1:]),
	}
	s.errors = []error{nil}
	out, err := systemd.New("", systemd.SystemMode, s.rep).Status("foo.service")
	c.Assert(err, ErrorMatches, `.* duplicate field "ActiveState" .*`)
	c.Check(out, IsNil)
}

func (s *SystemdTestSuite) TestStatusEmptyField(c *C) {
	s.outs = [][]byte{
		[]byte(`
Type=simple
Id=
ActiveState=active
UnitFileState=enabled
`[1:]),
	}
	s.errors = []error{nil}
	out, err := systemd.New("", systemd.SystemMode, s.rep).Status("foo.service")
	c.Assert(err, ErrorMatches, `.* empty field "Id" .*`)
	c.Check(out, IsNil)
}

func (s *SystemdTestSuite) TestStopTimeout(c *C) {
	restore := systemd.FakeStopDelays(time.Millisecond, 25*time.Second)
	defer restore()
	err := systemd.New("", systemd.SystemMode, s.rep).Stop("foo", 10*time.Millisecond)
	c.Assert(err, FitsTypeOf, &systemd.Timeout{})
	c.Assert(len(s.rep.msgs) > 0, Equals, true)
	c.Check(s.rep.msgs[0], Equals, "Waiting for foo to stop.")
}

func (s *SystemdTestSuite) TestDisable(c *C) {
	err := systemd.New("xyzzy", systemd.SystemMode, s.rep).Disable("foo")
	c.Assert(err, IsNil)
	c.Check(s.argses, DeepEquals, [][]string{{"--root", "xyzzy", "disable", "foo"}})
}

func (s *SystemdTestSuite) TestAvailable(c *C) {
	err := systemd.Available()
	c.Assert(err, IsNil)
	c.Check(s.argses, DeepEquals, [][]string{{"--version"}})
}

func (s *SystemdTestSuite) TestEnable(c *C) {
	err := systemd.New("xyzzy", systemd.SystemMode, s.rep).Enable("foo")
	c.Assert(err, IsNil)
	c.Check(s.argses, DeepEquals, [][]string{{"--root", "xyzzy", "enable", "foo"}})
}

func (s *SystemdTestSuite) TestMask(c *C) {
	err := systemd.New("xyzzy", systemd.SystemMode, s.rep).Mask("foo")
	c.Assert(err, IsNil)
	c.Check(s.argses, DeepEquals, [][]string{{"--root", "xyzzy", "mask", "foo"}})
}

func (s *SystemdTestSuite) TestUnmask(c *C) {
	err := systemd.New("xyzzy", systemd.SystemMode, s.rep).Unmask("foo")
	c.Assert(err, IsNil)
	c.Check(s.argses, DeepEquals, [][]string{{"--root", "xyzzy", "unmask", "foo"}})
}

func (s *SystemdTestSuite) TestRestart(c *C) {
	restore := systemd.FakeStopDelays(time.Millisecond, 25*time.Second)
	defer restore()
	s.outs = [][]byte{
		nil, // for the "stop" itself
		[]byte("ActiveState=inactive\n"),
		nil, // for the "start"
	}
	s.errors = []error{nil, nil, nil, nil, &systemd.Timeout{}}
	err := systemd.New("", systemd.SystemMode, s.rep).Restart("foo", 100*time.Millisecond)
	c.Assert(err, IsNil)
	c.Check(s.argses, HasLen, 3)
	c.Check(s.argses[0], DeepEquals, []string{"stop", "foo"})
	c.Check(s.argses[1], DeepEquals, []string{"show", "--property=ActiveState", "foo"})
	c.Check(s.argses[2], DeepEquals, []string{"start", "foo"})
}

func (s *SystemdTestSuite) TestKill(c *C) {
	c.Assert(systemd.New("", systemd.SystemMode, s.rep).Kill("foo", "HUP", ""), IsNil)
	c.Check(s.argses, DeepEquals, [][]string{{"kill", "foo", "-s", "HUP", "--kill-who=all"}})
}

func (s *SystemdTestSuite) TestIsTimeout(c *C) {
	c.Check(systemd.IsTimeout(os.ErrInvalid), Equals, false)
	c.Check(systemd.IsTimeout(&systemd.Timeout{}), Equals, true)
}

func (s *SystemdTestSuite) TestLogErrJctl(c *C) {
	s.jerrs = []error{&systemd.Timeout{}}

	reader, err := systemd.New("", systemd.SystemMode, s.rep).LogReader([]string{"foo"}, 24, false)
	c.Check(err, NotNil)
	c.Check(reader, IsNil)
	c.Check(s.jns, DeepEquals, []string{"24"})
	c.Check(s.jsvcs, DeepEquals, [][]string{{"foo"}})
	c.Check(s.jfollows, DeepEquals, []bool{false})
	c.Check(s.j, Equals, 1)
}

func (s *SystemdTestSuite) TestLogs(c *C) {
	expected := `{"a": 1}
{"a": 2}
`
	s.jouts = [][]byte{[]byte(expected)}

	reader, err := systemd.New("", systemd.SystemMode, s.rep).LogReader([]string{"foo"}, 24, false)
	c.Check(err, IsNil)
	logs, err := ioutil.ReadAll(reader)
	c.Assert(err, IsNil)
	c.Check(string(logs), Equals, expected)
	c.Check(s.jns, DeepEquals, []string{"24"})
	c.Check(s.jsvcs, DeepEquals, [][]string{{"foo"}})
	c.Check(s.jfollows, DeepEquals, []bool{false})
	c.Check(s.j, Equals, 1)
}

func (s *SystemdTestSuite) TestLogPID(c *C) {
	c.Check(systemd.Log{}.PID(), Equals, "-")
	c.Check(systemd.Log{"_PID": "99"}.PID(), Equals, "99")
	c.Check(systemd.Log{"SYSLOG_PID": "99"}.PID(), Equals, "99")
	// things starting with underscore are "trusted", so we trust
	// them more than the user-settable ones:
	c.Check(systemd.Log{"_PID": "42", "SYSLOG_PID": "99"}.PID(), Equals, "42")
}

func (s *SystemdTestSuite) TestTime(c *C) {
	t, err := systemd.Log{}.Time()
	c.Check(t.IsZero(), Equals, true)
	c.Check(err, ErrorMatches, "no timestamp")

	t, err = systemd.Log{"__REALTIME_TIMESTAMP": "what"}.Time()
	c.Check(t.IsZero(), Equals, true)
	c.Check(err, ErrorMatches, `timestamp not a decimal number: "what"`)

	t, err = systemd.Log{"__REALTIME_TIMESTAMP": "0"}.Time()
	c.Check(err, IsNil)
	c.Check(t.String(), Equals, "1970-01-01 00:00:00 +0000 UTC")

	t, err = systemd.Log{"__REALTIME_TIMESTAMP": "42"}.Time()
	c.Check(err, IsNil)
	c.Check(t.String(), Equals, "1970-01-01 00:00:00.000042 +0000 UTC")
}

func (s *SystemdTestSuite) TestMountUnitPath(c *C) {
	c.Assert(systemd.MountUnitPath("/apps/hello/1.1"), Equals, filepath.Join(systemd.ServicesDir, "apps-hello-1.1.mount"))
}

func makeFakeFile(c *C, path string) {
	err := os.MkdirAll(filepath.Dir(path), 0755)
	c.Assert(err, IsNil)
	err = ioutil.WriteFile(path, nil, 0644)
	c.Assert(err, IsNil)
}

func (s *SystemdTestSuite) TestAddMountUnit(c *C) {
	restore := squashfs.FakeUseFuse(false)
	defer restore()

	fakeSnapPath := filepath.Join(c.MkDir(), "/var/lib/snappy/snaps/foo_1.0.snap")
	makeFakeFile(c, fakeSnapPath)

	mountUnitName, err := systemd.New(s.rootDir, systemd.SystemMode, nil).AddMountUnitFile("foo", "42", fakeSnapPath, "/snap/snapname/123", "squashfs")
	c.Assert(err, IsNil)
	defer os.Remove(mountUnitName)

	c.Assert(filepath.Join(systemd.ServicesDir, mountUnitName), testutil.FileEquals, fmt.Sprintf(`
[Unit]
Description=Mount unit for foo, revision 42
Before=snapd.service

[Mount]
What=%s
Where=/snap/snapname/123
Type=squashfs
Options=nodev,ro,x-gdu.hide

[Install]
WantedBy=multi-user.target
`[1:], fakeSnapPath))

	c.Assert(s.argses, DeepEquals, [][]string{
		{"daemon-reload"},
		{"--root", s.rootDir, "enable", "snap-snapname-123.mount"},
		{"start", "snap-snapname-123.mount"},
	})
}

func (s *SystemdTestSuite) TestAddMountUnitForDirs(c *C) {
	restore := squashfs.FakeUseFuse(false)
	defer restore()

	// a directory instead of a file produces a different output
	snapDir := c.MkDir()
	mountUnitName, err := systemd.New("", systemd.SystemMode, nil).AddMountUnitFile("foodir", "x1", snapDir, "/snap/snapname/x1", "squashfs")
	c.Assert(err, IsNil)
	defer os.Remove(mountUnitName)

	c.Assert(filepath.Join(systemd.ServicesDir, mountUnitName), testutil.FileEquals, fmt.Sprintf(`
[Unit]
Description=Mount unit for foodir, revision x1
Before=snapd.service

[Mount]
What=%s
Where=/snap/snapname/x1
Type=none
Options=nodev,ro,x-gdu.hide,bind

[Install]
WantedBy=multi-user.target
`[1:], snapDir))

	c.Assert(s.argses, DeepEquals, [][]string{
		{"daemon-reload"},
		{"--root", "", "enable", "snap-snapname-x1.mount"},
		{"start", "snap-snapname-x1.mount"},
	})
}

func (s *SystemdTestSuite) TestFuseInContainer(c *C) {
	if !osutil.CanStat("/dev/fuse") {
		c.Skip("No /dev/fuse on the system")
	}

	systemdCmd := testutil.FakeCommand(c, "systemd-detect-virt", `
echo lxc
exit 0
	`, false)
	defer systemdCmd.Restore()

	fuseCmd := testutil.FakeCommand(c, "squashfuse", `
exit 0
	`, false)
	defer fuseCmd.Restore()

	fakeSnapPath := filepath.Join(c.MkDir(), "/var/lib/snappy/snaps/foo_1.0.snap")
	err := os.MkdirAll(filepath.Dir(fakeSnapPath), 0755)
	c.Assert(err, IsNil)
	err = ioutil.WriteFile(fakeSnapPath, nil, 0644)
	c.Assert(err, IsNil)

	mountUnitName, err := systemd.New("", systemd.SystemMode, nil).AddMountUnitFile("foo", "x1", fakeSnapPath, "/snap/snapname/123", "squashfs")
	c.Assert(err, IsNil)
	defer os.Remove(mountUnitName)

	c.Check(filepath.Join(systemd.ServicesDir, mountUnitName), testutil.FileEquals, fmt.Sprintf(`
[Unit]
Description=Mount unit for foo, revision x1
Before=snapd.service

[Mount]
What=%s
Where=/snap/snapname/123
Type=fuse.squashfuse
Options=nodev,ro,x-gdu.hide,allow_other

[Install]
WantedBy=multi-user.target
`[1:], fakeSnapPath))
}

func (s *SystemdTestSuite) TestFuseOutsideContainer(c *C) {
	systemdCmd := testutil.FakeCommand(c, "systemd-detect-virt", `
echo none
exit 0
	`, false)
	defer systemdCmd.Restore()

	fuseCmd := testutil.FakeCommand(c, "squashfuse", `
exit 0
	`, false)
	defer fuseCmd.Restore()

	fakeSnapPath := filepath.Join(c.MkDir(), "/var/lib/snappy/snaps/foo_1.0.snap")
	err := os.MkdirAll(filepath.Dir(fakeSnapPath), 0755)
	c.Assert(err, IsNil)
	err = ioutil.WriteFile(fakeSnapPath, nil, 0644)
	c.Assert(err, IsNil)

	mountUnitName, err := systemd.New("", systemd.SystemMode, nil).AddMountUnitFile("foo", "x1", fakeSnapPath, "/snap/snapname/123", "squashfs")
	c.Assert(err, IsNil)
	defer os.Remove(mountUnitName)

	c.Assert(filepath.Join(systemd.ServicesDir, mountUnitName), testutil.FileEquals, fmt.Sprintf(`
[Unit]
Description=Mount unit for foo, revision x1
Before=snapd.service

[Mount]
What=%s
Where=/snap/snapname/123
Type=squashfs
Options=nodev,ro,x-gdu.hide

[Install]
WantedBy=multi-user.target
`[1:], fakeSnapPath))
}

func (s *SystemdTestSuite) TestJctl(c *C) {
	var args []string
	var err error
	systemd.FakeOsutilStreamCommand(func(name string, myargs ...string) (io.ReadCloser, error) {
		c.Check(cap(myargs) <= len(myargs)+2, Equals, true, Commentf("cap:%d, len:%d", cap(myargs), len(myargs)))
		args = myargs
		return nil, nil
	})

	_, err = systemd.Jctl([]string{"foo", "bar"}, 10, false)
	c.Assert(err, IsNil)
	c.Check(args, DeepEquals, []string{"-o", "json", "--no-pager", "-n", "10", "-u", "foo", "-u", "bar"})
	_, err = systemd.Jctl([]string{"foo", "bar", "baz"}, 99, true)
	c.Assert(err, IsNil)
	c.Check(args, DeepEquals, []string{"-o", "json", "--no-pager", "-n", "99", "-f", "-u", "foo", "-u", "bar", "-u", "baz"})
	_, err = systemd.Jctl([]string{"foo", "bar"}, -1, false)
	c.Assert(err, IsNil)
	c.Check(args, DeepEquals, []string{"-o", "json", "--no-pager", "--no-tail", "-u", "foo", "-u", "bar"})
}

func (s *SystemdTestSuite) TestIsActiveIsInactive(c *C) {
	sysErr := &systemd.Error{}
	sysErr.SetExitCode(1)
	sysErr.SetMsg([]byte("inactive\n"))
	s.errors = []error{sysErr}

	active, err := systemd.New("xyzzy", systemd.SystemMode, s.rep).IsActive("foo")
	c.Assert(active, Equals, false)
	c.Assert(err, IsNil)
	c.Check(s.argses, DeepEquals, [][]string{{"--root", "xyzzy", "is-active", "foo"}})
}

func (s *SystemdTestSuite) TestIsActiveIsActive(c *C) {
	s.errors = []error{nil}

	active, err := systemd.New("xyzzy", systemd.SystemMode, s.rep).IsActive("foo")
	c.Assert(active, Equals, true)
	c.Assert(err, IsNil)
	c.Check(s.argses, DeepEquals, [][]string{{"--root", "xyzzy", "is-active", "foo"}})
}

func (s *SystemdTestSuite) TestIsActiveErr(c *C) {
	sysErr := &systemd.Error{}
	sysErr.SetExitCode(1)
	sysErr.SetMsg([]byte("random-failure\n"))
	s.errors = []error{sysErr}

	active, err := systemd.New("xyzzy", systemd.SystemMode, s.rep).IsActive("foo")
	c.Assert(active, Equals, false)
	c.Assert(err, ErrorMatches, ".* failed with exit status 1: random-failure\n")
}

func makeFakeMountUnit(c *C, mountDir string) string {
	mountUnit := systemd.MountUnitPath(mountDir)
	err := ioutil.WriteFile(mountUnit, nil, 0644)
	c.Assert(err, IsNil)
	return mountUnit
}

// FIXME: also test for the "IsMounted" case
func (s *SystemdTestSuite) TestRemoveMountUnit(c *C) {
	mountDir := s.rootDir + "/snap/foo/42"
	mountUnit := makeFakeMountUnit(c, "/snap/foo/42")
	err := systemd.New(s.rootDir, systemd.SystemMode, nil).RemoveMountUnitFile(mountDir)
	c.Assert(err, IsNil)

	// the file is gone
	c.Check(osutil.CanStat(mountUnit), Equals, false)
	// and the unit is disabled and the daemon reloaded
	c.Check(s.argses, DeepEquals, [][]string{
		{"--root", s.rootDir, "disable", "snap-foo-42.mount"},
		{"daemon-reload"},
	})
}

func (s *SystemdTestSuite) TestDaemonReloadMutex(c *C) {
	sysd := systemd.New(s.rootDir, systemd.SystemMode, nil)

	fakeSnapPath := filepath.Join(c.MkDir(), "/var/lib/snappy/snaps/foo_1.0.snap")
	makeFakeFile(c, fakeSnapPath)

	// create a go-routine that will try to daemon-reload like crazy
	stopCh := make(chan bool, 1)
	stoppedCh := make(chan bool, 1)
	go func() {
		for {
			sysd.DaemonReload()
			select {
			case <-stopCh:
				close(stoppedCh)
				return
			default:
				//pass
			}
		}
	}()

	// And now add a mount unit file while the go-routine tries to
	// daemon-reload. This will be serialized, if not this would
	// panic because systemd.daemonReloadNoLock ensures the lock is
	// taken when this happens.
	_, err := sysd.AddMountUnitFile("foo", "42", fakeSnapPath, "/snap/foo/42", "squashfs")
	c.Assert(err, IsNil)
	close(stopCh)
	<-stoppedCh
}

func (s *SystemdTestSuite) TestUserMode(c *C) {
	sysd := systemd.New(s.rootDir, systemd.UserMode, nil)

	c.Assert(sysd.Enable("foo"), IsNil)
	c.Check(s.argses[0], DeepEquals, []string{"--user", "--root", s.rootDir, "enable", "foo"})
	c.Assert(sysd.Start("foo"), IsNil)
	c.Check(s.argses[1], DeepEquals, []string{"--user", "start", "foo"})
}

func (s *SystemdTestSuite) TestGlobalUserMode(c *C) {
	sysd := systemd.New(s.rootDir, systemd.GlobalUserMode, nil)

	c.Assert(sysd.Enable("foo"), IsNil)
	c.Check(s.argses[0], DeepEquals, []string{"--user", "--global", "--root", s.rootDir, "enable", "foo"})
	c.Assert(sysd.Disable("foo"), IsNil)
	c.Check(s.argses[1], DeepEquals, []string{"--user", "--global", "--root", s.rootDir, "disable", "foo"})
	c.Assert(sysd.Mask("foo"), IsNil)
	c.Check(s.argses[2], DeepEquals, []string{"--user", "--global", "--root", s.rootDir, "mask", "foo"})
	c.Assert(sysd.Unmask("foo"), IsNil)
	c.Check(s.argses[3], DeepEquals, []string{"--user", "--global", "--root", s.rootDir, "unmask", "foo"})

	// Commands that don't make sense for GlobalUserMode panic
	c.Check(sysd.DaemonReload, Panics, "cannot call daemon-reload with GlobalUserMode")
	c.Check(func() { sysd.Start("foo") }, Panics, "cannot call start with GlobalUserMode")
	c.Check(func() { sysd.StartNoBlock("foo") }, Panics, "cannot call start with GlobalUserMode")
	c.Check(func() { sysd.Stop("foo", 0) }, Panics, "cannot call stop with GlobalUserMode")
	c.Check(func() { sysd.Restart("foo", 0) }, Panics, "cannot call restart with GlobalUserMode")
	c.Check(func() { sysd.Kill("foo", "HUP", "") }, Panics, "cannot call kill with GlobalUserMode")
	c.Check(func() { sysd.Status("foo") }, Panics, "cannot call status with GlobalUserMode")
	c.Check(func() { sysd.IsEnabled("foo") }, Panics, "cannot call is-enabled with GlobalUserMode")
	c.Check(func() { sysd.IsActive("foo") }, Panics, "cannot call is-active with GlobalUserMode")
}
