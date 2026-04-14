package pipeline

import "log/slog"

type Record map[string]float64

func RunTransform(t TransformConfig, r Record) (Record, bool) {
	if r == nil {
		r = Record{}
	}

	switch t.Type {
	case "extract":
		v, ok := r[t.Field]
		if !ok {
			return r, false
		}
		key := t.As
		if key == "" {
			key = t.Field
		}
		out := Record{key: v}
		return out, true

	case "scale":
		inKey, v, ok := selectFieldValue(r, t.Field)
		if !ok {
			return r, false
		}
		out := cloneRecord(r)
		outKey := t.As
		if outKey == "" {
			outKey = inKey
		}
		out[outKey] = (v * t.Factor) + t.Offset
		return out, true

	case "rename":
		v, ok := r[t.From]
		if !ok || t.To == "" {
			return r, false
		}
		out := cloneRecord(r)
		delete(out, t.From)
		out[t.To] = v
		return out, true

	case "filter":
		_, v, ok := selectFieldValue(r, t.Field)
		if !ok {
			return r, false
		}
		if v < t.Min || v > t.Max {
			return r, false
		}
		return r, true

	case "script":
		compiled, err := CompileScript(t.Script)
		if err != nil {
			slog.Error("pipeline script compile failed",
				"component", "pipeline",
				"error", err)
			return r, false
		}
		out, err := RunScript(compiled, r)
		if err != nil {
			slog.Error("pipeline script run failed",
				"component", "pipeline",
				"error", err)
			return r, false
		}
		return out, true

	default:
		return r, false
	}
}

func RunChain(transforms []TransformConfig, r Record) (Record, bool) {
	current := cloneRecord(r)
	for _, t := range transforms {
		next, ok := RunTransform(t, current)
		if !ok {
			return current, false
		}
		current = next
	}
	return current, true
}

func selectFieldValue(r Record, field string) (string, float64, bool) {
	if field != "" {
		v, ok := r[field]
		return field, v, ok
	}
	if v, ok := r["value"]; ok {
		return "value", v, true
	}
	if len(r) == 1 {
		for k, v := range r {
			return k, v, true
		}
	}
	return "", 0, false
}

func cloneRecord(r Record) Record {
	out := make(Record, len(r))
	for k, v := range r {
		out[k] = v
	}
	return out
}
