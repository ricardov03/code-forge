package main

import "github.com/ricardov03/code-forge/native/config"

// gatesImportGraphStop is gates.import_graph.stop of the project that holds
// cwd (B427): the hub files where the import-graph closure stops. A missing
// or unreadable config, or a value that is not a list of strings, stops
// nowhere.
func gatesImportGraphStop(cwd string) []string {
	loaded, err := config.Load(cwd)
	if err != nil {
		return nil
	}
	g, _ := loaded["gates"].(map[string]any)
	ig, _ := g["import_graph"].(map[string]any)
	list, _ := ig["stop"].([]any)
	out := []string{}
	for _, v := range list {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
