// Package jev ports the Jev (System 1) HTTP transport of
// src/decide/jev-client.mjs (askJev) and the `risk` entry of
// src/decide/questions.mjs (buildQuestionPayload). One call is one POST
// `{state, model, questions}` with `Authorization: Bearer <key>`; 429, 529, a
// network failure and a per-attempt timeout back off 0.5 s, 2 s, 8 s (four
// attempts); 401 and 422 never retry; any other non-2xx fails at once. The
// key goes only into the header: no result field, error or log line holds it.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// URL is jev-client.mjs JEV_URL; Model is JEV_MODEL.
const (
	URL   = "https://api.typesafe.ai/v1/systemone"
	Model = "jev-latest"
)

// RetryDelaysMs is RETRY_DELAYS_MS.
var RetryDelaysMs = []float64{500, 2000, 8000}

// DefaultTimeout is DEFAULT_JEV_TIMEOUT_MS.
const DefaultTimeout = 30 * time.Second

// Opts are askJev's options. State and Questions are written with Node's
// JSON.stringify (ledger.Stringify keeps their key order).
type Opts struct {
	State     any
	Questions any
	Key       string
	URL       string           // "" = URL
	Model     string           // "" = Model
	Timeout   time.Duration    // 0 = DefaultTimeout (per attempt)
	Sleep     func(ms float64) // nil = time.Sleep
	Client    *http.Client     // nil = a client that, like Node's fetch, ignores proxy variables
}

// Result is JevSuccess or JevFailure. Status 0 is Node's null.
type Result struct {
	OK        bool
	Answers   any
	Usage     any
	Kind      string
	Status    int
	RequestID *string
	Attempts  int
	Raw       []byte // B550: the 2xx body, for a caller that needs JSON.parse's key order
}

var (
	terminalKinds  = map[int]string{401: "unauthorized", 422: "invalid_request"}
	retryableKinds = map[int]string{429: "rate_limited", 529: "unavailable"}
)

func defaultClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil // undici's fetch reads no HTTP(S)_PROXY variable
	return &http.Client{Transport: t}
}

// Ask is askJev. It never returns an error for a normal failure; the error
// is only for options Node's askJev refuses with a TypeError before any call.
func Ask(o Opts) (Result, error) {
	if o.Key == "" {
		return Result{}, errors.New("askJev: key must be a non-empty string")
	}
	if o.URL == "" {
		o.URL = URL
	}
	if o.Model == "" {
		o.Model = Model
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Sleep == nil {
		o.Sleep = func(ms float64) { time.Sleep(time.Duration(ms * float64(time.Millisecond))) }
	}
	if o.Client == nil {
		o.Client = defaultClient()
	}
	body, err := ledger.Stringify(ledger.Object{{Key: "state", Value: o.State}, {Key: "model", Value: o.Model}, {Key: "questions", Value: o.Questions}}, false)
	if err != nil {
		return Result{}, err
	}
	max := len(RetryDelaysMs) + 1
	for attempt := 1; attempt <= max; attempt++ {
		res, retry := once(o, body, attempt)
		if !retry {
			return res, nil
		}
		if attempt < max {
			o.Sleep(RetryDelaysMs[attempt-1])
			continue
		}
		return res, nil
	}
	return Result{Kind: "network", Attempts: max}, nil // unreachable, as in Node
}

// once makes one attempt; retry tells the caller to back off and try again.
func once(o Opts, body []byte, attempt int) (Result, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.URL, bytes.NewReader(body))
	if err != nil {
		return Result{Kind: "network", Attempts: attempt}, true
	}
	req.Header.Set("Authorization", "Bearer "+o.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.Client.Do(req)
	if err != nil {
		kind := "network"
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			kind = "timeout"
		}
		return Result{Kind: kind, Attempts: attempt}, true
	}
	defer resp.Body.Close()
	status := resp.StatusCode
	if status >= 200 && status <= 299 {
		data, err := io.ReadAll(resp.Body)
		var parsed any
		if err == nil {
			err = json.Unmarshal(data, &parsed)
		}
		if err != nil {
			return Result{Kind: "invalid_response", Status: status, Attempts: attempt}, false
		}
		obj, isObj := parsed.(map[string]any) // an array has no `answers`
		if !isObj {
			return Result{Kind: "invalid_response", Status: status, Attempts: attempt}, false
		}
		switch obj["answers"].(type) {
		case map[string]any, []any:
		default:
			return Result{Kind: "invalid_response", Status: status, Attempts: attempt}, false
		}
		return Result{OK: true, Answers: obj["answers"], Usage: obj["usage"], Attempts: attempt, Raw: data}, false
	}
	if kind, ok := terminalKinds[status]; ok {
		return Result{Kind: kind, Status: status, RequestID: requestID(resp), Attempts: attempt}, false
	}
	if kind, ok := retryableKinds[status]; ok {
		return Result{Kind: kind, Status: status, Attempts: attempt}, true
	}
	return Result{Kind: "unexpected_status", Status: status, Attempts: attempt}, false
}

// requestID is requestIdOf: the x-request-id header, else the JSON body's
// request_id / requestId string; never anything else from the body.
func requestID(resp *http.Response) *string {
	if h := resp.Header.Get("x-request-id"); h != "" {
		return &h
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var body map[string]any
	if json.Unmarshal(data, &body) != nil {
		return nil
	}
	id := body["request_id"]
	if id == nil {
		id = body["requestId"]
	}
	if s, ok := id.(string); ok && s != "" {
		return &s
	}
	return nil
}
