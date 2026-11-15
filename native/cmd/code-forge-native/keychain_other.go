//go:build !linux

package main

import "errors"

// ssDial off Linux: there is no Secret Service to open (kcOpen only asks
// on Linux).
func ssDial() (ssAPI, error) {
	return nil, errors.New("Secret Service is Linux only")
}
