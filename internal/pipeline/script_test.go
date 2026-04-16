package pipeline

import (
	"reflect"
	"testing"
)

func TestScriptBasic(t *testing.T) {
	compiled, err := CompileScript("value = value * 2")
	if err != nil {
		t.Fatalf("compile script: %v", err)
	}
	out, err := RunScript(compiled, Record{"value": 5.0})
	if err != nil {
		t.Fatalf("run script: %v", err)
	}
	if got := out["value"]; got != 10.0 {
		t.Fatalf("value = %v, want %v", got, 10.0)
	}
}

func TestScriptMultiVar(t *testing.T) {
	compiled, err := CompileScript("result = a + b")
	if err != nil {
		t.Fatalf("compile script: %v", err)
	}
	out, err := RunScript(compiled, Record{"a": 2.5, "b": 3.5})
	if err != nil {
		t.Fatalf("run script: %v", err)
	}
	if got := out["result"]; got != 6.0 {
		t.Fatalf("result = %v, want %v", got, 6.0)
	}
}

func TestScriptCompileError(t *testing.T) {
	_, err := CompileScript("this is not valid tengo @@@")
	if err == nil {
		t.Fatal("expected compile error, got nil")
	}
}

func TestScriptCacheHit(t *testing.T) {
	src := "value = value * 2"
	a, err := CompileScript(src)
	if err != nil {
		t.Fatalf("compile script a: %v", err)
	}
	b, err := CompileScript(src)
	if err != nil {
		t.Fatalf("compile script b: %v", err)
	}
	if a != b {
		t.Fatal("expected same compiled pointer from cache")
	}
}

func TestScriptTimeout(t *testing.T) {
	compiled, err := CompileScript("for true {}")
	if err != nil {
		t.Fatalf("compile timeout script: %v", err)
	}
	in := Record{"value": 9.0}
	out, err := RunScript(compiled, in)
	if err != nil {
		t.Fatalf("run timeout script: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("timeout output = %#v, want original %#v", out, in)
	}
}
