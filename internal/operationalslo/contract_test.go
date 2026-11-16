package operationalslo

import "testing"

func TestNumericServiceSLOContractMatchesPython(t *testing.T) {
	rows := Matrix()
	if len(rows) != 432 {
		t.Fatalf("service SLO matrix has %d rows", len(rows))
	}
	for _, row := range rows {
		if row.P50MS <= 0 || row.P50MS > row.P95MS || row.P95MS > row.P99MS ||
			row.MinThroughputPerSecond <= 0 || row.MaxQueueLagMS <= 0 || row.RTOMS <= 0 {
			t.Fatalf("invalid service SLO row: %+v", row)
		}
	}
	revision, err := ContractRevision()
	if err != nil {
		t.Fatal(err)
	}
	const pythonRevision = "service-slo:sha256:a3b9adac91fec1a5ce46b8b3c341b5597afafaf022b63651c15cfdb176fa4844"
	if revision != pythonRevision {
		t.Fatalf("service SLO revision %s differs from Python %s", revision, pythonRevision)
	}
}
