package jobs

import "slices"

var transitions = map[string]map[string][]string{
	"job": {
		"pending":    {"queued", "completed", "failed", "dead_lettered"},
		"queued":     {"processing", "completed", "failed", "dead_lettered"},
		"processing": {"completed", "failed", "dead_lettered"},
	},
	"document": {
		"pending":    {"processing", "deleting"},
		"processing": {"ready", "failed", "deleting"},
		"ready":      {"deleting"},
		"failed":     {"pending", "deleting"},
		"deleting":   {"deleted", "failed"},
	},
	"coverage": {
		"pending": {"running"},
		"running": {"completed", "failed"},
	},
}

func CanTransition(entity, from, to string) bool {
	return slices.Contains(transitions[entity][from], to)
}
