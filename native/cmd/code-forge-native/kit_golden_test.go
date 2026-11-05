package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestKitGoldenProtocol(t *testing.T) {
	for _, c := range []struct{ input, want string }{
		{`{"op":"owned","entries":["?.go"],"files":["a.go","😀.go"]}`, "{\"kinds\":{},\"matches\":[true,false]}\n"},
		{`{"op":"changed","base":"HEAD"}`, "{\"error\":{\"code\":\"bad-ref\",\"message\":\"the block base must be a commit id\"}}\n"},
	} {
		var out bytes.Buffer
		if err := kitGolden(strings.NewReader(c.input), &out); err != nil || out.String() != c.want {
			t.Fatalf("JSON protocol got %q %v, want %q", out.String(), err, c.want)
		}
	}
}
