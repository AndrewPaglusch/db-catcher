package main

import (
	"bytes"
	"errors"
	"io"

	"github.com/klauspost/compress/zstd"
)

var (
	errEmpty    = errors.New("empty dump")
	errNoMarker = errors.New("completion marker not found")
)

// tailSize is how close to the end the completion marker must be. Dump tools
// write a little after it: mysqldump appends the date, and pg_dump 17.6+
// adds an \unrestrict line.
const tailSize = 4096

// dumpValidator passes a dump through unchanged, remembering only its size
// and last few bytes. At the end of the dump it replaces io.EOF with an error
// if the dump was empty or incomplete, so whatever is consuming the stream
// sees a failed read and gives up.
type dumpValidator struct {
	src    io.Reader
	marker []byte
	size   int64
	tail   []byte
	err    error
}

func newDumpValidator(src io.Reader, marker string) *dumpValidator {
	return &dumpValidator{src: src, marker: []byte(marker)}
}

func (v *dumpValidator) Read(p []byte) (int, error) {
	n, err := v.src.Read(p)
	v.size += int64(n)
	v.tail = append(v.tail, p[:n]...)
	if len(v.tail) > tailSize {
		v.tail = v.tail[len(v.tail)-tailSize:]
	}

	if err == io.EOF {
		switch {
		case v.size == 0:
			err = errEmpty
		case !bytes.Contains(v.tail, v.marker):
			err = errNoMarker
		}
	}
	if err != nil && err != io.EOF {
		v.err = err
	}
	return n, err
}

// invalid reports whether the dump arrived in full but failed validation.
func (v *dumpValidator) invalid() bool {
	return v.err == errEmpty || v.err == errNoMarker
}

func compress(dst io.Writer, src io.Reader) error {
	zw, err := zstd.NewWriter(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(zw, src); err != nil {
		zw.Close()
		return err
	}
	return zw.Close()
}
