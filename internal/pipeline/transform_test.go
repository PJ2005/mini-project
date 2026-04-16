package pipeline

import "testing"

func TestRunTransformExtract(t *testing.T) {
	r := Record{"temperature": 21.5, "humidity": 40}
	out, ok := RunTransform(TransformConfig{Type: "extract", Field: "temperature", As: "value"}, r)
	if !ok {
		t.Fatal("extract returned false")
	}
	if len(out) != 1 || out["value"] != 21.5 {
		t.Fatalf("extract output = %#v", out)
	}
}

func TestRunTransformScaleZeroOffset(t *testing.T) {
	r := Record{"value": 10}
	out, ok := RunTransform(TransformConfig{Type: "scale", Factor: 2, Offset: 0, As: "scaled"}, r)
	if !ok {
		t.Fatal("scale returned false")
	}
	if got := out["scaled"]; got != 20 {
		t.Fatalf("scaled value = %v, want %v", got, 20)
	}
}

func TestRunTransformRename(t *testing.T) {
	r := Record{"temp_f": 72.5}
	out, ok := RunTransform(TransformConfig{Type: "rename", From: "temp_f", To: "value"}, r)
	if !ok {
		t.Fatal("rename returned false")
	}
	if _, ok := out["temp_f"]; ok {
		t.Fatal("temp_f key still present")
	}
	if got := out["value"]; got != 72.5 {
		t.Fatalf("renamed value = %v, want %v", got, 72.5)
	}
}

func TestRunTransformFilterPass(t *testing.T) {
	r := Record{"value": 50}
	_, ok := RunTransform(TransformConfig{Type: "filter", Min: 0, Max: 100}, r)
	if !ok {
		t.Fatal("filter unexpectedly dropped")
	}
}

func TestRunTransformFilterDrop(t *testing.T) {
	r := Record{"value": 150}
	_, ok := RunTransform(TransformConfig{Type: "filter", Min: 0, Max: 100}, r)
	if ok {
		t.Fatal("filter expected to drop")
	}
}
