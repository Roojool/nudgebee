package storage

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"

	"nudgebee/runbook/internal/model"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Real-Postgres coverage for the free-text search behind the "pre-built
// templates" picker (#38448): the `name` filter matches the template name, its
// description, or any browse label, and typed LIKE wildcards match literally.

func templateSearchTestDB(t *testing.T) *sqlx.DB {
	t.Helper()

	db, err := sqlx.Connect("postgres", requireTestDSN(t))
	require.NoError(t, err, "connect to TEST_POSTGRES_DSN")

	schema := fmt.Sprintf("template_search_test_%d", os.Getpid())
	_, err = db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE; CREATE SCHEMA %s; SET search_path TO %s`,
		schema, schema, schema))
	require.NoError(t, err)

	// Only the columns ListGlobal reads.
	_, err = db.Exec(`
		CREATE TABLE users (id uuid PRIMARY KEY, display_name text);
		CREATE TABLE workflow_templates (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id uuid,
			account_id uuid,
			name varchar(255) NOT NULL,
			description text,
			category varchar(100),
			icon varchar(255),
			definition jsonb NOT NULL DEFAULT '{}'::jsonb,
			template_variables jsonb DEFAULT '[]'::jsonb,
			tags jsonb DEFAULT '{}'::jsonb,
			is_system boolean DEFAULT true,
			status varchar(50) NOT NULL DEFAULT 'ACTIVE',
			created_by uuid,
			updated_by uuid,
			created_at timestamp DEFAULT now(),
			updated_at timestamp DEFAULT now()
		)`)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema))
		_ = db.Close()
	})
	return db
}

func TestListGlobal_SearchMatchesNameDescriptionAndLabels(t *testing.T) {
	db := templateSearchTestDB(t)
	dao := &WorkflowTemplateDao{db: db}

	_, err := db.Exec(`
		INSERT INTO workflow_templates (name, description, category, tags) VALUES
			('EC2 Host Diagnostics', 'Collect host metrics from an instance', 'cloud-cost', '{"labels": ["diagnostic"]}'),
			('Diagnose Database on EC2', 'Checks PostgreSQL health', 'monitoring', '{"labels": ["read-only"]}'),
			('Bounce Deployment', 'Rollout pods again', 'kubernetes', '{"labels": ["restart"]}'),
			('Purge queue_backlog', NULL, 'automation', '{"labels": "not-an-array"}'),
			('Purge queue backlog', 'no underscore here', 'automation', NULL)`)
	require.NoError(t, err)

	cases := []struct {
		name     string
		request  model.ListWorkflowTemplateRequest
		expected []string
	}{
		{"title hit, case-insensitive", model.ListWorkflowTemplateRequest{Name: "ec2"}, []string{"Diagnose Database on EC2", "EC2 Host Diagnostics"}},
		{"description-only hit", model.ListWorkflowTemplateRequest{Name: "postgres"}, []string{"Diagnose Database on EC2"}},
		{"label hit", model.ListWorkflowTemplateRequest{Name: "RESTART"}, []string{"Bounce Deployment"}},
		{"surrounding whitespace ignored", model.ListWorkflowTemplateRequest{Name: "  read-only "}, []string{"Diagnose Database on EC2"}},
		{"search ANDs with category", model.ListWorkflowTemplateRequest{Name: "ec2", Category: "monitoring"}, []string{"Diagnose Database on EC2"}},
		{"underscore matches literally", model.ListWorkflowTemplateRequest{Name: "queue_"}, []string{"Purge queue_backlog"}},
		{"percent matches literally", model.ListWorkflowTemplateRequest{Name: "%"}, nil},
		{"no match", model.ListWorkflowTemplateRequest{Name: "zzz"}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			templates, total, err := dao.ListGlobal(context.Background(), tc.request)
			require.NoError(t, err)

			var got []string
			for _, tmpl := range templates {
				got = append(got, tmpl.Name)
			}
			sort.Strings(got)
			require.Equal(t, tc.expected, got)
			require.Equal(t, len(tc.expected), total)
		})
	}
}
