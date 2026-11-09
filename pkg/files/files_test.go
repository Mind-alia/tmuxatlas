package files

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func request(t *testing.T, handler http.HandlerFunc, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(method, target, body))
	return recorder
}

func q(path string) string { return url.QueryEscape(path) }

func TestListSortsFoldersFirstAndDescribesLinks(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "zeta"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "Alpha.txt"), []byte("a"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, ".hidden"), nil, 0o644))
	must(t, os.Symlink(filepath.Join(root, "zeta"), filepath.Join(root, "link")))
	must(t, os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken")))
	h := NewWithHome(root)

	res := request(t, h.List, http.MethodGet, "/api/files/list", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status %d: %s", res.Code, res.Body)
	}
	var listing Listing
	must(t, json.Unmarshal(res.Body.Bytes(), &listing))
	if listing.Path != root || listing.Home != root {
		t.Fatalf("path %q home %q", listing.Path, listing.Home)
	}
	var names []string
	for _, entry := range listing.Entries {
		names = append(names, entry.Name)
	}
	if got := strings.Join(names, ","); got != "link,zeta,.hidden,Alpha.txt,broken" {
		t.Fatalf("order %s", got)
	}
	if !listing.Entries[0].LinkDir || listing.Entries[0].Type != "symlink" || !listing.Entries[2].Hidden {
		t.Fatalf("entries %+v", listing.Entries)
	}
	if !listing.Entries[4].Broken {
		t.Fatalf("broken link not flagged: %+v", listing.Entries[4])
	}
}

func TestListRejectsRelativePathsAndFiles(t *testing.T) {
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "f"), nil, 0o644))
	h := NewWithHome(root)
	for name, target := range map[string]string{
		"relative": "/api/files/list?path=etc",
		"file":     "/api/files/list?path=" + q(filepath.Join(root, "f")),
		"missing":  "/api/files/list?path=" + q(filepath.Join(root, "nope")),
	} {
		t.Run(name, func(t *testing.T) {
			if res := request(t, h.List, http.MethodGet, target, nil); res.Code < 400 {
				t.Fatalf("status %d", res.Code)
			}
		})
	}
}

func TestReadDetectsTextAndBinary(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name   string
		data   []byte
		binary bool
	}{
		{"notes.md", []byte("# Titre\nété ✓\n"), false},
		{"empty.txt", nil, false},
		{"image.png", []byte{0x89, 'P', 'N', 'G', 0, 0, 1}, true},
		{"latin1.txt", []byte{'c', 'a', 'f', 0xe9}, true},
	}
	h := NewWithHome(root)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name)
			must(t, os.WriteFile(path, tc.data, 0o644))
			res := request(t, h.Read, http.MethodGet, "/api/files/content?path="+q(path), nil)
			if res.Code != http.StatusOK {
				t.Fatalf("status %d: %s", res.Code, res.Body)
			}
			var content Content
			must(t, json.Unmarshal(res.Body.Bytes(), &content))
			if content.Binary != tc.binary {
				t.Fatalf("binary = %v", content.Binary)
			}
			if !tc.binary && content.Content != string(tc.data) {
				t.Fatalf("content %q", content.Content)
			}
			if content.Version == "" {
				t.Fatal("missing version")
			}
		})
	}
}

func TestLooksBinaryAllowsRuneCutAtSampleEnd(t *testing.T) {
	sample := []byte("abc é")
	if looksBinary(sample[:len(sample)-1], false) {
		t.Fatal("a rune cut by the sample boundary is still text")
	}
	if !looksBinary(sample[:len(sample)-1], true) {
		t.Fatal("a cut rune at the real end of the file is not UTF-8")
	}
}

func readVersion(t *testing.T, h *Handler, path string) string {
	t.Helper()
	var content Content
	must(t, json.Unmarshal(request(t, h.Read, http.MethodGet, "/api/files/content?path="+q(path), nil).Body.Bytes(), &content))
	return content.Version
}

func TestWriteChecksVersionAndKeepsMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "script.sh")
	must(t, os.WriteFile(path, []byte("echo 1\n"), 0o644))
	must(t, os.Chmod(path, 0o750))
	h := NewWithHome(root)
	ver := readVersion(t, h, path)

	stale := request(t, h.Write, http.MethodPut, "/api/files/content?path="+q(path)+"&version=old", strings.NewReader("x"))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale write status %d", stale.Code)
	}
	res := request(t, h.Write, http.MethodPut, "/api/files/content?path="+q(path)+"&version="+ver, strings.NewReader("echo 2\n"))
	if res.Code != http.StatusOK {
		t.Fatalf("status %d: %s", res.Code, res.Body)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(data) != "echo 2\n" || info.Mode().Perm() != 0o750 {
		t.Fatalf("data %q mode %v", data, info.Mode())
	}
	var saved Saved
	must(t, json.Unmarshal(res.Body.Bytes(), &saved))
	if saved.Version == ver || saved.Version != readVersion(t, h, path) {
		t.Fatalf("saved version %q", saved.Version)
	}
	// The same, now stale, version is refused; force overrides.
	if res := request(t, h.Write, http.MethodPut, "/api/files/content?path="+q(path)+"&version="+ver, strings.NewReader("y")); res.Code != http.StatusConflict {
		t.Fatalf("second stale write status %d", res.Code)
	}
	if res := request(t, h.Write, http.MethodPut, "/api/files/content?path="+q(path)+"&force=1", strings.NewReader("y")); res.Code != http.StatusOK {
		t.Fatalf("forced write status %d", res.Code)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("temporary file left behind: %v", entries)
	}
}

func TestWriteThroughSymlinkKeepsLink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real.txt")
	link := filepath.Join(root, "link.txt")
	must(t, os.WriteFile(target, []byte("a"), 0o644))
	must(t, os.Symlink(target, link))
	h := NewWithHome(root)
	res := request(t, h.Write, http.MethodPut, "/api/files/content?path="+q(link)+"&version="+readVersion(t, h, link), strings.NewReader("b"))
	if res.Code != http.StatusOK {
		t.Fatalf("status %d: %s", res.Code, res.Body)
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced by a file")
	}
	if data, _ := os.ReadFile(target); string(data) != "b" {
		t.Fatalf("target %q", data)
	}
}

func TestWriteNewFileRequiresNoVersion(t *testing.T) {
	root := t.TempDir()
	h := NewWithHome(root)
	path := filepath.Join(root, "new.txt")
	if res := request(t, h.Write, http.MethodPut, "/api/files/content?path="+q(path), strings.NewReader("hi")); res.Code != http.StatusOK {
		t.Fatalf("status %d", res.Code)
	}
	if res := request(t, h.Write, http.MethodPut, "/api/files/content?path="+q(path), strings.NewReader("again")); res.Code != http.StatusConflict {
		t.Fatalf("overwrite without version: status %d", res.Code)
	}
}

func TestRawServesImagesInlineAndOthersAsAttachment(t *testing.T) {
	root := t.TempDir()
	svg := filepath.Join(root, "logo.svg")
	html := filepath.Join(root, "page.html")
	must(t, os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644))
	must(t, os.WriteFile(html, []byte(`<script>alert(1)</script>`), 0o644))
	h := NewWithHome(root)

	res := request(t, h.Raw, http.MethodGet, "/api/files/raw?path="+q(svg), nil)
	if res.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(res.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("svg headers %v", res.Header())
	}
	if !strings.Contains(res.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("svg served without sandbox")
	}
	res = request(t, h.Raw, http.MethodGet, "/api/files/raw?path="+q(html), nil)
	if res.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(res.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("html headers %v", res.Header())
	}
	res = request(t, h.Raw, http.MethodGet, "/api/files/raw?download=1&path="+q(svg), nil)
	if !strings.HasPrefix(res.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download headers %v", res.Header())
	}
}

func jsonBody(value any) io.Reader {
	data, _ := json.Marshal(value)
	return bytes.NewReader(data)
}

