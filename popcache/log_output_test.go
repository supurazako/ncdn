package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingLogWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "l7lb.log")
	writer, err := newRotatingLogWriter(path, 10, 2)
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{"aaaa\n", "bbbb\n", "cccc\n"} {
		if _, err := writer.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "cccc\n" {
		t.Fatalf("current log = %q", current)
	}
	if string(previous) != "aaaa\nbbbb\n" {
		t.Fatalf("previous log = %q", previous)
	}
}
