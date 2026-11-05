package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"filippo.io/age"
)

func isFile(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }
func isDir(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readJSON(p string, v any) error {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSON(p string, v any) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o600)
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ---- age -------------------------------------------------------------------

func loadIdentities(p string) ([]age.Identity, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("age key not found: %s", p)
	}
	defer f.Close()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("reading age key %s: %w", p, err)
	}
	return ids, nil
}

func loadX25519Identity(p string) (*age.X25519Identity, error) {
	ids, err := loadIdentities(p)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if x, ok := id.(*age.X25519Identity); ok {
			return x, nil
		}
	}
	return nil, fmt.Errorf("no X25519 age key found in %s", p)
}

func (a *app) encryptFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w, err := age.Encrypt(out, a.recips...)
	if err != nil {
		out.Close()
		return err
	}
	if _, err := io.Copy(w, in); err != nil {
		out.Close()
		return err
	}
	if err := w.Close(); err != nil { // flushes the last chunk
		out.Close()
		return err
	}
	return out.Close()
}

func (a *app) decryptFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	r, err := age.Decrypt(in, a.ids...)
	if err != nil {
		return fmt.Errorf("decrypting %s: %w", filepath.Base(src), err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// decryptedHash hashes the plaintext of an .age file in memory, without writing it anywhere.
func (a *app) decryptedHash(src string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	r, err := age.Decrypt(in, a.ids...)
	if err != nil {
		return "", fmt.Errorf("decrypting %s: %w", filepath.Base(src), err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
