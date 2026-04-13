package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type tarWriter struct {
	gw *gzip.Writer
	tw *tar.Writer
}

func newTarWriter(w io.Writer) *tarWriter {
	gw := gzip.NewWriter(w)
	return &tarWriter{gw: gw, tw: tar.NewWriter(gw)}
}

func (t *tarWriter) close() error {
	if err := t.tw.Close(); err != nil {
		_ = t.gw.Close()
		return err
	}
	return t.gw.Close()
}

func (t *tarWriter) addBytes(name string, b []byte, mode int64, modTime time.Time) error {
	name = strings.TrimPrefix(filepath.ToSlash(name), "/")
	h := &tar.Header{Name: name, Mode: mode, Size: int64(len(b)), ModTime: modTime}
	if err := t.tw.WriteHeader(h); err != nil {
		return err
	}
	_, err := t.tw.Write(b)
	return err
}

func (t *tarWriter) addFileFromDisk(srcPath, archivePath string) error {
	st, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return nil
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := &tar.Header{
		Name:    strings.TrimPrefix(filepath.ToSlash(archivePath), "/"),
		Mode:    int64(st.Mode().Perm()),
		Size:    st.Size(),
		ModTime: st.ModTime(),
	}
	if err := t.tw.WriteHeader(h); err != nil {
		return err
	}
	_, err = io.Copy(t.tw, f)
	return err
}

func (t *tarWriter) addTree(baseDir, archivePrefix string, skip func(rel string, d fs.DirEntry) bool) (int, error) {
	count := 0
	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if skip != nil && skip(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			if err := t.addFileFromDisk(path, filepath.Join(archivePrefix, rel)); err != nil {
				return fmt.Errorf("add %s: %w", rel, err)
			}
			count++
		}
		return nil
	})
	return count, err
}
