package observability

import "fmt"

// completeUtilisationResults gives every requested key exactly one entry, so a caller can
// tell "not supported" from "no data":
//   - an existing entry is kept as is (providers write their own, more specific Notes);
//   - a rendered key with no entry gets an empty entry carrying its query, and no Note;
//   - any other key gets an empty entry with a Note saying why.
//
// rendered is nil for the arms that fetch for themselves; their sources emit an entry for
// every key they render, so an absent key there was never rendered.
func completeUtilisationResults(results []QueryResult, requested []string, rendered map[string]string, provider string, scope esUtilScope) []QueryResult {
	present := make(map[string]bool, len(results))
	for _, r := range results {
		if r.QueryKey != "" {
			present[r.QueryKey] = true
		}
	}

	for _, key := range requested {
		if key == "" || present[key] {
			continue
		}
		present[key] = true // a key requested twice still gets one entry

		if query := rendered[key]; query != "" {
			results = append(results, QueryResult{QueryKey: key, Query: query, Payload: []Result{}})
			continue
		}
		var note string
		if provider == "" {
			note = "no metrics provider resolved for this account"
		} else {
			note = fmt.Sprintf("%s does not answer utilisation key %q at %s scope", provider, key, scope)
		}
		results = append(results, QueryResult{QueryKey: key, Payload: []Result{}, Note: note})
	}
	return results
}
