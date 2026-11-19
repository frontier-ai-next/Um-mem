package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestAgentTopicPathGrowsWithoutReplacingRecords(t *testing.T) {
	ctx := context.Background()
	config := Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "default", BackendID: "mnemosyne"}
	first, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	paths := [][]string{{"Атмосфера теплицы", "Схема датчиков"}, {"атмосфера  теплицы", "СХЕМА ДАТЧИКОВ"}}
	var group sync.WaitGroup
	results := make([]AgentTopic, 2)
	failures := make([]error, 2)
	for i, selected := range []*Engine{first, second} {
		group.Add(1)
		go func() {
			defer group.Done()
			results[i], failures[i] = selected.EnsureAgentTopic(ctx, paths[i], "How the sensors are wired")
		}()
	}
	group.Wait()
	if failures[0] != nil || failures[1] != nil || results[0].BlockID == "" ||
		results[0].BlockID != results[1].BlockID {
		t.Fatalf("concurrent writes did not reuse one path: %+v, %v", results, failures)
	}
	if err := first.RefreshCatalog(); err != nil {
		t.Fatal(err)
	}
	leaf := results[0].BlockID
	created, err := first.RememberCandidate(ctx, CandidateRequest{RequestedBlockID: leaf,
		Content: "The bay sensor is VA-214.", Kind: "equipment-tag"})
	if err != nil || !created.Saved {
		t.Fatalf("writing an arbitrary note kind failed: %+v, %v", created, err)
	}
	if _, err := first.EnsureAgentTopic(ctx, []string{results[0].Path[0], "Проверки", "Повторный замер"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := first.EnsureAgentTopic(ctx, []string{results[0].Path[0], results[0].Path[1], "отчёт"}, ""); err == nil {
		t.Fatal("silently converted an occupied leaf into a folder")
	}
	topics, err := second.AgentTopics("Повторный замер", 8)
	if err != nil || len(topics) != 4 || !strings.Contains(strings.Join(topics[0].Path, " "), "Повторный замер") {
		t.Fatalf("unexpected authored topics from another client: %+v, %v", topics, err)
	}
	record, err := first.ReadStored(ctx, created.RecordID)
	if err != nil || record == nil || record.Kind != "equipment-tag" ||
		record.BlockMembership.PrimaryBlockID != leaf {
		t.Fatalf("earlier record lost its authored leaf: %+v, %v", record, err)
	}
	broad, err := first.EnsureAgentTopic(ctx, []string{"Вентиляция"}, "История работ по вентиляции")
	if err != nil {
		t.Fatal(err)
	}
	partitions := make(map[string]bool)
	for _, item := range []string{"Новый вентилятор поставили в июне.", "Замер после монтажа показал снижение шума."} {
		saved, err := first.RememberCandidate(ctx, CandidateRequest{Content: item, RequestedBlockID: broad.BlockID})
		if err != nil || !saved.Saved {
			t.Fatalf("writing to the broad topic failed: %+v, %v", saved, err)
		}
		row, err := first.ReadStored(ctx, saved.RecordID)
		if err != nil || row == nil || row.BlockMembership == nil {
			t.Fatalf("broad topic lost a record: %+v, %v", row, err)
		}
		partitions[row.BlockMembership.BackendPartitionID] = true
	}
	if len(partitions) != 1 || partitions[""] {
		t.Fatalf("notes in one block did not share one native partition: %+v", partitions)
	}

	config.WorkspaceID = "beta"
	other, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if seen, err := other.AgentTopics("", 8); err != nil || len(seen) != 0 {
		t.Fatalf("topics crossed a workspace boundary: %+v, %v", seen, err)
	}
}
