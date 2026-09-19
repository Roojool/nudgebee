package sources

import (
	"fmt"

	"nudgebee/services/knowledge_graph/core"
)

// CheckPatchSpecs validates a cloud's patch specs. Each cloud package calls it
// from a test, so a mistake in a resource file fails that cloud's tests:
//
//   - every name a spec covers must be a registered specific type, a NodeType
//     name, or a kind the cloud's policy derives itself (derivedKinds); a typo
//     would silently leave the real type on the default spec;
//   - no signature may include a forbidden edge kind: an edge a global pass or
//     another resource's builder emits out of this type, which a patch would
//     retire and never re-create.
func CheckPatchSpecs(specs []PatchSpec, forbidden []core.EdgeKind, derivedKinds ...string) []error {
	derived := make(map[string]bool, len(derivedKinds))
	for _, k := range derivedKinds {
		derived[k] = true
	}
	var errs []error
	for _, spec := range specs {
		for _, name := range spec.SpecificTypes {
			if _, known := core.LookupSpecificTypeSchema(name); known {
				continue
			}
			if core.NodeType(name).IsKnown() || derived[name] {
				continue
			}
			errs = append(errs, fmt.Errorf("patch spec names unknown specific type %q", name))
		}
		for _, kind := range spec.Signature.Kinds {
			for _, bad := range forbidden {
				if kind.Relationship == bad.Relationship && (bad.ConnectionType == "" || kind.ConnectionType == bad.ConnectionType || kind.ConnectionType == "") {
					errs = append(errs, fmt.Errorf("patch spec %v: signature includes %s/%s, which another pass emits", spec.SpecificTypes, kind.Relationship, kind.ConnectionType))
				}
			}
		}
	}
	return errs
}
