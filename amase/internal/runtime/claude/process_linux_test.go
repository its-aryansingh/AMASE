package claude

import (
	"syscall"
	"testing"
)

func TestPdeathsigKillsChildOnLinux(t *testing.T) {
	if got := sysProcAttr().Pdeathsig; got != syscall.SIGKILL {
		t.Fatalf("Pdeathsig = %v, want SIGKILL", got)
	}
}
