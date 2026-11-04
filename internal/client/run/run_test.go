package run

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestExecStreamsLines(t *testing.T) {
	var lines []string
	out, err := Exec{}.Run(context.Background(), "sh",
		[]string{"-c", `printf 'one\ntwo 10%%\rtwo 50%%\r\nthree'`}, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"one", "two 10%", "two 50%", "three"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("lines = %q, want %q", lines, want)
	}
	if !strings.HasPrefix(out, "one\n") {
		t.Errorf("out = %q", out)
	}
}

func TestExecError(t *testing.T) {
	_, err := Exec{}.Run(context.Background(), "sh", []string{"-c", "echo 'error: no such ref' >&2; exit 3"}, nil)
	var re *Error
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if !strings.Contains(err.Error(), "error: no such ref") {
		t.Errorf("message = %q", err.Error())
	}
}

func TestExecNotFound(t *testing.T) {
	_, err := Exec{}.Run(context.Background(), "uas-no-such-command", nil, nil)
	if !IsNotFound(err) {
		t.Errorf("err = %v, want not found", err)
	}
}
