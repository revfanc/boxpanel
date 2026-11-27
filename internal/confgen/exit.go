package confgen

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"

	"github.com/revfanc/boxpanel/internal/exitlink"
	"github.com/revfanc/boxpanel/internal/fwmark"
)

func addExitInbounds(cfg *config, incoming []exitlink.Inbound, opts Options, tags map[string]bool, ports map[int]bool) error {
	if len(incoming) == 0 {
		return nil
	}
	incoming = slices.Clone(incoming)
	slices.SortFunc(incoming, func(a, b exitlink.Inbound) int { return compareID(a.ID, b.ID) })
	guards := make([]routeRule, 0, len(incoming)+len(cfg.Route.Rules)+1)
	forwardTags := make([]string, 0, len(incoming))
	for _, in := range incoming {
		if err := in.Validate(); err != nil {
			return err
		}
		tag := exitlink.InboundPrefix + strconv.FormatInt(in.ID, 10)
		if tags[tag] || ports[in.Port] || in.Port == opts.ClashAPIPort {
			return fmt.Errorf("exit inbound needs a unique tag and a port distinct from the Clash API and user inbounds")
		}
		tags[tag], ports[in.Port] = true, true
		cfg.Inbounds = append(cfg.Inbounds, inboundConfig{
			Type: "shadowsocks", Tag: tag, Listen: "::", ListenPort: in.Port,
			Method: exitlink.Method, Password: in.Password,
		})
		ip := netip.MustParseAddr(in.Source)
		guards = append(guards, routeRule{Type: "logical", Mode: "and", Action: "reject", Rules: []routeRule{
			{Inbound: []string{tag}}, {SourceIP: []string{netip.PrefixFrom(ip, ip.BitLen()).String()}, Invert: true},
		}})
		forwardTags = append(forwardTags, tag)
	}
	cfg.Route.Rules = append(append(guards, cfg.Route.Rules...), routeRule{Inbound: forwardTags, Action: "route", Outbound: exitlink.ForwardTag})
	forward := outboundConfig{Type: "direct", Tag: exitlink.ForwardTag}
	if opts.UserRoutingMarks {
		forward.RoutingMark = fwmark.ExitForward
	}
	cfg.Outbounds = append(cfg.Outbounds, forward)
	return nil
}

func addExitProbe(cfg *config, snapshot Snapshot) (*exitlink.Outbound, routeRule, error) {
	if snapshot.Exit.Outgoing == nil {
		return nil, routeRule{}, nil
	}
	out := *snapshot.Exit.Outgoing
	if err := out.Validate(); err != nil {
		return nil, routeRule{}, err
	}
	cfg.Outbounds = append(cfg.Outbounds, exitOutbound(out, out.Revision.ProbeTag(), 0))
	var match routeRule
	if out.Mode == exitlink.Split {
		match.Domain, match.Suffix = out.Rules.Domains, out.Rules.Suffixes()
	}
	return &out, match, nil
}

func addUserExitRoute(cfg *config, exit *exitlink.Outbound, match routeRule, probe exitlink.Probe, mark uint32, user User, tags []string) {
	if exit == nil || len(tags) == 0 || exit.Mode == exitlink.Split && len(match.Domain) == 0 && len(match.Suffix) == 0 {
		return
	}
	down := probe.DownFor(exit.Revision)
	if down && exit.Failure == exitlink.Fallback {
		return
	}
	match.Inbound, match.AuthUser = tags, []string{user.Name}
	if down {
		match.Action = "reject"
	} else {
		tag := "bp-exit-u" + strconv.FormatInt(user.ID, 10)
		cfg.Outbounds = append(cfg.Outbounds, exitOutbound(*exit, tag, mark))
		match.Action, match.Outbound = "route", tag
	}
	cfg.Route.Rules = append(cfg.Route.Rules, match)
}

func exitOutbound(exit exitlink.Outbound, tag string, mark uint32) outboundConfig {
	return outboundConfig{
		Type: "shadowsocks", Tag: tag, RoutingMark: mark, Server: exit.Address,
		ServerPort: exit.Port, Method: exitlink.Method, Password: exit.Password, ConnectTimeout: "4s",
	}
}
