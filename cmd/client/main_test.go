package main

import (
	"reflect"
	"testing"
)

func TestNewPeerID(t *testing.T) {
	id1, err := newPeerID()
	if err != nil {
		t.Fatalf("newPeerID() error: %v", err)
	}
	if string(id1[:8]) != "-RZ0001-" {
		t.Fatalf("prefix = %q, want -RZ0001-", string(id1[:8]))
	}
	id2, err := newPeerID()
	if err != nil {
		t.Fatalf("second newPeerID() error: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("expected distinct random peer IDs, got identical %x", id1)
	}
}

func TestParseStaticPeers(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{raw: "", want: nil},
		{raw: "   ", want: nil},
		{raw: "127.0.0.1:6881", want: []string{"127.0.0.1:6881"}},
		{raw: "127.0.0.1:6881, 127.0.0.1:6882 ,", want: []string{"127.0.0.1:6881", "127.0.0.1:6882"}},
	}
	for _, tc := range cases {
		got := parseStaticPeers(tc.raw)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseStaticPeers(%q) = %#v, want %#v", tc.raw, got, tc.want)
		}
	}
}
