package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
)

//go:embed templates static
var files embed.FS

var pages = parse("status", "detail", "login", "admin_list", "admin_form", "notfound", "error")

// jede seite bekommt ein eigenes set, sonst ueberschreiben sich die "main"-bloecke gegenseitig
func parse(names ...string) map[string]*template.Template {
	m := make(map[string]*template.Template, len(names))
	for _, n := range names {
		m[n] = template.Must(template.New(n).Funcs(Funcs()).ParseFS(files, "templates/layout.tmpl", "templates/"+n+".tmpl"))
	}
	return m
}

// Static enthaelt css, js, icon und grain fuer /static/.
func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

// Render schreibt erst, wenn das template komplett durchgelaufen ist. Ein fehler mitten
// im rendern hinterlaesst so keine halbe seite mit status 200.
func Render(w http.ResponseWriter, status int, name string, data any) error {
	body, err := execute(name, data)
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}

func execute(name string, data any) ([]byte, error) {
	t, ok := pages[name]
	if !ok {
		return nil, fmt.Errorf("web: template %q gibt es nicht", name)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		return nil, fmt.Errorf("web: %s rendern: %w", name, err)
	}
	return buf.Bytes(), nil
}