func TestFileOperations(t *testing.T) {
	root := t.TempDir()
	h := NewWithHome(root)
	dir := filepath.Join(root, "docs")
	if res := request(t, h.Mkdir, http.MethodPost, "/", jsonBody(map[string]string{"path": dir})); res.Code != http.StatusCreated {
		t.Fatalf("mkdir %d", res.Code)
	}
	file := filepath.Join(dir, "a.md")
	if res := request(t, h.Create, http.MethodPost, "/", jsonBody(map[string]string{"path": file})); res.Code != http.StatusCreated {
		t.Fatalf("create %d", res.Code)
	}
	if res := request(t, h.Create, http.MethodPost, "/", jsonBody(map[string]string{"path": file})); res.Code != http.StatusConflict {
		t.Fatalf("create twice %d", res.Code)
	}
	moved := filepath.Join(root, "b.md")
	if res := request(t, h.Rename, http.MethodPost, "/", jsonBody(map[string]string{"from": file, "to": moved})); res.Code != http.StatusOK {
		t.Fatalf("rename %d: %s", res.Code, res.Body)
	}
	if res := request(t, h.Rename, http.MethodPost, "/", jsonBody(map[string]string{"from": dir, "to": filepath.Join(dir, "x")})); res.Code != http.StatusBadRequest {
		t.Fatalf("move into itself %d", res.Code)
	}
	must(t, os.WriteFile(filepath.Join(dir, "keep"), nil, 0o644))
	if res := request(t, h.Delete, http.MethodPost, "/", jsonBody(map[string]any{"path": dir})); res.Code != http.StatusConflict {
		t.Fatalf("delete non-empty without recursive %d", res.Code)
	}
	if res := request(t, h.Delete, http.MethodPost, "/", jsonBody(map[string]any{"path": dir, "recursive": true})); res.Code != http.StatusNoContent {
		t.Fatalf("delete recursive %d", res.Code)
	}
	for _, protected := range []string{"/", root} {
		if res := request(t, h.Delete, http.MethodPost, "/", jsonBody(map[string]any{"path": protected, "recursive": true})); res.Code != http.StatusBadRequest {
			t.Fatalf("delete %s: %d", protected, res.Code)
		}
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
}

func TestUploadCreatesParentsAndRefusesOverwrite(t *testing.T) {
	root := t.TempDir()
	h := NewWithHome(root)
	path := filepath.Join(root, "drop", "nested", "photo.jpg")
	target := "/api/files/upload?path=" + q(path)
	if res := request(t, h.Upload, http.MethodPost, target, strings.NewReader("jpeg")); res.Code != http.StatusCreated {
		t.Fatalf("upload %d: %s", res.Code, res.Body)
	}
	if res := request(t, h.Upload, http.MethodPost, target, strings.NewReader("other")); res.Code != http.StatusConflict {
		t.Fatalf("second upload %d", res.Code)
	}
	if res := request(t, h.Upload, http.MethodPost, target+"&overwrite=1", strings.NewReader("new")); res.Code != http.StatusCreated {
		t.Fatalf("overwrite %d", res.Code)
	}
	if data, _ := os.ReadFile(path); string(data) != "new" {
		t.Fatalf("content %q", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}

	small := &Handler{home: root, maxUpload: 3}
	if res := request(t, small.Upload, http.MethodPost, "/api/files/upload?path="+q(filepath.Join(root, "big")), strings.NewReader("toolong")); res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize %d", res.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "big")); !os.IsNotExist(err) {
		t.Fatal("oversize upload left a file")
	}
}

func TestArchiveZipsFolder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "project")
	must(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644))
	must(t, os.Symlink(dir, filepath.Join(dir, "loop")))
	h := NewWithHome(root)
	res := request(t, h.Archive, http.MethodGet, "/api/files/archive?path="+q(dir), nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status %d", res.Code)
	}
	reader, err := zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len()))
	must(t, err)
	names := map[string]bool{}
	for _, file := range reader.File {
		names[file.Name] = true
	}
	for _, want := range []string{"project/src/", "project/src/main.go", "project/loop"} {
		if !names[want] {
			t.Fatalf("missing %s in %v", want, names)
		}
	}
}

func TestCleanPathExpandsHome(t *testing.T) {
	h := NewWithHome("/home/me")
	for input, want := range map[string]string{"": "/home/me", "~": "/home/me", "~/a/../b": "/home/me/b", "/etc/": "/etc"} {
		got, err := h.cleanPath(input)
		if err != nil || got != want {
			t.Fatalf("%q -> %q, %v", input, got, err)
		}
	}
	if _, err := h.cleanPath("~other"); err == nil {
		t.Fatal("~other is relative")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
