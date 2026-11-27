package asar

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	archive "golift.io/asar"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

type Archive struct {
	reader   *archive.Reader
	file     *os.File
	info     os.FileInfo
	filename string
	files    map[string]*archive.File
	BuildID  string
}

func Open(filename string) (*Archive, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("ASAR_FILE_INVALID")
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	reader, err := archive.NewReader(file, info.Size())
	if err != nil {
		file.Close()
		return nil, err
	}
	a := &Archive{reader: reader, file: file, info: info, filename: filename, files: map[string]*archive.File{}}
	failed := true
	defer func() {
		if failed {
			file.Close()
		}
	}()
	for _, entry := range reader.Files {
		if entry.Name != "webview" && !strings.HasPrefix(entry.Name, "webview/") {
			continue
		}
		if !fs.ValidPath(entry.Name) || strings.Contains(entry.Name, "\\") || entry.IsLink() || entry.Unpacked {
			return nil, errors.New("ASAR_ENTRY_UNSAFE")
		}
		if !entry.IsDir() {
			a.files[strings.TrimPrefix(entry.Name, "webview/")] = entry
		}
	}
	entry, err := a.Read("index.html")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(a.files))
	for name := range a.files {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	hash.Write(entry)
	for _, name := range names {
		f := a.files[name]
		data, err := json.Marshal(struct {
			Name      string
			Size      int64
			Integrity *archive.Integrity
		}{name, f.Size, f.Integrity})
		if err != nil {
			return nil, err
		}
		hash.Write(data)
	}
	a.BuildID = hex.EncodeToString(hash.Sum(nil))
	failed = false
	return a, nil
}
func (a *Archive) Close() error { return a.file.Close() }
func (a *Archive) Consistent(expected string) error {
	info, err := os.Lstat(a.filename)
	if err != nil {
		return err
	}
	if expected != a.BuildID || !os.SameFile(info, a.info) || info.Size() != a.info.Size() || !info.ModTime().Equal(a.info.ModTime()) {
		return errors.New("ASSET_BUILD_CHANGED")
	}
	return nil
}
func (a *Archive) Read(name string) ([]byte, error) {
	if !fs.ValidPath(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00") {
		return nil, fs.ErrPermission
	}
	entry := a.files[name]
	if entry == nil {
		return nil, fs.ErrNotExist
	}
	reader, err := entry.Open()
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if entry.Integrity != nil && entry.Integrity.Algorithm == "SHA256" {
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != entry.Integrity.Hash {
			return nil, errors.New("ASAR_INTEGRITY_INVALID")
		}
	}
	return data, nil
}
