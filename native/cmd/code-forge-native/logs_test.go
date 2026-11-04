package main

import "testing"

func TestLogsRouteRegistered(t *testing.T) {
	if routes["logs"] == nil {
		t.Fatal("logs route count = 0; want one Go handler")
	}
}
