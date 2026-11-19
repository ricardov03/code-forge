package main

import "testing"

// The smoke check matches <package>@<version> as a whole word only.
func TestB780SmokeMatchesWholeRelease(t *testing.T) {
	const want = "@codedology/code-forge@0.25.0"
	for text, ok := range map[string]bool{
		"npm i @codedology/code-forge@0.25.0\n":                              true,
		"pin @codedology/code-forge@0.25.0.":                                 true,
		"`@codedology/code-forge@0.25.0`":                                    true,
		"@codedology/code-forge@0.25.0":                                      true,
		"@codedology/code-forge@0.25.01":                                     false,
		"@codedology/code-forge@0.25.0.1":                                    false,
		"@codedology/code-forge@0.25.0-rc.1":                                 false,
		"x@codedology/code-forge@0.25.0":                                     false,
		"@codedology/code-forge@0.24.0":                                      false,
		"@codedology/code-forge@0.25.01 then @codedology/code-forge@0.25.0 ": true,
	} {
		if got := siteSmokeHas([]byte(text), want); got != ok {
			t.Errorf("siteSmokeHas(%q) = %v, want %v", text, got, ok)
		}
	}
}
