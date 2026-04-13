package fpm

import (
	"bytes"
	_ "embed"
	"fmt"
	"mynginx/internal/util"
	"os"
	"path/filepath"
	"sort"
	"text/template"
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
	TemplatePath string // optional override; default template is embedded
}

//go:embed templates/pool.tmpl
var poolTemplate string

func (m *PoolManager) Render(td PoolData) ([]byte, error) {
	tplName := "pool.tmpl"
	tplBody := poolTemplate
	if m.TemplatePath != "" {
		b, err := os.ReadFile(m.TemplatePath)
		if err != nil {
			return nil, fmt.Errorf("read pool template %s: %w", m.TemplatePath, err)
		}
		tplBody = string(b)
		tplName = filepath.Base(m.TemplatePath)
	}
	tpl, err := template.New(tplName).Funcs(template.FuncMap{
		"sortedPairs": sortedPairs,
	}).Parse(tplBody)
	if err != nil {
		return nil, fmt.Errorf("parse pool template: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, tplName, td); err != nil {
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
