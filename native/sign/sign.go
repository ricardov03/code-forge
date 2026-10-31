// Package sign signs and verifies code-forge rows with HMAC-SHA256.
package sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/ricardov03/code-forge/native/canon"
)

// UnsignedFields returns the top-level fields excluded from a row's MAC.
func UnsignedFields() []string { return []string{"mac", "tokens_source", "cost_source"} }

// MACFor returns a lowercase hex MAC, leaving row untouched.
func MACFor(row map[string]any, key []byte) (string, error) {
	covered := clone(row)
	for _, field := range UnsignedFields() {
		delete(covered, field)
	}
	data, err := canon.Marshal(covered)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// SignRow copies row, stamps a missing or null ts, and replaces mac.
func SignRow(row map[string]any, key []byte) (map[string]any, error) {
	stamped := clone(row)
	if stamped["ts"] == nil {
		stamped["ts"] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	delete(stamped, "mac")
	mac, err := MACFor(stamped, key)
	if err != nil {
		return nil, err
	}
	stamped["mac"] = mac
	return stamped, nil
}

// Result matches Node's verifyRow result, including its rejection reason.
type Result struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// VerifyRow checks both the exact lowercase MAC encoding and its value.
func VerifyRow(row map[string]any, key []byte) (Result, error) {
	mac, ok := row["mac"].(string)
	if !ok || mac == "" {
		return Result{Reason: "unsigned"}, nil
	}
	if len(mac) != 64 {
		return Result{Reason: "mismatch"}, nil
	}
	for _, c := range mac {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return Result{Reason: "mismatch"}, nil
		}
	}
	expected, err := MACFor(row, key)
	if err != nil {
		return Result{}, err
	}
	if !hmac.Equal([]byte(mac), []byte(expected)) {
		return Result{Reason: "mismatch"}, nil
	}
	return Result{OK: true}, nil
}

func clone(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for key, value := range row {
		out[key] = value
	}
	return out
}
