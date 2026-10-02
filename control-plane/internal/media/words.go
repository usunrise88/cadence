package media

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Word is one timed hypothesis word, as transcribe steps write it in a hypotheses row (R42) and as the audio view
// draws it; Op and Ref come from a scores row's alignment.
type Word struct {
	Word       string   `json:"word"`
	Start      float64  `json:"start"`
	End        float64  `json:"end"`
	Confidence *float64 `json:"confidence,omitempty"`
	Op         string   `json:"op,omitempty"`
	Ref        string   `json:"ref,omitempty"`
}

// Deletion is a reference word the hypothesis dropped, placed before hypothesis word Before.
type Deletion struct {
	Before int    `json:"before"`
	Ref    string `json:"ref"`
}

// Partial is one partial event of a streaming decode.
type Partial struct {
	Text          string   `json:"text"`
	AudioOffsetMs *float64 `json:"audioOffsetMs,omitempty"`
	EmitMs        *float64 `json:"emitMs,omitempty"`
}

// HypothesisRow is one line of a hypotheses artifact (the fields the audio view reads).
type HypothesisRow struct {
	Audio    string    `json:"audio"`
	Text     string    `json:"text"`
	Words    []Word    `json:"words"`
	Partials []Partial `json:"partials,omitempty"`
}

// ScoreRow is one line of a scores artifact's utterances.jsonl (the fields the audio view reads).
type ScoreRow struct {
	Audio    string     `json:"audio"`
	Ref      string     `json:"ref"`
	Hyp      string     `json:"hyp"`
	RefWords int        `json:"refWords"`
	Sub      int        `json:"sub"`
	Del      int        `json:"del"`
	Ins      int        `json:"ins"`
	Ops      [][]string `json:"ops"`
}

// FindRow scans JSON lines for the row whose "audio" is audio and decodes it into v; found is false when no line
// has it. Lines without the hash are skipped without decoding.
func FindRow(r io.Reader, audio string, v any) (bool, error) {
	needle := []byte(`"` + audio + `"`)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<16), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, needle) {
			continue
		}
		var probe struct {
			Audio string `json:"audio"`
		}
		if err := json.Unmarshal(line, &probe); err != nil || probe.Audio != audio {
			continue
		}
		if err := json.Unmarshal(line, v); err != nil {
			return false, fmt.Errorf("decode row of %s: %w", audio, err)
		}
		return true, nil
	}
	if err := sc.Err(); err != nil {
		return false, fmt.Errorf("read rows: %w", err)
	}
	return false, nil
}

// Align marks words with the alignment ops of a scores row. Ops consume hypothesis words in order (=, S and I take
// one, D none); when they consume exactly len(words) the marks apply and aligned is true. Otherwise (the scoring
// normaliser split or merged words) the words stay unmarked and only the deletions' reference words are returned
// at the end.
func Align(words []Word, ops [][]string) (out []Word, deletions []Deletion, aligned bool) {
	out = append([]Word(nil), words...)
	deletions = []Deletion{}
	consumed := 0
	for _, op := range ops {
		if len(op) > 0 && op[0] != "D" {
			consumed++
		}
	}
	aligned = consumed == len(words)
	i := 0
	for _, op := range ops {
		if len(op) == 0 {
			continue
		}
		ref := ""
		if len(op) > 1 {
			ref = op[1]
		}
		switch op[0] {
		case "D":
			before := i
			if !aligned {
				before = len(words)
			}
			deletions = append(deletions, Deletion{Before: before, Ref: ref})
		case "=", "S", "I":
			if aligned {
				out[i].Op = op[0]
				if op[0] == "S" {
					out[i].Ref = ref
				}
			}
			i++
		}
	}
	return out, deletions, aligned
}
