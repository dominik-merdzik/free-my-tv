package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
)

// NUT reads the UPS from upsd. Its text protocol needs no login for LIST VAR, so no secret is involved. upsd runs
// on pve: while pve is down the UPS section is null, which is expected.
type NUT struct {
	Addr string // host:port
	UPS  string // UPS name in upsd
}

func (n *NUT) Fetch(ctx context.Context) (*UPS, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", n.Addr)
	if err != nil {
		return nil, errors.New(shortErr(err))
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if _, err := fmt.Fprintf(conn, "LIST VAR %s\nLOGOUT\n", n.UPS); err != nil {
		return nil, err
	}
	vars, err := parseVars(bufio.NewScanner(conn), n.UPS)
	if err != nil {
		return nil, err
	}
	return upsFrom(vars)
}

// parseVars reads the reply to LIST VAR: `VAR <ups> <name> "<value>"` lines between BEGIN and END.
func parseVars(sc *bufio.Scanner, ups string) (map[string]string, error) {
	vars := map[string]string{}
	prefix := "VAR " + ups + " "
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "ERR "):
			return nil, errors.New("upsd: " + strings.TrimPrefix(line, "ERR "))
		case strings.HasPrefix(line, prefix):
			name, value, ok := strings.Cut(strings.TrimPrefix(line, prefix), " ")
			if ok {
				vars[name] = strings.Trim(value, `"`)
			}
		case strings.HasPrefix(line, "END LIST VAR"):
			return vars, nil
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("upsd closed the connection before the end of the list")
}

func upsFrom(vars map[string]string) (*UPS, error) {
	status, ok := vars["ups.status"]
	if !ok {
		return nil, errors.New("upsd reported no ups.status")
	}
	u := &UPS{Status: status, ChargePct: number(vars["battery.charge"]), LoadPct: number(vars["ups.load"])}
	if secs := number(vars["battery.runtime"]); secs != nil {
		u.RuntimeMin = intPtr(int(math.Round(float64(*secs) / 60))) // upsd reports seconds
	}
	return u, nil
}

// number parses a NUT value ("100", "10.0"); nil when absent or not a number.
func number(v string) *int {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) {
		return nil
	}
	return intPtr(int(math.Round(f)))
}
