package auth

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/GehirnInc/crypt"
	"github.com/openwall/yescrypt-go"
	"golang.org/x/crypto/bcrypt"
)

var errInvalidShadow = errors.New("invalid shadow entry")

func readShadowHash(username string) (string, error) {
	f, err := os.Open("/etc/shadow")
	if err != nil {
		return "", err
	}
	defer f.Close()

	needle := strings.TrimSpace(username) + ":"
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if !strings.HasPrefix(line, needle) {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < 2 {
			return "", errInvalidShadow
		}
		h := strings.TrimSpace(parts[1])
		if h == "" || h == "!" || h == "!!" || h == "*" {
			return "", errInvalidShadow
		}
		return h, nil
	}
	if err := s.Err(); err != nil {
		return "", err
	}
	return "", os.ErrNotExist
}

func VerifyShadowPassword(username, password string) error {
	hash, err := readShadowHash(username)
	if err != nil {
		return fmt.Errorf("verify shadow: %w", err)
	}
	switch {
	case strings.HasPrefix(hash, "$y$"):
		computed, err := yescrypt.Hash([]byte(password), []byte(hash))
		if err != nil {
			return err
		}
		if !bytes.Equal(computed, []byte(hash)) {
			return errors.New("invalid credentials")
		}
		return nil
	case strings.HasPrefix(hash, "$2a$"), strings.HasPrefix(hash, "$2b$"):
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	case strings.HasPrefix(hash, "$1$"), strings.HasPrefix(hash, "$5$"), strings.HasPrefix(hash, "$6$"):
		c := crypt.NewFromHash(hash)
		if c == nil {
			return errors.New("unsupported hash")
		}
		out, err := c.Generate([]byte(password), []byte(hash))
		if err != nil {
			return err
		}
		if !bytes.Equal([]byte(out), []byte(hash)) {
			return errors.New("invalid credentials")
		}
		return nil
	default:
		return errors.New("unsupported hash")
	}
}
