package leases

import (
	"errors"
	"os"
	"time"
)

const DefaultPath = "/var/lib/misc/dnsmasq.leases"

type File struct {
	Path string
	Read func(string) ([]byte, error)
	Now  func() time.Time
}

func (f File) Active() ([]Pair, error) {
	path := f.Path
	if path == "" {
		path = DefaultPath
	}
	read := f.Read
	if read == nil {
		read = os.ReadFile
	}
	now := f.Now
	if now == nil {
		now = time.Now
	}
	data, err := read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &CodeError{Code: CodeMissing}
		}
		return nil, &CodeError{Code: CodeUnreadable}
	}
	return Parse(data, now())
}
