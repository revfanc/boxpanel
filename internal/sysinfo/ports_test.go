package sysinfo

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestListeningPortsSnapshot(t *testing.T) {
	for _, tt := range []struct {
		output    string
		err       error
		wantError bool
	}{
		{"tcp LISTEN 0 128 [::]:443 *:*\nudp UNCONN 0 0 0.0.0.0:8443 *:*", nil, false},
		{"bad output", nil, true}, {"", errors.New("ss failed"), true},
	} {
		r := &fakeRunner{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "ss" || !reflect.DeepEqual(args, []string{"-H", "-lntu"}) {
				t.Fatal(name, args)
			}
			return []byte(tt.output), tt.err
		}}
		got, err := ListeningPorts(context.Background(), r)
		if (err != nil) != tt.wantError {
			t.Fatal(err)
		}
		if !tt.wantError && !reflect.DeepEqual(got, map[Socket]bool{{443, "tcp"}: true, {8443, "udp"}: true}) {
			t.Fatal(got)
		}
	}
}
