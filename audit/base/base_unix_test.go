package base

import (
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var (
	darwinKernelRE = regexp.MustCompile(`(\d+\.\d+\.\d+)`)
)

func TestKernelVer(t *testing.T) {
	b, err := exec.Command("uname", "-r").Output()
	if err != nil {
		t.Fatal(err)
	}

	var (
		want string
		got  string
	)

	switch runtime.GOOS {
	case "linux":
		// Linux has so many variations we simply just tests that this runs.
		want = strings.TrimSpace(string(b))
		got, err = kernelVer()
		if err != nil {
			t.Fatal(err)
		}
		return
	case "darwin":
		want = strings.TrimSpace(string(b))
		if !darwinKernelRE.MatchString(want) {
			t.Fatalf("TestKernelVer(): darwin kernel version from command line failed to match an expected kernel version, got: %q", want)
		}
		got, err = kernelVer()
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Skipf("TestKernelVer() is not supported on %s", runtime.GOOS)
	}
	if got != want {
		t.Fatalf("TestKernelVer() = got %q; want %q", got, want)
	}
}
