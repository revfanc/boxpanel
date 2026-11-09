package sysinfo

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestBBRDetection(t *testing.T) {
	for _, tt := range []struct {
		current, available             string
		module, enabled, wantAvailable bool
	}{{"cubic", "reno cubic bbr", false, false, true}, {"bbr", "reno bbr", false, true, true}, {"cubic", "reno cubic", true, false, true}, {"cubic", "reno cubic", false, false, false}} {
		s, f, r := fixture()
		f.data[congestionPath] = tt.current
		f.data[availablePath] = tt.available
		r.paths["modinfo"] = tt.module
		r.run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
		state, err := s.DetectBBR(context.Background())
		if err != nil || state != (BBR{tt.enabled, tt.wantAvailable}) {
			t.Fatalf("state=%+v %v", state, err)
		}
		if len(f.writes) != 0 {
			t.Fatal("detect wrote configuration")
		}
	}
}

func TestEnableBBRAndRollback(t *testing.T) {
	for _, tt := range []struct {
		name                                  string
		applyFail, verifyFail, exists, module bool
	}{{name: "success"}, {name: "module", module: true}, {name: "apply failure", applyFail: true}, {name: "restore previous config", applyFail: true, exists: true}, {name: "verify failure", verifyFail: true}} {
		t.Run(tt.name, func(t *testing.T) {
			s, f, r := fixture()
			if tt.exists {
				f.data[bbrConfigPath] = "# previous custom content\n"
			}
			if tt.module {
				f.data[availablePath] = "reno cubic"
				r.paths["modinfo"] = true
			}
			r.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "modinfo" {
					return nil, nil
				}
				if name == "modprobe" {
					f.data[availablePath] = "reno cubic bbr"
					return nil, nil
				}
				if args[0] == "-p" {
					f.data[qdiscPath] = "fq"
					if tt.applyFail {
						return nil, fs.ErrPermission
					}
					if !tt.verifyFail {
						f.data[congestionPath] = "bbr"
					}
				} else {
					if ctx.Err() != nil {
						t.Fatal("rollback canceled")
					}
					if strings.Join(args, " ") != "-w net.core.default_qdisc=fq_codel net.ipv4.tcp_congestion_control=cubic" {
						t.Fatalf("bad rollback %v", args)
					}
					f.data[qdiscPath] = "fq_codel"
					f.data[congestionPath] = "cubic"
				}
				return nil, nil
			}
			state, err := s.EnableBBR(context.Background())
			if tt.applyFail || tt.verifyFail {
				if err == nil || state.Enabled {
					t.Fatal("failure reported success")
				}
				if f.data[congestionPath] != "cubic" || f.data[qdiscPath] != "fq_codel" {
					t.Fatal("runtime not restored")
				}
				if tt.exists {
					if f.data[bbrConfigPath] != "# previous custom content\n" {
						t.Fatal("file not restored")
					}
				} else {
					if _, ok := f.data[bbrConfigPath]; ok {
						t.Fatal("new file not removed")
					}
				}
			} else {
				if err != nil || !state.Enabled || f.data[bbrConfigPath] != bbrConfig {
					t.Fatalf("enable=%+v %v", state, err)
				}
				if _, err := s.EnableBBR(context.Background()); err != nil {
					t.Fatalf("repeated enable: %v", err)
				}
			}
		})
	}
}

func TestBBRFailureBoundaries(t *testing.T) {
	s, f, r := fixture()
	f.data[availablePath] = "reno cubic"
	if _, err := s.EnableBBR(context.Background()); !errors.Is(err, ErrBBRUnavailable) {
		t.Fatal(err)
	}
	if len(f.writes) != 0 || len(r.calls) != 0 {
		t.Fatal("unavailable BBR modified host")
	}
	f.data[availablePath] = "bbr cubic"
	f.errs["write:"+bbrConfigPath] = fs.ErrPermission
	if _, err := s.EnableBBR(context.Background()); err == nil {
		t.Fatal("ignored write failure")
	}
	if len(r.calls) != 0 {
		t.Fatal("applied after persistence failure")
	}
	delete(f.errs, "write:"+bbrConfigPath)
	f.data[congestionPath] = "cubic\nmalicious"
	if _, err := s.EnableBBR(context.Background()); err == nil {
		t.Fatal("accepted invalid kernel parameter")
	}
}

func TestBBRCancellationAndRollbackFailure(t *testing.T) {
	s, f, r := fixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rollbackFailure := errors.New("rollback failed")
	r.run = func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "-p" {
			cancel()
			return nil, context.Canceled
		}
		if ctx.Err() != nil {
			t.Fatal("rollback inherited canceled context")
		}
		return nil, rollbackFailure
	}
	_, err := s.EnableBBR(ctx)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, rollbackFailure) {
		t.Fatalf("lost failure: %v", err)
	}
	if _, ok := f.data[bbrConfigPath]; ok {
		t.Fatal("file rollback skipped")
	}
}
