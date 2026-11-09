package sysinfo

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func parseOSRelease(data []byte) (string, error) {
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || (key != "ID" && key != "VERSION_ID") {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			var err error
			value, err = strconv.Unquote(value)
			if err != nil {
				return "", fmt.Errorf("parse os-release %s: %w", key, err)
			}
		} else if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") && len(value) >= 2 {
			value = value[1 : len(value)-1]
		}
		if value == "" || strings.ContainsAny(value, "\n\r\x00") {
			return "", fmt.Errorf("invalid os-release %s", key)
		}
		values[key] = value
	}
	if values["ID"] == "" {
		return "", fmt.Errorf("missing os-release ID")
	}
	return strings.TrimSpace(values["ID"] + " " + values["VERSION_ID"]), nil
}

type cpuCounters struct{ total, idle uint64 }

func parseCPU(data []byte) (cpuCounters, error) {
	var counters cpuCounters
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		if len(fields) < 5 {
			break
		}
		// guest/guest_nice are already included in user/nice. Do not double count.
		for i, field := range fields[1:min(len(fields), 9)] {
			n, err := strconv.ParseUint(field, 10, 64)
			if err != nil || math.MaxUint64-counters.total < n {
				return counters, fmt.Errorf("invalid CPU counters")
			}
			counters.total += n
			if i == 3 || i == 4 {
				counters.idle += n
			}
		}
		return counters, nil
	}
	return counters, fmt.Errorf("missing aggregate CPU counters")
}

func parseMemory(data []byte) (Usage, error) {
	values := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (fields[0] != "MemTotal:" && fields[0] != "MemAvailable:") {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return Usage{}, fmt.Errorf("invalid memory units")
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || n > math.MaxUint64/1024 {
			return Usage{}, fmt.Errorf("invalid memory value")
		}
		values[fields[0]] = n * 1024
	}
	total, okTotal := values["MemTotal:"]
	available, okAvailable := values["MemAvailable:"]
	if !okTotal || !okAvailable || total == 0 || available > total {
		return Usage{}, fmt.Errorf("missing or inconsistent memory counters")
	}
	return Usage{Used: total - available, Total: total}, nil
}

type netCounters struct{ rx, tx uint64 }

func parseNetwork(data []byte) (map[string]netCounters, error) {
	result := map[string]netCounters{}
	header := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "Inter-") && strings.Contains(line, "Receive") {
			header = true
		}
		name, counters, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		fields := strings.Fields(counters)
		if name == "" || len(fields) != 16 {
			return nil, fmt.Errorf("invalid network counters")
		}
		rx, errRX := strconv.ParseUint(fields[0], 10, 64)
		tx, errTX := strconv.ParseUint(fields[8], 10, 64)
		if errRX != nil || errTX != nil {
			return nil, fmt.Errorf("invalid network bytes")
		}
		if name != "lo" {
			result[name] = netCounters{rx, tx}
		}
	}
	if !header {
		return nil, fmt.Errorf("missing network header")
	}
	return result, nil
}

func parseUptime(data []byte) (int64, error) {
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return 0, fmt.Errorf("invalid uptime fields")
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n >= math.MaxInt64 {
		return 0, fmt.Errorf("invalid uptime seconds")
	}
	return int64(n), nil
}

type portKey struct {
	port  int
	proto string
}

// ss -H -lntu includes Netid, State, queues, Local Address:Port, Peer.
// IPv4, bracketed IPv6 and * listeners all reserve the numeric port.
func parseSockets(data []byte) (map[portKey]bool, error) {
	ports := map[portKey]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 6 || (f[0] != "tcp" && f[0] != "udp") {
			return nil, fmt.Errorf("invalid ss socket row")
		}
		i := strings.LastIndex(f[4], ":")
		if i < 0 {
			return nil, fmt.Errorf("invalid ss local address")
		}
		port, err := strconv.Atoi(f[4][i+1:])
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid ss local port")
		}
		ports[portKey{port, f[0]}] = true
	}
	return ports, nil
}
