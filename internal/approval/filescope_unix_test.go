//go:build !windows

// The 0600 mode this asserts is a Unix concept. On Windows os.Chmod only
// toggles the read-only attribute — file access there is governed by ACLs
// inherited from the containing directory, not by mode bits — so the
// assertion is meaningless (and fails, reporting 0666) on that platform.
// See FileScopeStore's doc comment for what protects the file on Windows.

package approval

import (
	"os"
	"testing"
)

// The file records what the operator authorised; another local user being
// able to append to it is a privilege-escalation path.
func TestFileScopeStore_FileIsNotGroupOrWorldAccessible(t *testing.T) {
	path := storePath(t)
	s, _ := OpenFileScopeStore(path, "/work/proj")
	if err := s.Grant(execPrefixGrant("go test")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("permissions file mode is %04o; want no group/other access", perm)
	}
}
