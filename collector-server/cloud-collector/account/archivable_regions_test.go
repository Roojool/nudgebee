package account

import (
	"reflect"
	"testing"
)

func TestArchivableRegions(t *testing.T) {
	cases := []struct {
		name    string
		queried []string
		skipped []string
		want    []string
	}{
		{"nothing skipped", []string{"us-east-1", "eu-west-1"}, nil, []string{"us-east-1", "eu-west-1"}},
		{"one region skipped", []string{"us-east-1", "me-south-1", "eu-west-1"}, []string{"me-south-1"}, []string{"us-east-1", "eu-west-1"}},
		{"all regions skipped yields empty, not nil-scope", []string{"me-south-1", "me-central-1"}, []string{"me-south-1", "me-central-1"}, []string{}},
		{"skipped region not in queried is a no-op", []string{"us-east-1"}, []string{"me-south-1"}, []string{"us-east-1"}},
		{"empty queried stays empty", []string{}, []string{"me-south-1"}, []string{}},
		{"nil queried stays nil", nil, []string{"me-south-1"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := archivableRegions(c.queried, c.skipped)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("archivableRegions(%v, %v) = %v, want %v", c.queried, c.skipped, got, c.want)
			}
		})
	}
}

func TestResolveArchiveRegions(t *testing.T) {
	cases := []struct {
		name    string
		crawled []string
		archive []string
		want    []string
	}{
		{
			// No allowlist in play: archival reconciles exactly what was crawled.
			name:    "no widening falls back to the crawl set",
			crawled: []string{"ap-southeast-2"},
			archive: nil,
			want:    []string{"ap-southeast-2"},
		},
		{
			name:    "empty widening falls back to the crawl set",
			crawled: []string{"ap-southeast-2"},
			archive: []string{},
			want:    []string{"ap-southeast-2"},
		},
		{
			// A region dropped from the allowlist is no longer crawled, so it must
			// stay in the archival scope — otherwise its rows are never observed
			// absent and stay is_active = true forever.
			name:    "de-listed region stays in archival scope",
			crawled: []string{"ap-southeast-2"},
			archive: []string{"ap-southeast-2", "us-east-1"},
			want:    []string{"ap-southeast-2", "us-east-1"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveArchiveRegions(c.crawled, c.archive)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("resolveArchiveRegions(%v, %v) = %v, want %v", c.crawled, c.archive, got, c.want)
			}
		})
	}
}
