package main

import (
	"errors"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/buildexecutor"
)

func TestRunnerFailureCode(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{err: buildexecutor.Error{Code: "SOURCE_ACCESS_DENIED"}, want: "SOURCE_ACCESS_DENIED"},
		{err: buildexecutor.Error{Code: "SOURCE_TOKEN_UNAVAILABLE"}, want: "SOURCE_TOKEN_UNAVAILABLE"},
		{err: errors.New("transport failed"), want: "EXECUTOR_INFRASTRUCTURE_FAILED"},
	} {
		if got := runnerFailureCode(test.err); got != test.want {
			t.Fatalf("runnerFailureCode(%v)=%q want=%q", test.err, got, test.want)
		}
	}
}
