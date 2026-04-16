package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	tengo "github.com/d5/tengo/v2"
)

var scriptCompileCache sync.Map // map[string]*tengo.Compiled

var scriptIdentRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

var tengoKeywords = map[string]struct{}{
	"break": {}, "continue": {}, "else": {}, "false": {}, "for": {},
	"if": {}, "immutable": {}, "import": {}, "return": {}, "true": {},
	"undefined": {},
}

func CompileScript(src string) (*tengo.Compiled, error) {
	h := sha256.Sum256([]byte(src))
	key := hex.EncodeToString(h[:])
	if v, ok := scriptCompileCache.Load(key); ok {
		return v.(*tengo.Compiled), nil
	}

	s := tengo.NewScript([]byte(src))
	for _, ident := range extractScriptIdentifiers(src) {
		if err := s.Add(ident, float64(0)); err != nil {
			return nil, fmt.Errorf("declare script variable %q: %w", ident, err)
		}
	}
	compiled, err := s.Compile()
	if err != nil {
		return nil, err
	}

	actual, _ := scriptCompileCache.LoadOrStore(key, compiled)
	return actual.(*tengo.Compiled), nil
}

func RunScript(compiled *tengo.Compiled, r Record) (Record, error) {
	if compiled == nil {
		return cloneRecord(r), fmt.Errorf("nil compiled script")
	}

	clone := compiled.Clone()
	original := cloneRecord(r)

	for k, v := range r {
		if !clone.IsDefined(k) {
			continue
		}
		if err := clone.Set(k, v); err != nil {
			return original, fmt.Errorf("set variable %q: %w", k, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	if err := clone.RunContext(ctx); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			slog.Warn("pipeline script execution timed out; keeping original record",
				"component", "pipeline",
				"timeout_ms", 5)
			return original, nil
		}
		return original, err
	}

	out := cloneRecord(original)
	for _, v := range clone.GetAll() {
		switch n := v.Value().(type) {
		case float64:
			out[v.Name()] = n
		case float32:
			out[v.Name()] = float64(n)
		case int:
			out[v.Name()] = float64(n)
		case int32:
			out[v.Name()] = float64(n)
		case int64:
			out[v.Name()] = float64(n)
		case uint:
			out[v.Name()] = float64(n)
		case uint32:
			out[v.Name()] = float64(n)
		case uint64:
			out[v.Name()] = float64(n)
		}
	}

	return out, nil
}

func extractScriptIdentifiers(src string) []string {
	seen := make(map[string]struct{})
	idents := make([]string, 0)
	for _, token := range scriptIdentRe.FindAllString(src, -1) {
		if _, kw := tengoKeywords[token]; kw {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		idents = append(idents, token)
	}
	return idents
}
