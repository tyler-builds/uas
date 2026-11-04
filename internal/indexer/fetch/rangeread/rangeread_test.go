package rangeread

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReadAt(t *testing.T) {
	data := make([]byte, 3*ChunkSize+100)
	for i := range data {
		data[i] = byte(i * 7)
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()

	r := New(context.Background(), srv.Client(), srv.URL, int64(len(data)), 1<<30)
	got := make([]byte, 200)
	// Across the boundary of chunks 0 and 1.
	if _, err := r.ReadAt(got, ChunkSize-100); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data[ChunkSize-100:ChunkSize+100]) {
		t.Error("wrong bytes across a chunk boundary")
	}
	// Chunk 1 again: no new request.
	if _, err := r.ReadAt(got[:50], ChunkSize+10); err != nil || requests != 2 {
		t.Errorf("err = %v, requests = %d", err, requests)
	}
	// The short last chunk, read past the end.
	n, err := r.ReadAt(got, int64(len(data))-50)
	if n != 50 || err != io.EOF || !bytes.Equal(got[:50], data[len(data)-50:]) {
		t.Errorf("tail: n = %d, err = %v", n, err)
	}
}

func TestBudget(t *testing.T) {
	data := make([]byte, 4*ChunkSize)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	r := New(context.Background(), srv.Client(), srv.URL, int64(len(data)), 2*ChunkSize)
	if _, err := r.ReadAt(make([]byte, 4*ChunkSize), 0); !errors.Is(err, ErrBudget) {
		t.Errorf("err = %v, want ErrBudget", err)
	}
}

func TestNoRangeSupport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 10))
	}))
	defer srv.Close()
	r := New(context.Background(), srv.Client(), srv.URL, 10, 1<<20)
	if _, err := r.ReadAt(make([]byte, 4), 0); err == nil {
		t.Error("expected an error from a server that ignores Range")
	}
}
