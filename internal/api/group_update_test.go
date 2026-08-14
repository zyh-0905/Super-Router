package api

import (
	"encoding/json"
	"testing"
)

func TestGroupIDsPresenceDistinguishesOmittedAndEmpty(t *testing.T) {
	var omitted channelUpdateRequest
	if err := json.Unmarshal([]byte(`{"name":"unchanged"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.GroupIDs != nil {
		t.Fatal("omitted group_ids must remain nil")
	}

	var empty channelUpdateRequest
	if err := json.Unmarshal([]byte(`{"group_ids":[]}`), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.GroupIDs == nil {
		t.Fatal("explicit empty group_ids must be distinguishable from omitted")
	}
	if len(*empty.GroupIDs) != 0 {
		t.Fatalf("expected empty group_ids, got %v", *empty.GroupIDs)
	}
}

func TestKeyGroupIDsPresenceDistinguishesOmittedAndEmpty(t *testing.T) {
	var omitted keyUpdateRequest
	if err := json.Unmarshal([]byte(`{"enabled":true}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.GroupIDs != nil {
		t.Fatal("omitted group_ids must remain nil")
	}

	var empty keyUpdateRequest
	if err := json.Unmarshal([]byte(`{"group_ids":[]}`), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.GroupIDs == nil {
		t.Fatal("explicit empty group_ids must be distinguishable from omitted")
	}
	if len(*empty.GroupIDs) != 0 {
		t.Fatalf("expected empty group_ids, got %v", *empty.GroupIDs)
	}
}

func TestAPIKeyUpdateRequiresExistingRow(t *testing.T) {
	if apiKeyUpdateFound(0) {
		t.Fatal("zero affected rows must be treated as a missing api key")
	}
	if !apiKeyUpdateFound(1) {
		t.Fatal("one affected row must be treated as an existing api key")
	}
}
