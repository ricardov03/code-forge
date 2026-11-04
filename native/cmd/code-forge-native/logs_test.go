package main

import "testing"

func TestLogsRouteRegistered(t *testing.T) {
	if routes["logs"] == nil {
		t.Fatal("routes[\"logs\"] is nil; want a Go handler")
	}
}
