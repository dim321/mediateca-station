package httpmedia

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSecretMimeAndRange(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "19"), []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "19.mime"), []byte("video/mp2t"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"
	h := Handler(secret, func(id int) (*os.File, string, error) {
		f, err := os.Open(filepath.Join(dir, "19"))
		if err != nil {
			return nil, "", err
		}
		mime, err := os.ReadFile(filepath.Join(dir, "19.mime"))
		if err != nil {
			f.Close()
			return nil, "", err
		}
		if id != 19 {
			f.Close()
			return nil, "", os.ErrNotExist
		}
		return f, string(mime), nil
	})

	ok := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/m/"+secret+"/19", nil)
	req.Header.Set("Range", "bytes=0-3")
	h.ServeHTTP(ok, req)
	if ok.Code != http.StatusPartialContent || ok.Body.String() != "abcd" {
		t.Fatalf("code %d body %q", ok.Code, ok.Body.String())
	}
	if ok.Header().Get("Content-Type") != "video/mp2t" {
		t.Fatalf("type %q", ok.Header().Get("Content-Type"))
	}

	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/m/wrong/19", nil))
	if missing.Code != http.StatusNotFound || missing.Body.Len() != 0 {
		t.Fatalf("secret code %d body %q", missing.Code, missing.Body.String())
	}

	notFound := httptest.NewRecorder()
	h.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/other", nil))
	if notFound.Code != http.StatusNotFound || notFound.Body.Len() != 0 {
		t.Fatalf("path code %d", notFound.Code)
	}

	badRange := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/m/"+secret+"/19", nil)
	req.Header.Set("Range", "bytes=100-200")
	h.ServeHTTP(badRange, req)
	if badRange.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("range code %d", badRange.Code)
	}
}

func TestMissingFileIs404(t *testing.T) {
	h := Handler("0123456789abcdef0123456789abcdef", func(int) (*os.File, string, error) {
		return nil, "", os.ErrNotExist
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/m/0123456789abcdef0123456789abcdef/19", nil))
	if rec.Code != http.StatusNotFound || rec.Body.Len() != 0 {
		t.Fatalf("code %d body %q", rec.Code, rec.Body.String())
	}
}
