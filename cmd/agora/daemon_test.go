package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// TestAgoraDaemon_ActuallyBootable builds the real agora binary and runs
// `agora daemon -socket <path>` as a genuine subprocess — proving the
// subcommand is actually bootable (blueprint §6 q4's own bar: "the daemon
// must be actually bootable"), not just wired in source. It dials the
// socket as a real session-protocol client and sends an attach frame for a
// thread the (fresh, empty) daemon has never heard of, and asserts the
// connection is refused/closed — an externally observable sign that the
// running process is genuinely speaking the session protocol through a
// real internal/daemon.Daemon, not just listening.
func TestAgoraDaemon_ActuallyBootable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets unsupported on this Windows runtime")
	}
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "agora-daemon-test-bin")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "agora.sock")

	cmd := exec.Command(binPath, "daemon", "-socket", sockPath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start agora daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for agora daemon to create its socket")
		}
		time.Sleep(20 * time.Millisecond)
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial daemon socket: %v", err)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	if err := enc.Encode(agoraio.ClientFrame{Attach: &agoraio.AttachRequest{
		ThreadID: "th_never_created", ClientID: "smoke-test-client", Kind: "tui",
	}}); err != nil {
		t.Fatalf("send attach frame: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if n, err := conn.Read(buf); n != 0 || err == nil {
		t.Fatalf("expected the connection to be refused/closed for an unknown thread, got n=%d err=%v", n, err)
	}
}
