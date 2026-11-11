package main

import (
	"reflect"
	"testing"
)

func TestNormalizeStatusWatchArgs(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{[]string{"status", "--watch", "0", "--repos"}, []string{"status", "--watch=0", "--repos"}},
		{[]string{"status", "--watch", "007"}, []string{"status", "--watch=007"}},
		{[]string{"status", "--watch", "demo"}, []string{"status", "--watch", "demo"}},
		{[]string{"status", "--watch"}, []string{"status", "--watch"}},
	}
	for _, tc := range cases {
		got, err := normalizeStatusWatchArgs(tc.in)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("watch args %v -> %v; want %v", tc.in, got, tc.want)
		}
	}
	for _, bad := range [][]string{{"status", "--watch", "0abc"}, {"status", "--watch=0abc"}} {
		if _, err := normalizeStatusWatchArgs(bad); err == nil {
			t.Fatalf("malformed watch interval accepted: %v", bad)
		}
	}
}
