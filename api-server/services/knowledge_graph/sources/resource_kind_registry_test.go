package sources

import "testing"

// testKind is a minimal kind payload: the registry only needs KindKey().
type testKind struct {
	service string
	types   []string
	label   string
}

func (k testKind) KindKey() KindKey { return KindKey{Service: k.service, Types: k.types} }

func TestResourceKindRegistryLookup(t *testing.T) {
	r := NewResourceKindRegistry[testKind]()
	r.Register(testKind{service: "AWSQueueService", types: []string{"queue"}, label: "SQSQueue"})
	r.Register(testKind{service: "AWSQueueService", label: "SQSQueueFallback"})
	r.Register(testKind{service: "AmazonEC2", types: []string{"compute-instance", "Managed-Instance"}, label: "EC2Instance"})

	if kind, ok := r.LookupType("queue", "AWSQueueService"); !ok || kind.label != "SQSQueue" {
		t.Fatalf("exact lookup = %+v, %v; want SQSQueue", kind, ok)
	}
	// Row types are matched lower-cased, in both directions.
	if kind, ok := r.LookupType("QUEUE", "AWSQueueService"); !ok || kind.label != "SQSQueue" {
		t.Fatalf("upper-case type lookup = %+v, %v; want SQSQueue", kind, ok)
	}
	if kind, ok := r.LookupType("managed-instance", "AmazonEC2"); !ok || kind.label != "EC2Instance" {
		t.Fatalf("mixed-case registration lookup = %+v, %v; want EC2Instance", kind, ok)
	}
	// A service's other types miss the exact lookup rather than falling back:
	// the resolution order is the caller's.
	if _, ok := r.LookupType("dead-letter-queue", "AWSQueueService"); ok {
		t.Fatal("unregistered type resolved through LookupType")
	}
	if kind, ok := r.LookupService("AWSQueueService"); !ok || kind.label != "SQSQueueFallback" {
		t.Fatalf("service fallback = %+v, %v; want SQSQueueFallback", kind, ok)
	}
	if _, ok := r.LookupService("AmazonEC2"); ok {
		t.Fatal("service with no fallback kind resolved through LookupService")
	}
	// Service names are matched as written: the collector's casing is data.
	if _, ok := r.LookupType("queue", "awsqueueservice"); ok {
		t.Fatal("service name matched case-insensitively")
	}
}

func TestResourceKindRegistryKindsOrder(t *testing.T) {
	r := NewResourceKindRegistry[testKind]()
	r.Register(testKind{service: "AmazonS3", types: []string{"storage"}, label: "S3Bucket"})
	r.Register(testKind{service: "AmazonEC2", types: []string{"storage"}, label: "EBSVolume"})
	r.Register(testKind{service: "AmazonEC2", label: "EC2Fallback"})
	r.Register(testKind{service: "AmazonEC2", types: []string{"compute-instance"}, label: "EC2Instance"})

	want := []string{"EC2Fallback", "EC2Instance", "EBSVolume", "S3Bucket"}
	kinds := r.Kinds()
	if len(kinds) != len(want) {
		t.Fatalf("Kinds() returned %d kinds; want %d", len(kinds), len(want))
	}
	for i, label := range want {
		if kinds[i].label != label {
			t.Errorf("Kinds()[%d] = %q; want %q", i, kinds[i].label, label)
		}
	}
}

func TestResourceKindRegistryPanics(t *testing.T) {
	tests := []struct {
		name  string
		kinds []testKind
	}{
		{"no type and no service", []testKind{{label: "nowhere"}}},
		{"empty type", []testKind{{service: "AmazonEC2", types: []string{""}}}},
		{"duplicate exact key", []testKind{
			{service: "AmazonEC2", types: []string{"storage"}},
			{service: "AmazonEC2", types: []string{"STORAGE"}},
		}},
		{"duplicate service fallback", []testKind{
			{service: "AmazonEC2"},
			{service: "AmazonEC2"},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("Register did not panic")
				}
			}()
			r := NewResourceKindRegistry[testKind]()
			for _, kind := range tc.kinds {
				r.Register(kind)
			}
		})
	}
}
