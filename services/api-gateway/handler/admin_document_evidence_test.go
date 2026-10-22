package handler

import "testing"

func TestCompleteDocumentEvidence(t *testing.T) {
	front := documentEvidenceFile{Side: "front", StorageKey: "driver/front"}
	back := documentEvidenceFile{Side: "back", StorageKey: "driver/back"}
	t.Run("retain both mobile uploads without new uploads", func(t *testing.T) {
		got, err := completeDocumentEvidence("driver_license", []documentEvidenceFile{front, back})
		if err != nil || len(got) != 2 || got[0].StorageKey != front.StorageKey || got[1].StorageKey != back.StorageKey {
			t.Fatalf("evidence lost: %v %v", got, err)
		}
	})
	t.Run("complete missing side while retaining existing side", func(t *testing.T) {
		added := documentEvidenceFile{Side: "back", StorageKey: "admin/back"}
		got, err := completeDocumentEvidence("driver_license", []documentEvidenceFile{front, added})
		if err != nil || got[0].StorageKey != front.StorageKey || got[1].StorageKey != added.StorageKey {
			t.Fatalf("merge failed: %v %v", got, err)
		}
	})
	t.Run("reject incomplete or invalid evidence", func(t *testing.T) {
		for _, files := range [][]documentEvidenceFile{{front}, {back}, {{Side: "front", StorageKey: ""}, back}, {{Side: "document", StorageKey: "other"}, back}} {
			if _, err := completeDocumentEvidence("driver_license", files); err == nil {
				t.Fatalf("accepted invalid evidence: %v", files)
			}
		}
	})
}
