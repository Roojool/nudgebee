package core_test

// End-to-end reproduction of the outage that motivated this code: RabbitMQ was
// recreated, no consumer re-declared webhook_process, and every delivery was
// discarded by the broker while the publish reported success — leaving rows at
// processing_status='pending' forever.
//
// Needs a real Postgres (schema in testdata/webhook_scenario_schema.sql) and a
// real broker; skipped unless both env vars are set:
//
//	docker run -d --name nb-mq-test -p 5673:5672 rabbitmq:3-management
//	docker run -d --name nb-pg-test -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=nbtest -p 5434:5432 postgres:16
//	docker exec -i nb-pg-test psql -U postgres -d nbtest < testdata/webhook_scenario_schema.sql
//	SERVER_ROLE=api NB_TEST_PG_DSN='postgres://postgres:postgres@localhost:5434/nbtest?sslmode=disable' \
//	  NB_TEST_RABBIT_HOST=localhost NB_TEST_RABBIT_PORT=5673 go test ./integrations/core/ -run TestScenario -v

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"nudgebee/services/common"
	"nudgebee/services/config"
	_ "nudgebee/services/integrations" // registers workflow_webhook
	"nudgebee/services/integrations/core"
	"nudgebee/services/integrations/core/webhook_queue"
	"nudgebee/services/internal/database"
	"nudgebee/services/security"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scenarioToken = "scenario-token-123"

func scenarioSetup(t *testing.T) (*sqlx.DB, *security.RequestContext) {
	t.Helper()

	dsn := os.Getenv("NB_TEST_PG_DSN")
	brokerHost := os.Getenv("NB_TEST_RABBIT_HOST")
	if dsn == "" || brokerHost == "" {
		t.Skip("NB_TEST_PG_DSN / NB_TEST_RABBIT_HOST not set; skipping live scenario")
	}
	brokerPort, err := strconv.Atoi(os.Getenv("NB_TEST_RABBIT_PORT"))
	require.NoError(t, err, "NB_TEST_RABBIT_PORT must be set alongside NB_TEST_RABBIT_HOST")

	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)

	database.RegisterDatabaseManagerHook(database.Metastore, func() (*database.DatabaseManager, error) {
		return &database.DatabaseManager{Db: db}, nil
	})
	manager, err := database.GetDatabaseManager(database.Metastore)
	require.NoError(t, err)
	manager.Db = db

	config.Config.RabbitMqHost = brokerHost
	config.Config.RabbitMqPort = brokerPort
	config.Config.RabbitMqUsername = "guest"
	config.Config.RabbitMqPassword = "guest"
	config.Config.RabbitMqWebhookProcessExchange = "webhook_process_exchange"
	config.Config.RabbitMqWebhookProcessQueue = "webhook_process"

	// Stand in for runbook-server so a fully-processed webhook reaches 'processed'.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(stub.Close)
	config.Config.WorkflowServerEndpoint = stub.URL

	sc := security.NewRequestContextForTenantAdmin("890cad87-c452-4aa7-b84a-742cee0454a1", nil, nil, nil)
	return db, sc
}

// TestScenarioWebhookSurvivesMissingQueue is the outage, end to end.
func TestScenarioWebhookSurvivesMissingQueue(t *testing.T) {
	db, sc := scenarioSetup(t)

	payload := `{"ref":"refs/heads/main","repository":{"name":"nudgebee-enterprise"}}`
	rowID, err := core.ValidateAndStoreWebhook(sc, "/api/webhooks/workflow?token="+scenarioToken, nil, payload)
	require.NoError(t, err)

	var status string
	require.NoError(t, db.Get(&status, `SELECT processing_status FROM event_incoming_webhooks WHERE id = $1`, rowID))
	assert.Equal(t, "pending", status, "delivery starts as pending")

	// No consumer is running, so no queue is bound — the state dev was in.
	pubErr := webhook_queue.PublishWebhookProcess(context.Background(), rowID)
	require.Error(t, pubErr, "publish must report the drop instead of returning nil")
	assert.ErrorIs(t, pubErr, common.ErrMqUnroutable)

	// What public_webhooks.go does with that error.
	require.NoError(t, core.ProcessStoredWebhook(sc, rowID))

	require.NoError(t, db.Get(&status, `SELECT processing_status FROM event_incoming_webhooks WHERE id = $1`, rowID))
	assert.Equal(t, "processed", status, "the fallback must have processed the webhook")
}

