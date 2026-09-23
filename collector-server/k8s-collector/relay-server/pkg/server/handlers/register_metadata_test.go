package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"nudgebee/relay-server/pkg/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// metadataFakeStore tracks how many times UpdateDatasourceMetadata was
// called and lets a test script which dsIDs "aren't found" (integrations
// row not yet created) on each call, to exercise handleDatasourceMetadata's
// retry loop deterministically.
type metadataFakeStore struct {
	fakeStore
	calls      int
	notFoundOn []map[string]bool // notFoundOn[i] = dsIDs still not found on the i-th call
}

func (f *metadataFakeStore) UpdateDatasourceMetadata(ctx context.Context, accountID, agentType string, metadata map[string]map[string]any) ([]string, error) {
	i := f.calls
	f.calls++
	if i >= len(f.notFoundOn) {
		return nil, nil
	}
	var notFound []string
	for dsID := range metadata {
		if f.notFoundOn[i][dsID] {
			notFound = append(notFound, dsID)
		}
	}
	return notFound, nil
}

func withFastMetadataRetries(t *testing.T) {
	t.Helper()
	orig := metadataRetryDelays
	metadataRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { metadataRetryDelays = orig })
}

// The common case from #38718: metadata for one datasource races its
// datasource_inventory INSERT, then succeeds on the next attempt once the
// integrations row exists. handleDatasourceMetadata must retry only the
// still-missing keys, not the whole batch, and must stop as soon as
// nothing is left unmatched.
func TestHandleDatasourceMetadata_RetriesRacedKeyThenSucceeds(t *testing.T) {
	withFastMetadataRetries(t)
	store := &metadataFakeStore{
		notFoundOn: []map[string]bool{
			{"local:vm-agent": true}, // 1st call: raced, not found yet
			{},                       // 2nd call: found, update lands
		},
	}
	raw, err := json.Marshal(map[string]any{
		"action":   "datasource_metadata",
		"metadata": map[string]map[string]any{"local:vm-agent": {"actions": []string{"discovery_sweep"}}},
	})
	require.NoError(t, err)

	handleDatasourceMetadata(context.Background(), store, "acct-1", "k8s", raw, discardLogger())

	assert.Equal(t, 2, store.calls, "must retry exactly once after the first miss, then stop")
}

// If every retry is exhausted and the key still isn't found, the handler
// must give up (not retry forever) and not panic — it's a best-effort
// self-heal, not a guarantee (see /challenge's counterargument 3: this
// narrows the race, it does not close it).
func TestHandleDatasourceMetadata_GivesUpAfterExhaustingRetries(t *testing.T) {
	withFastMetadataRetries(t)
	alwaysNotFound := map[string]bool{"local:vm-agent": true}
	store := &metadataFakeStore{
		notFoundOn: []map[string]bool{alwaysNotFound, alwaysNotFound, alwaysNotFound, alwaysNotFound, alwaysNotFound},
	}
	raw, err := json.Marshal(map[string]any{
		"action":   "datasource_metadata",
		"metadata": map[string]map[string]any{"local:vm-agent": {"actions": []string{"discovery_sweep"}}},
	})
	require.NoError(t, err)

	handleDatasourceMetadata(context.Background(), store, "acct-1", "k8s", raw, discardLogger())

	assert.Equal(t, 1+len(metadataRetryDelays), store.calls, "must stop after the bounded retry budget, not loop forever")
}

// A metadata message with nothing raced (the common, non-racing path) must
// not retry at all.
func TestHandleDatasourceMetadata_NoRetryWhenNothingRaced(t *testing.T) {
	withFastMetadataRetries(t)
	store := &metadataFakeStore{}
	raw, err := json.Marshal(map[string]any{
		"action":   "datasource_metadata",
		"metadata": map[string]map[string]any{"local:vm-agent": {"actions": []string{"discovery_sweep"}}},
	})
	require.NoError(t, err)

	handleDatasourceMetadata(context.Background(), store, "acct-1", "k8s", raw, discardLogger())

	assert.Equal(t, 1, store.calls)
}

var _ db.AgentStore = (*metadataFakeStore)(nil)
