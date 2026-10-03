package msa

import "testing"

func TestEdgeAllEqual(t *testing.T) {
	var data []Measurement
	for i := 0; i < 5; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 2; k++ {
				data = append(data, Measurement{PartID: partLabel(i), OperatorID: opLabel(j), Trial: k + 1, Value: 10})
			}
		}
	}
	res, err := Analyze(data, 1, MethodAuto)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SSCheck.WithinTol {
		t.Fatalf("SS rel %.3e", res.SSCheck.RelError)
	}
}

func TestEdgeTooFewCells(t *testing.T) {
	// 2 parts, 2 ops, only one cell has replicates; Henderson still runs.
	data := []Measurement{
		{PartID: "A", OperatorID: "x", Trial: 1, Value: 1},
		{PartID: "A", OperatorID: "x", Trial: 2, Value: 2},
		{PartID: "A", OperatorID: "y", Trial: 1, Value: 3},
		{PartID: "B", OperatorID: "x", Trial: 1, Value: 4},
		{PartID: "B", OperatorID: "y", Trial: 1, Value: 5},
	}
	res, err := Analyze(data, 10, MethodAuto)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SSCheck.WithinTol {
		t.Fatalf("SS rel %.3e", res.SSCheck.RelError)
	}
	t.Logf("pooled=%v used=%d", res.DataUsage.InteractionPooled, res.DataUsage.UsedReadings)
}
