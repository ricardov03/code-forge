package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

func TestBlockCloseNativeUsage(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	for _, args := range [][]string{{"close"}, {"close", "B1"}, {"close", "B1", "--run"}, {"close", "B1", "--run", "r-test", "--json=true"}} {
		if code := runBlock(args); code != 2 {
			t.Errorf("block %v: usage exit = %d, want 2 without Node", args, code)
		}
	}
}

func TestConfirmedBlockCloseNeverHandsOverToNode(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	for _, step := range []*landStep{nil, {}} {
		var reason handoverReason
		// B380: an exact --help is native usage; a --help=value spelling still hands over.
		code := blockCloseHumanStep([]string{"--help=yes"}, step, &reason, true)
		if code != landHandover || reason != handoverFlagReason("close", "--help", hCloseUnsupportedFlag) {
			t.Fatalf("step=%v: code=%d reason=%v; want handover sentinel without invoking Node", step, code, reason)
		}
	}
}

func TestB380BlockCloseHelpIsNative(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	for _, args := range [][]string{{"--help"}, {"-h"}, {"B1", "--run", "r-x", "--auto-claim", "--help"}} {
		reason := handoverReason(-1)
		if code := blockCloseHumanStep(args, nil, &reason, false); code != 0 || reason != -1 {
			t.Errorf("%v: code=%d reason=%v; want native usage exit 0 with no hand-over", args, code, reason)
		}
	}
}

func TestCloseDoneRowsDaemonAndFallback(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "cf-close-unit-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NATIVE", "1")
	run := "r-close-unit"
	dir := filepath.Join(home, ".code-forge/runs", run)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ledgerDir := filepath.Join(home, ".code-forge/ledger")
	if err := os.MkdirAll(ledgerDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ledgerDir, "fixture.jsonl"), []byte("{\"event\":\"ledger\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	record := ledger.Object{{Key: "project", Value: "fixture"}}
	for _, answering := range []bool{true, false} {
		listener, err := net.Listen("unix", filepath.Join(dir, "native.sock"))
		if err != nil {
			t.Fatal(err)
		}
		requests := make(chan map[string]any, 2)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			scan := bufio.NewScanner(conn)
			for scan.Scan() {
				var request map[string]any
				if json.Unmarshal(scan.Bytes(), &request) != nil {
					return
				}
				requests <- request
				if request["method"] == "ping" {
					json.NewEncoder(conn).Encode(map[string]any{"ok": true, "run": run, "version": version})
				} else if answering {
					json.NewEncoder(conn).Encode(map[string]any{"rows": []any{map[string]any{"event": "daemon"}}})
					return
				} else {
					return
				}
			}
		}()
		rows, err := closeDoneRows(home, run, "B1", record)
		listener.Close()
		<-finished
		if err != nil {
			t.Fatal(err)
		}
		want := "ledger"
		if answering {
			want = "daemon"
		}
		if len(rows) != 1 || rows[0].(map[string]any)["event"] != want {
			t.Fatalf("answering=%v: rows=%v, want %s", answering, rows, want)
		}
		<-requests
		request := <-requests
		if request["method"] != "rows" || request["block"] != "B1" {
			t.Fatalf("wrong request: %v", request)
		}
	}
	t.Setenv("CODE_FORGE_NATIVE", "0")
	rows, err := closeDoneRows(home, run, "B1", record)
	if err != nil || len(rows) != 1 || rows[0].(map[string]any)["event"] != "ledger" {
		t.Fatalf("disabled: %v %v", rows, err)
	}
}