// TestScenarioReaperRecoversStuckRows covers deliveries that were already lost
// before this fix shipped — today's 17 rows.
func TestScenarioReaperRecoversStuckRows(t *testing.T) {
	db, sc := scenarioSetup(t)

	var rowID string
	require.NoError(t, db.Get(&rowID, `
		INSERT INTO event_incoming_webhooks(tenant_id, integration_id, integration_type, webhook_id, event_type, event_id, event_created_at, event_title, raw, processing_status, request_url)
		VALUES ('890cad87-c452-4aa7-b84a-742cee0454a1','0e75a0cc-a55c-4187-afc1-8acf5aead026','workflow_webhook','','','',$1,'','{"ref":"refs/heads/main"}','pending','/api/webhooks/workflow?token=`+scenarioToken+`')
		RETURNING id::text`, time.Now().UTC().Add(-45*time.Minute)))

	require.NoError(t, core.RecoverStuckWebhooks(sc))

	var status string
	require.NoError(t, db.Get(&status, `SELECT processing_status FROM event_incoming_webhooks WHERE id = $1`, rowID))
	assert.Equal(t, "processed", status, "the reaper must re-drive a stuck pending row")
}

// TestScenarioReaperSkipsStaleRows covers the 09-22 dev flood: months-old
// pending rows must not be replayed into today's analysis pipeline.
func TestScenarioReaperSkipsStaleRows(t *testing.T) {
	db, sc := scenarioSetup(t)

	var rowID string
	require.NoError(t, db.Get(&rowID, `
		INSERT INTO event_incoming_webhooks(tenant_id, integration_id, integration_type, webhook_id, event_type, event_id, event_created_at, event_title, raw, processing_status, request_url)
		VALUES ('890cad87-c452-4aa7-b84a-742cee0454a1','0e75a0cc-a55c-4187-afc1-8acf5aead026','workflow_webhook','','','',$1,'','{"ref":"refs/heads/main"}','pending','/api/webhooks/workflow?token=`+scenarioToken+`')
		RETURNING id::text`, time.Now().UTC().Add(-48*time.Hour)))

	require.NoError(t, core.RecoverStuckWebhooks(sc))

	var status string
	require.NoError(t, db.Get(&status, `SELECT processing_status FROM event_incoming_webhooks WHERE id = $1`, rowID))
	assert.Equal(t, "skipped", status, "a row older than the max age must not be re-driven")
}

// TestScenarioReaperResetsStalledProcessing covers a pod that died mid-flight.
func TestScenarioReaperResetsStalledProcessing(t *testing.T) {
	db, sc := scenarioSetup(t)

	var rowID string
	require.NoError(t, db.Get(&rowID, `
		INSERT INTO event_incoming_webhooks(tenant_id, integration_id, integration_type, webhook_id, event_type, event_id, event_created_at, event_title, raw, processing_status, request_url)
		VALUES ('890cad87-c452-4aa7-b84a-742cee0454a1','0e75a0cc-a55c-4187-afc1-8acf5aead026','workflow_webhook','','','',$1,'','{"ref":"refs/heads/main"}','processing','/api/webhooks/workflow?token=`+scenarioToken+`')
		RETURNING id::text`, time.Now().UTC().Add(-45*time.Minute)))

	require.NoError(t, core.RecoverStuckWebhooks(sc))

	var status string
	require.NoError(t, db.Get(&status, `SELECT processing_status FROM event_incoming_webhooks WHERE id = $1`, rowID))
	assert.Equal(t, "processed", status, "a row stranded in processing must be re-driven")
}
