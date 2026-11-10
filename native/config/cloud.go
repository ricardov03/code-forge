package config

import "fmt"

// Defaults for coder.cloud (B342). The schema carries the same values.
const (
	DefaultCloudTimeoutS = 3600.0
	DefaultCloudPollS    = 30.0
)

// CloudCoder is coder.cloud: two argv templates and the poll limits. The
// templates take {block}, {base}, {brief}, {task} and {patch_out}.
type CloudCoder struct {
	Submit   []string
	Fetch    []string
	TimeoutS float64
	PollS    float64
}

// CloudCoderFrom reads coder.cloud from a loaded config. It returns nil and no
// error when the key is absent, and an error naming the key when a value has
// the wrong shape.
func CloudCoderFrom(cfg map[string]any) (*CloudCoder, error) {
	coder, _ := cfg["coder"].(map[string]any)
	raw, present := coder["cloud"]
	if !present {
		return nil, nil
	}
	cloud, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("coder.cloud must be a mapping")
	}
	for key := range cloud {
		switch key {
		case "submit", "fetch", "timeout_s", "poll_s":
		default:
			return nil, fmt.Errorf("coder.cloud.%s is not a known key", safeSegment(key))
		}
	}
	out := &CloudCoder{TimeoutS: DefaultCloudTimeoutS, PollS: DefaultCloudPollS}
	for _, field := range []struct {
		key  string
		dest *[]string
	}{{"submit", &out.Submit}, {"fetch", &out.Fetch}} {
		list, ok := cloud[field.key].([]any)
		if !ok || len(list) == 0 {
			return nil, fmt.Errorf("coder.cloud.%s must be a non-empty argv list", field.key)
		}
		for _, item := range list {
			text, ok := item.(string)
			if !ok || text == "" {
				return nil, fmt.Errorf("coder.cloud.%s must hold non-empty strings", field.key)
			}
			*field.dest = append(*field.dest, text)
		}
	}
	for _, field := range []struct {
		key  string
		dest *float64
	}{{"timeout_s", &out.TimeoutS}, {"poll_s", &out.PollS}} {
		value, present := cloud[field.key]
		if !present {
			continue
		}
		n, ok := value.(float64)
		if !ok || n <= 0 {
			return nil, fmt.Errorf("coder.cloud.%s must be a number > 0", field.key)
		}
		*field.dest = n
	}
	return out, nil
}
