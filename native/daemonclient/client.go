// Package daemonclient makes read-only requests to an already running daemon.
package daemonclient

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Version must be set to the CLI's build version before making requests.
// It is negotiated on every connection, just like Node's package version.
var Version = "dev"

const maxAnswer = 8 * 1024 * 1024

var runPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Enabled matches daemonEnabled: only the exact environment value "0" or a
// literal false at native.daemon disables the daemon.
func Enabled(config map[string]any) bool {
	native, _ := config["native"].(map[string]any)
	return os.Getenv("CODE_FORGE_NATIVE") != "0" && native["daemon"] != false
}

// Request uses one Unix connection, negotiates ping, then returns the answer
// without transport stats. Every failure returns a nil answer and an error;
// callers can hand over without starting a daemon or leaving any resources.
// A nonpositive timeout uses Node's 6500ms default (max_ms + 2500 for wait-block).
func Request(run string, req any, timeout time.Duration) (map[string]any, error) {
	if !runPattern.MatchString(run) || !Enabled(nil) {
		return nil, errors.New("daemon unavailable")
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var request map[string]any
	if err := json.Unmarshal(data, &request); err != nil || request == nil {
		return nil, errors.New("invalid daemon request")
	}
	if timeout <= 0 {
		timeout = 6500 * time.Millisecond
		if request["method"] == "wait-block" {
			ms, _ := request["max_ms"].(float64)
			timeout = time.Duration(ms)*time.Millisecond + 2500*time.Millisecond
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	connectTimeout := min(2*time.Second, timeout)
	conn, err := net.DialTimeout("unix", filepath.Join(home, ".code-forge", "runs", run, "native.sock"), connectTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	exchange := func(payload []byte, limit time.Duration) (map[string]any, error) {
		if err := conn.SetDeadline(time.Now().Add(limit)); err != nil {
			return nil, err
		}
		if _, err := conn.Write(append(payload, '\n')); err != nil {
			return nil, err
		}
		var line []byte
		for {
			part, err := reader.ReadSlice('\n')
			if len(line)+len(part) > maxAnswer {
				return nil, errors.New("daemon answer exceeds 8 MiB")
			}
			line = append(line, part...)
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil {
				return nil, err
			}
			break
		}
		var answer map[string]any
		if err := json.Unmarshal(line, &answer); err != nil {
			return nil, err
		}
		if answer == nil || answer["ok"] == false {
			return nil, errors.New("invalid daemon answer")
		}
		return answer, nil
	}
	ping, err := exchange([]byte(`{"method":"ping"}`), connectTimeout)
	if err != nil {
		return nil, err
	}
	if ping["ok"] != true || ping["version"] != Version || ping["run"] != run {
		return nil, errors.New("incompatible daemon")
	}
	if request["method"] == "ping" {
		return ping, nil
	}
	answer, err := exchange(data, timeout)
	if err != nil {
		return nil, err
	}
	delete(answer, "stats")
	if !validAnswer(run, request, answer) {
		return nil, fmt.Errorf("invalid daemon %v answer", request["method"])
	}
	return answer, nil
}

// Timeout reports a request that failed only because a fixed transport
// deadline passed (connect, ping or answer): the daemon may be alive but
// slow, as on a loaded machine. A refused connect, a closed connection or an
// invalid answer is not a timeout.
func Timeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func validAnswer(run string, req, answer map[string]any) bool {
	switch req["method"] {
	case "status":
		runs, ok := answer["runs"].([]any)
		if !ok || len(runs) != 1 {
			return false
		}
		summary, _ := runs[0].(map[string]any)
		_, blocks := summary["blocks"].([]any)
		return summary["run_id"] == run && blocks
	case "rows":
		rows, ok := answer["rows"].([]any)
		if !ok {
			return false
		}
		for _, row := range rows {
			if _, ok := row.(map[string]any); !ok {
				return false
			}
		}
	case "wait-block":
		files, ok := answer["files"].([]any)
		_, done := answer["done"].(bool)
		if !ok || !done || answer["block"] != req["block"] {
			return false
		}
		for _, value := range files {
			file, _ := value.(map[string]any)
			_, name := file["file"].(string)
			_, status := file["status"].(string)
			if !name || !status {
				return false
			}
		}
	}
	return true
}
