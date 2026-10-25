package main

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

const listVar = `BEGIN LIST VAR cp1500
VAR cp1500 battery.charge "100"
VAR cp1500 battery.runtime "3525"
VAR cp1500 ups.load "10"
VAR cp1500 ups.status "OL"
END LIST VAR cp1500
`

func TestParseVarsAndUPS(t *testing.T) {
	vars, err := parseVars(bufio.NewScanner(strings.NewReader(listVar)), "cp1500")
	if err != nil {
		t.Fatal(err)
	}
	u, err := upsFrom(vars)
	if err != nil {
		t.Fatal(err)
	}
	if u.Status != "OL" || *u.ChargePct != 100 || *u.RuntimeMin != 59 || *u.LoadPct != 10 {
		t.Errorf("ups = %s %d %d %d (runtime is seconds in upsd: 3525 s ≈ 59 min)", u.Status, *u.ChargePct, *u.RuntimeMin, *u.LoadPct)
	}
}

func TestUnknownUPSIsAnError(t *testing.T) {
	_, err := parseVars(bufio.NewScanner(strings.NewReader("ERR UNKNOWN-UPS\n")), "nope")
	if err == nil || !strings.Contains(err.Error(), "UNKNOWN-UPS") {
		t.Errorf("err = %v", err)
	}
}

// A fake upsd over real TCP, to check the request and the round trip.
func TestNUTFetch(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- line
		c.Write([]byte(listVar))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	u, err := (&NUT{Addr: l.Addr().String(), UPS: "cp1500"}).Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if req := <-got; req != "LIST VAR cp1500\n" {
		t.Errorf("request = %q", req)
	}
	if u.Status != "OL" {
		t.Errorf("status = %q", u.Status)
	}
}

func TestShortErr(t *testing.T) {
	_, err := net.Dial("tcp", "127.0.0.1:1")
	if got := shortErr(err); got != "connection refused" {
		t.Errorf("shortErr = %q", got)
	}
}
