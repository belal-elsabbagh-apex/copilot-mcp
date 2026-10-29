package orchestrator

import (
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// get_job / get_job_logs accept one `jobId` XOR a `jobIds` batch; the answer shape follows.

// JobIDs is the resolved lookup: the ids and whether the caller asked for a batch. A single id
// keeps failing the call on a bad lookup; a batch isolates failures per entry.
type JobIDs struct {
	IDs   []string
	Batch bool
}

// ResolveJobIDs applies the jobId XOR jobIds rule. jobID "" = absent; jobIDs nil = absent (an
// empty non-nil slice is a present, empty batch, as in TS). Violations are Expected errors.
func ResolveJobIDs(jobID string, jobIDs []string) (JobIDs, error) {
	switch {
	case jobID != "" && jobIDs != nil:
		return JobIDs{}, shared.Expectedf("provide only one of jobId or jobIds")
	case jobIDs != nil:
		return JobIDs{IDs: jobIDs, Batch: true}, nil
	case jobID != "":
		return JobIDs{IDs: []string{jobID}}, nil
	}
	return JobIDs{}, shared.Expectedf("provide either jobId or jobIds")
}

type envelopeHead struct {
	Orchestrator Orchestrator `json:"orchestrator"`
	Env          config.Env   `json:"env"`
	Folder       string       `json:"folder"`
}

type batchEnvelope[E any] struct {
	envelopeHead
	Count int `json:"count"`
	Jobs  []E `json:"jobs"`
}

type singleEnvelope[E any] struct {
	envelopeHead
	entry *E
}

// MarshalJSON flattens the entry's fields beside the envelope fields (the TS `...entries[0]`).
func (s singleEnvelope[E]) MarshalJSON() ([]byte, error) {
	head, err := shared.MarshalJSON(s.envelopeHead)
	if err != nil || s.entry == nil {
		return head, err
	}
	body, err := shared.MarshalJSON(s.entry)
	if err != nil {
		return nil, err
	}
	if len(body) <= 2 { // "{}"
		return head, nil
	}
	return append(append(head[:len(head)-1], ','), body[1:]...), nil
}

// JobEntriesEnvelope shapes the answer: single => the entry's fields flat under the envelope;
// batch => { count, jobs: entries }. Entries must marshal to JSON objects.
func JobEntriesEnvelope[E any](orchestrator Orchestrator, env config.Env, folder string, batch bool, entries []E) any {
	head := envelopeHead{Orchestrator: orchestrator, Env: env, Folder: folder}
	if batch {
		if entries == nil {
			entries = []E{}
		}
		return batchEnvelope[E]{envelopeHead: head, Count: len(entries), Jobs: entries}
	}
	if len(entries) == 0 {
		return singleEnvelope[E]{envelopeHead: head}
	}
	return singleEnvelope[E]{envelopeHead: head, entry: &entries[0]}
}
