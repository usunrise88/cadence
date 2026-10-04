package data

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Purposes of a dataset artifact (its header's purpose).
const (
	PurposeSpeech = "speech"
	PurposeNoise  = "noise"
)

// TagNoiseBank marks noise-bank collections.
const TagNoiseBank = "noise-bank"

// noisePayload is a noise-bank version's payload: where the noise came from and how much there is. Its clips are the
// artifact's audio files; a noise bank has no utterances or transcripts and never enters a mix.
type noisePayload struct {
	Source       string            `json:"source"`
	SourceIDs    []string          `json:"sourceIds"`
	Licence      string            `json:"licence"`
	Clips        int               `json:"clips"`
	Hours        float64           `json:"hours"`
	Bytes        int64             `json:"bytes"`
	SampleRateHz int               `json:"sampleRateHz,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Artifact     steps.ArtifactRef `json:"artifact"`
	Lineage      lineage           `json:"lineage"`
	// Mined says where a bank mined from recordings came from (noise_mine: the segments, roles and clip rules).
	Mined json.RawMessage `json:"mined,omitempty"`
}

// TagMined marks noise banks mined from the silences of recordings (noise_mine) rather than imported clips.
const TagMined = "mined"

// importNoise registers a noise-bank version (collection noise-bank/<name>) from a dataset artifact whose purpose is
// noise: the source with its licence, then a frozen version fingerprinted like a dataset's content. Re-importing the
// same clips into the same collection returns the version already there.
func importNoise(ctx context.Context, tx pgx.Tx, a Artifact, src Source, out steps.Output, actor auth.Actor, now time.Time) (registry.Version, []events.Draft, error) {
	h := a.Header
	p := noisePayload{Source: h.Source.URL, SourceIDs: []string{src.ID}, Licence: src.Licence, Tags: append([]string{}, h.Tags...),
		Artifact: steps.ArtifactRef{Hash: a.Hash, Type: ArtifactType, Size: out.Artifact.Size},
		Lineage:  lineage{PipelineRunID: out.PipelineRunID, StepID: out.StepID, ProjectID: out.ProjectID}, Mined: h.Mined}
	if p.Source == "" {
		p.Source = "source:" + src.Name
	}
	if out.Spec.Kind != "" {
		p.Lineage.StepKind = out.Spec.KindRef()
	}
	rates := map[int]bool{}
	for _, l := range a.Lines {
		p.Clips++
		p.Hours += l.Duration / 3600
		p.Bytes += l.Size
		rates[l.SampleRate] = true
	}
	p.Hours = round(p.Hours, 4)
	if len(rates) == 1 {
		p.SampleRateHz = a.Lines[0].SampleRate
	}
	body, err := json.Marshal(p)
	if err != nil {
		return registry.Version{}, nil, fmt.Errorf("encode noise bank payload: %w", err)
	}
	tags := []string{"source:" + src.Name, TagNoiseBank}
	if len(h.Mined) > 0 {
		tags = append(tags, TagMined)
	}
	for _, t := range h.Tags {
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	sort.Strings(tags)
	desc := h.Description
	if desc == "" {
		desc = fmt.Sprintf("Background noise from source %s (%d clips)", src.Name, p.Clips)
	}
	name := strings.TrimPrefix(collectionName(out, h), "noise-bank/")
	v, _, drafts, err := registry.Register(ctx, tx, registry.RegisterInput{
		Kind: registry.KindNoiseBank, Name: "noise-bank/" + name, Description: desc, Tags: tags, Licence: src.Licence,
		Payload: body, Actor: actor, Freeze: true, Fingerprint: ContentFingerprint(a.Lines),
	}, now)
	if err != nil {
		return registry.Version{}, nil, fmt.Errorf("register noise bank version: %w", err)
	}
	return v, drafts, nil
}
