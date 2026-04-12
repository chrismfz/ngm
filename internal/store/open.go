package store

import (
	"fmt"
	"strings"
)

type openerFunc func(dsn string) (SiteStore, error)

var openers = map[string]openerFunc{}

func RegisterDriver(name string, fn openerFunc) {
	if strings.TrimSpace(name) == "" || fn == nil {
		return
	}
	openers[strings.ToLower(strings.TrimSpace(name))] = fn
}

func Open(driver, dsn string) (SiteStore, error) {
	name := strings.ToLower(strings.TrimSpace(driver))
	if name == "" {
		name = "sqlite"
	}
	opener, ok := openers[name]
	if !ok {
		return nil, fmt.Errorf("unsupported storage driver: %s", driver)
	}
	return opener(dsn)
}
