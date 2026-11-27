package config

import "os"

func protectFile(path string, mode os.FileMode) error { return nil }
func replaceFile(from, to string) error               { return os.Rename(from, to) }
