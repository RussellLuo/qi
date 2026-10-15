package prompt

import "testing"

func TestSnapshotClone(t *testing.T) {
	snapshot := Snapshot{Revision: 7, SectionIDs: []string{"section"}, Text: "prompt", Diagnostics: []Diagnostic{{SectionID: "section", Bytes: 6}}}
	cloned := snapshot.Clone()
	cloned.SectionIDs[0] = "changed"
	cloned.Diagnostics[0].Bytes = 0
	if snapshot.SectionIDs[0] != "section" || snapshot.Diagnostics[0].Bytes != 6 || cloned.Revision != 7 || cloned.Text != "prompt" {
		t.Fatal("snapshot clone changed values or shares slices")
	}
	for _, snapshot := range []Snapshot{{}, {SectionIDs: []string{}, Diagnostics: []Diagnostic{}}} {
		cloned := snapshot.Clone()
		if cloned.SectionIDs != nil || cloned.Diagnostics != nil {
			t.Fatal("empty snapshot slices clone must be nil")
		}
	}
}
