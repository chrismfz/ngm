package fpm

import (
	"bytes"
	"fmt"
	"path/filepath"
	"text/template"
	"sort"
	"mynginx/internal/util"
)

type PoolData struct {
	PoolName    string
	RunUser     string
	RunGroup    string
	Socket      string
	ListenOwner string
	ListenGroup string

	MaxChildren int
	IdleTimeout string
	MaxRequests int

	RequestTerminateTimeout string
	SlowlogTimeout          string
	SlowlogPath             string

	ErrorLog string

	PHPAdminValues map[string]string
	PHPValues      map[string]string
}


type KV struct{ Key, Value string }

func sortedPairs(m map[string]string) []KV {
        if len(m) == 0 {
                return nil
        }
        keys := make([]string, 0, len(m))
        for k := range m {
                keys = append(keys, k)
        }
        sort.Strings(keys)
        out := make([]KV, 0, len(keys))
        for _, k := range keys {
                out = append(out, KV{Key: k, Value: m[k]})
        }
        return out
}


type PoolManager struct {
	TemplatePath string // internal/fpm/templates/pool.tmpl (resolved at build/deploy time)
}

func (m *PoolManager) Render(td PoolData) ([]byte, error) {
	tplPath := m.TemplatePath
	if tplPath == "" {
		tplPath = filepath.Join("internal", "fpm", "templates", "pool.tmpl")
	}
        tpl, err := template.New("pool").Funcs(template.FuncMap{
                "sortedPairs": sortedPairs,
        }).ParseFiles(tplPath)
	if err != nil {
		return nil, fmt.Errorf("parse pool template %s: %w", tplPath, err)
	}
	var buf bytes.Buffer
        // ParseFiles names the template after the base filename; ExecuteTemplate is safest.
        if err := tpl.ExecuteTemplate(&buf, filepath.Base(tplPath), td); err != nil {
		return nil, fmt.Errorf("exec pool template: %w", err)
	}
	return buf.Bytes(), nil
}


func writePoolFileAtomic(path string, data []byte) error {
	// util.WriteFileAtomic requires parent dir to exist.
	dir := filepath.Dir(path)
	if err := util.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return util.WriteFileAtomic(path, data, 0644)
}
