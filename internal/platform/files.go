// Package platform composes the Server and manages its local execution ends.
package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func DefaultRoot() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "astral"), nil
}
func WriteJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivate(path, append(raw, '\n'))
}
func WritePrivate(path string, raw []byte) error {
	if err := privateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".astral-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = ProtectPrivate(name); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func ReadJSON(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}
func Lock(path string) (*os.File, error) {
	if err := privateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("another Astral process already owns %s", path)
	}
	if err = file.Truncate(0); err == nil {
		_, err = fmt.Fprintln(file, os.Getpid())
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return ProtectPrivate(path)
}
