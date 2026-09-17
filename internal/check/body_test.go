package check

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func gzipped(t *testing.T, parts ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		if _, err := zw.Write(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestHTTPKeywordBody(t *testing.T) {
	const kw = "stichwort-ok"
	fits := strings.Repeat("x", maxBody-len(kw)) + kw
	cut := strings.Repeat("x", maxBody-len(kw)+1) + kw
	zipped := gzipped(t, []byte("<p>"+kw+"</p>"))
	// 64 MiB nullen werden zu wenigen KiB, gelesen wird trotzdem nur 1 MiB entpackt
	bomb := gzipped(t, make([]byte, 64<<20), []byte(kw))

	mux := http.NewServeMux()
	mux.HandleFunc("/chunks", func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		for _, part := range []string{"<p>stich", "wort", "-ok</p>"} {
			w.Write([]byte(part))
			fl.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	})
	mux.HandleFunc("/fits", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(fits)) })
	mux.HandleFunc("/cut", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(cut)) })
	gz := func(body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				w.WriteHeader(http.StatusNotAcceptable)
				return
			}
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(body)
		}
	}
	mux.HandleFunc("/gzip", gz(zipped))
	mux.HandleFunc("/bomb", gz(bomb))
	mux.HandleFunc("/gzip-kaputt", gz(zipped[:len(zipped)/2]))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tests := []struct {
		path string
		ok   bool
		err  string
	}{
		{"/chunks", true, ""},
		{"/fits", true, ""},
		{"/cut", false, "stichwort fehlt"},
		{"/gzip", true, ""},
		{"/bomb", false, "stichwort fehlt"},
		{"/gzip-kaputt", false, "antwort ungültig"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			tg := httpTarget(srv.URL + tt.path)
			tg.Keyword = kw
			began := time.Now()
			res := Checker{loopback: true}.Run(context.Background(), tg)
			if res.OK != tt.ok || res.Err != tt.err {
				t.Fatalf("got ok=%v err=%q code=%d, want ok=%v err=%q", res.OK, res.Err, res.StatusCode, tt.ok, tt.err)
			}
			if d := time.Since(began); d > 3*time.Second {
				t.Errorf("check lief %v", d)
			}
		})
	}
}
