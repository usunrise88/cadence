package media

import (
	"bufio"
	"encoding/json"
	"io"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// The audio view's reference-word track (R51; phase 4 tail): an utterance's row of an alignment artifact
// (cadence.alignment/1, written by align_reference and attached to a golden set's dataset artifact), keyed by the
// audio's hash like hypotheses rows. An aligned row carries each reference token at its aligned times; an unaligned
// one only the text and the reason — nothing is estimated.

// RefWord is one timed reference word of an alignment row.
type RefWord struct {
	Index int      `json:"index"`
	Word  string   `json:"word"`
	Start float64  `json:"start"`
	End   float64  `json:"end"`
	Score *float64 `json:"score,omitempty"`
}

// ReferenceRow is one utterance's row of an alignment artifact.
type ReferenceRow struct {
	Audio    string    `json:"audio"`
	Text     string    `json:"text"`
	Language string    `json:"language,omitempty"`
	Aligned  bool      `json:"aligned"`
	Words    []RefWord `json:"words,omitempty"`
	Skipped  []int     `json:"skipped,omitempty"`
	Reason   string    `json:"reason,omitempty"`
}

// alignmentHeader is the first line of an alignment artifact.
type alignmentHeader struct {
	Alignment struct {
		Format  string `json:"format"`
		Aligner struct {
			Auxiliary string `json:"auxiliary"`
		} `json:"aligner"`
	} `json:"alignment"`
}

// Reference returns the utterance's row of an alignment artifact and the aligner its header names
// (auxiliary/<name>, "" when it names none).
func (s *Service) Reference(u Utterance, alignment artifacts.Artifact) (ReferenceRow, string, error) {
	if s.CAS == nil {
		return ReferenceRow{}, "", problems.NotImplemented.New("this control plane has no content store")
	}
	var row ReferenceRow
	if err := s.scan(alignment, "alignment.jsonl", u, &row); err != nil {
		return ReferenceRow{}, "", err
	}
	if !row.Aligned {
		row.Words = nil
	}
	aligner := ""
	if !alignment.Directory {
		if f, err := s.CAS.Open(alignment.Hash); err == nil {
			line, _ := bufio.NewReader(io.LimitReader(f, 1<<16)).ReadBytes('\n')
			_ = f.Close()
			var h alignmentHeader
			if json.Unmarshal(line, &h) == nil {
				aligner = h.Alignment.Aligner.Auxiliary
			}
		}
	}
	return row, aligner, nil
}
