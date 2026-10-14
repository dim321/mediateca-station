package leases

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActiveMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.leases")
	_, err := (File{Path: path, Now: fixedNow}).Active()
	var ce *CodeError
	if !errors.As(err, &ce) || ce.Code != CodeMissing {
		t.Fatalf("%v", err)
	}
	if err.Error() != CodeMissing || stringsContainsPath(err.Error(), path) {
		t.Fatalf("error leaked path: %q", err.Error())
	}
}

func TestActiveUnreadableDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := (File{Path: dir, Now: fixedNow}).Active()
	var ce *CodeError
	if !errors.As(err, &ce) || ce.Code != CodeUnreadable {
		t.Fatalf("%v", err)
	}
	if stringsContainsPath(err.Error(), dir) {
		t.Fatalf("error leaked path: %q", err.Error())
	}
}

func TestActivePermissionViaReader(t *testing.T) {
	path := "/var/lib/misc/dnsmasq.leases"
	_, err := (File{
		Path: path,
		Now:  fixedNow,
		Read: func(string) ([]byte, error) {
			return nil, os.ErrPermission
		},
	}).Active()
	var ce *CodeError
	if !errors.As(err, &ce) || ce.Code != CodeUnreadable {
		t.Fatalf("%v", err)
	}
}

func TestActiveUsesInjectedReaderNotRealLeases(t *testing.T) {
	var got string
	pairs, err := (File{
		Path: DefaultPath,
		Now:  fixedNow,
		Read: func(path string) ([]byte, error) {
			got = path
			return []byte("0 aa:bb:cc:dd:ee:ff 10.0.0.2 host *\n"), nil
		},
	}).Active()
	if err != nil || got != DefaultPath || len(pairs) != 1 || pairs[0].IP != "10.0.0.2" {
		t.Fatalf("path=%s err=%v pairs=%+v", got, err, pairs)
	}
}

func stringsContainsPath(msg, path string) bool {
	return strings.Contains(msg, path)
}
