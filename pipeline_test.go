package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/klauspost/compress/zstd"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDumpValidator(t *testing.T) {
	postgres := readFixture(t, "postgres.sql")
	mysql := readFixture(t, "mysql.sql")
	padding := strings.Repeat("INSERT INTO t VALUES (1);\n", 1000)

	tests := []struct {
		name    string
		dump    string
		engine  string
		wantErr error
	}{
		{"postgres", string(postgres), "postgres", nil},
		{"mysql", string(mysql), "mysql", nil},
		{"mysql without dump date", "CREATE TABLE t (id int);\n-- Dump completed\n", "mysql", nil},
		{"marker near the end of a long dump", padding + string(postgres), "postgres", nil},
		{"empty", "", "postgres", errEmpty},
		{"truncated", string(postgres[:len(postgres)/2]), "postgres", errNoMarker},
		{"other engine's marker", string(mysql), "postgres", errNoMarker},
		{"marker too far from the end", string(postgres) + padding, "postgres", errNoMarker},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// OneByteReader makes sure a marker split across reads is still found.
			src := iotest.OneByteReader(strings.NewReader(tt.dump))
			v := newDumpValidator(src, engines[tt.engine].marker)

			got, err := io.ReadAll(v)

			if err != tt.wantErr {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if string(got) != tt.dump {
				t.Error("validator changed the data passing through it")
			}
		})
	}
}

func TestCompressRoundTrip(t *testing.T) {
	dump := readFixture(t, "postgres.sql")

	var compressed bytes.Buffer
	if err := compress(&compressed, bytes.NewReader(dump)); err != nil {
		t.Fatal(err)
	}

	if got := decompress(t, compressed.Bytes()); !bytes.Equal(got, dump) {
		t.Error("decompressed data differs from the original")
	}
}

func decompress(t *testing.T, data []byte) []byte {
	t.Helper()
	zr, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
