package httpmedia

import (
	"net/http"
	"os"
	"strconv"
	"strings"
)

type Opener func(id int) (*os.File, string, error)

func Handler(secret string, open Opener) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		id, ok := parse(r.URL.Path, secret)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f, mime, err := open(id)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", mime)
		http.ServeContent(w, r, "media", info.ModTime(), f)
	})
}

func parse(path, secret string) (int, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[1] != "m" || parts[2] != secret {
		return 0, false
	}
	id, err := strconv.Atoi(parts[3])
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
