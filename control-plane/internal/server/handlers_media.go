package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/media"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// ---------------------------------------------------------------- media (phase 3 · stream A; tag media, R25)
//
// People only: an agent session token reaches none of these (agents read no raw audio, 05 "What the agent sees";
// they test models through evals). audio.get and spectrogram.get answer bytes, so they are served by mediaRoutes
// at the non-strict layer; the rest are strict.

// mediaLinkKeyPurpose derives the audio-link signing key from the master key (links die with it).
const mediaLinkKeyPurpose = "media.audio-links"

func (s *Server) newMedia() (*media.Service, *media.Signer) {
	var key []byte
	if s.Secrets != nil {
		key = s.Secrets.DeriveKey(mediaLinkKeyPurpose)
	}
	d := s.defaultsDoc().Media
	svc := &media.Service{Pool: s.Pool, CAS: s.CAS, MaxSpan: float64(d.MaxSpanSeconds.Value),
		Conversions: make(chan struct{}, max(1, d.MaxConversions.Value))}
	if s.CAS != nil {
		svc.Cache = &media.SpanCache{Dir: filepath.Join(s.CAS.Root(), "cache", "media-spans"),
			MaxBytes: func() int64 { return int64(s.defaultsDoc().Media.SpanCacheMB.Value) << 20 }}
	}
	return svc, media.NewSigner(key)
}

// mediaViewer is the person a media request is for (06 "Media": people only): refused for agents (by actor or
// credential), for API keys, worker and host tokens (automation actors), and for credentials that may not read the
// registry.
func mediaViewer(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Principal{}, problems.Unauthenticated.New("sign in to hear audio, or open a signed link")
	}
	if p.Actor.Kind == auth.KindAgent || p.CredentialKind == credentials.KindAgent {
		return auth.Principal{}, problems.Forbidden.New("agents read no raw audio (docs/spec/05-agents.md \"What the agent sees\"); test a model with evals.new and read its scores")
	}
	if p.Actor.Kind != auth.KindUser {
		return auth.Principal{}, problems.Forbidden.New("audio is for people: sign in to hear it (an API key, worker or host token reads none; docs/spec/06-platform.md \"Media\")")
	}
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return auth.Principal{}, err
	}
	return p, nil
}

// audioLinkPath matches the one path a signed link may reach without a session.
var audioLinkPath = regexp.MustCompile(`^/registry/utterances/[^/]+/audio$`)

// signedAudioRequest reports a GET of audio.get that carries a link signature: it may proceed without a principal
// and is checked by the handler (publicRequest).
func signedAudioRequest(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Query().Get("sig") != "" &&
		audioLinkPath.MatchString(strings.TrimPrefix(r.URL.Path, APIPrefix))
}

func spanOf(channel *int, start, end *float64) media.Span {
	return media.Span{Channel: channel, Start: start, End: end}
}

// auditPlay records a play: who, which utterance and span, and how (session or link).
func (s *Server) auditPlay(ctx context.Context, op string, actor auth.Actor, status int, u media.Utterance, detail map[string]any) {
	detail["utteranceId"], detail["audio"] = u.ID, u.Hash
	if err := audit.Write(context.WithoutCancel(ctx), s.Pool, audit.Entry{Operation: op, Actor: actor, Outcome: audit.OutcomeOK,
		Status: status, Detail: detail}); err != nil {
		s.Log.Error("audit a play", "operation", op, "utterance", u.ID, "err", err)
	}
}

func spanDetail(start, end float64, channel int) map[string]any {
	d := map[string]any{"start": round3(start), "end": round3(end)}
	if channel >= 0 {
		d["channel"] = channel
	}
	return d
}

// mediaRoutes overrides the operations whose answers are bytes: audio.get (WAV with byte ranges) and
// spectrogram.get (a manifest or a tile).
type mediaRoutes struct {
	api.ServerInterface
	s *Server
}

// AudioGet serves audio.get.
func (m mediaRoutes) AudioGet(w http.ResponseWriter, r *http.Request, id api.UtteranceRef, params api.AudioGetParams) {
	s := m.s
	if err := s.audioGet(w, r, id, params); err != nil {
		s.writeProblem(w, r, err)
	}
}

func (s *Server) audioGet(w http.ResponseWriter, r *http.Request, id api.UtteranceRef, params api.AudioGetParams) error {
	ctx := r.Context()
	u, err := media.Lookup(ctx, s.Pool, id)
	if err != nil {
		return err
	}
	span := spanOf(params.Channel, params.Start, params.End)
	var (
		actor auth.Actor
		via   = "session"
	)
	if params.Sig != nil {
		// A signed link: the signature binds utterance, span, viewer and expiry; it is checked even when a session
		// came along, and the play is the viewer's.
		l := media.Link{Utterance: u.ID, Span: span, Viewer: deref(params.Viewer)}
		if params.Exp != nil {
			l.Expires = *params.Exp
		}
		if l.Viewer == "" || params.Exp == nil {
			return problems.MediaLinkInvalid.New("a signed link carries viewer, exp and sig")
		}
		if err := s.mediaLinks.Verify(l, *params.Sig); err != nil {
			return err
		}
		if p, ok := auth.PrincipalFromContext(ctx); ok && (p.Actor.Kind != auth.KindUser || p.CredentialKind == credentials.KindAgent) {
			return problems.Forbidden.New("audio is for people: agents, API keys, worker and host tokens read none, signed link or not")
		}
		actor, via = auth.Actor{Kind: auth.KindUser, ID: l.Viewer}, "link"
	} else {
		p, err := mediaViewer(ctx)
		if err != nil {
			return err
		}
		actor = p.Actor
	}
	body, err := s.media.Audio(u, span)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Disposition", "inline")
	// The first request of a play — whatever byte range it asks for — is audited, before the client has any byte.
	window := time.Duration(s.defaultsDoc().Media.PlayAuditWindowSeconds.Value) * time.Second
	audit := func(status int) {
		if r.Method != http.MethodGet ||
			!s.mediaPlays.First(media.PlayKey(actor.Kind, actor.ID, u.ID, body.Start, body.End, body.Channel), window) {
			return
		}
		d := spanDetail(body.Start, body.End, body.Channel)
		d["via"] = via
		if rg := r.Header.Get("Range"); rg != "" {
			d["range"] = rg[:min(len(rg), 100)]
		}
		s.auditPlay(ctx, "audio.get", actor, status, u, d)
	}
	_, err = media.Serve(w, r, body.Content, body.Size, "audio/wav", audit)
	return err
}

// SpectrogramGet serves spectrogram.get.
func (m mediaRoutes) SpectrogramGet(w http.ResponseWriter, r *http.Request, id api.UtteranceRef, params api.SpectrogramGetParams) {
	s := m.s
	if err := s.spectrogramGet(w, r, id, params); err != nil {
		s.writeProblem(w, r, err)
	}
}

func (s *Server) spectrogramGet(w http.ResponseWriter, r *http.Request, id api.UtteranceRef, params api.SpectrogramGetParams) error {
	ctx := r.Context()
	if _, err := mediaViewer(ctx); err != nil {
		return err
	}
	u, err := media.Lookup(ctx, s.Pool, id)
	if err != nil {
		return err
	}
	t, err := s.media.FindTiles(ctx, u)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if params.Tile != nil {
		b, err := s.media.File(t, *params.Tile+".u8")
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		_, _ = w.Write(b)
		return nil
	}
	b, err := s.media.File(t, media.TilesManifest)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return problems.ValidationFailed.New("the manifest of %s is not JSON: %v", t.Artifact, err)
	}
	doc["artifact"] = t.Artifact
	if _, ok := doc["audio"]; !ok {
		doc["audio"] = u.Hash
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(doc)
}

// AudioGet is served by mediaRoutes (bytes with ranges); the strict layer never routes it.
func (s *Server) AudioGet(context.Context, api.AudioGetRequestObject) (api.AudioGetResponseObject, error) {
	return nil, problems.NotImplemented.New("audio.get is served at the non-strict layer")
}

// SpectrogramGet is served by mediaRoutes (manifest or tile bytes); the strict layer never routes it.
func (s *Server) SpectrogramGet(context.Context, api.SpectrogramGetRequestObject) (api.SpectrogramGetResponseObject, error) {
	return nil, problems.NotImplemented.New("spectrogram.get is served at the non-strict layer")
}

// AudioSign implements audio.sign: a short-lived link for the signed-in viewer, audited.
func (s *Server) AudioSign(ctx context.Context, req api.AudioSignRequestObject) (api.AudioSignResponseObject, error) {
	p, err := mediaViewer(ctx)
	if err != nil {
		return nil, err
	}
	u, err := media.Lookup(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	var body api.AudioSignRequest
	if req.Body != nil {
		body = *req.Body
	}
	span := spanOf(body.Channel, body.Start, body.End)
	info, err := s.media.Info(u)
	if err != nil {
		return nil, err
	}
	if body.Channel != nil && *body.Channel >= info.Channels {
		return nil, problems.BadRequest.New("channel %d: the audio has %d channel(s)", *body.Channel, info.Channels)
	}
	if body.Start != nil && body.End != nil && *body.End <= *body.Start {
		return nil, problems.BadRequest.New("end (%.3f s) must be after start (%.3f s)", *body.End, *body.Start)
	}
	ttl := time.Duration(s.defaultsDoc().Media.SignedLinkTTLSeconds.Value) * time.Second
	exp := s.mediaLinks.Now().Add(ttl).Truncate(time.Second)
	l := media.Link{Utterance: u.ID, Span: span, Viewer: p.Actor.ID, Expires: exp.Unix()}
	q := s.mediaLinks.Query(l)
	channels := info.Channels
	if body.Channel != nil {
		channels = 1
	}
	out := api.AudioLink{
		Url: APIPrefix + "/registry/utterances/" + u.ID + "/audio?" + q.Encode(), ExpiresAt: exp.UTC(),
		UtteranceId: u.ID, Audio: u.Hash, DurationS: round3(info.Duration()), SampleRate: media.ServeRate, Channels: channels,
		OriginSampleRate: &info.SampleRate, Start: body.Start, End: body.End,
	}
	start, end := 0.0, info.Duration()
	if body.Start != nil {
		start = *body.Start
	}
	if body.End != nil {
		end = min(*body.End, end)
	}
	ch := -1
	if body.Channel != nil {
		ch = *body.Channel
	}
	d := spanDetail(start, end, ch)
	d["expiresAt"] = out.ExpiresAt.Format(time.RFC3339)
	s.auditPlay(ctx, "audio.sign", p.Actor, http.StatusOK, u, d)
	return api.AudioSign200JSONResponse(out), nil
}

// PeaksGet implements peaks.get: the 10 ms peaks (cached as a peaks artifact) pooled to hopMs over the span.
func (s *Server) PeaksGet(ctx context.Context, req api.PeaksGetRequestObject) (api.PeaksGetResponseObject, error) {
	if _, err := mediaViewer(ctx); err != nil {
		return nil, err
	}
	u, err := media.Lookup(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	hopMs := 10
	if req.Params.HopMs != nil {
		hopMs = *req.Params.HopMs
	}
	if hopMs%10 != 0 {
		return nil, problems.BadRequest.New("hopMs must be a multiple of 10 (the base peaks are 10 ms); got %d", hopMs)
	}
	p, hash, rate, err := s.media.Peaks(ctx, u)
	if err != nil {
		return nil, err
	}
	dur := float64(p.Frames()) * p.HopS
	if u.Duration > 0 {
		dur = u.Duration
	}
	start, end := 0.0, dur
	if req.Params.Start != nil {
		start = *req.Params.Start
	}
	if req.Params.End != nil {
		end = min(*req.Params.End, end)
	}
	if end <= start {
		return nil, problems.BadRequest.New("end (%.3f s) must be after start (%.3f s)", end, start)
	}
	first := int(start / p.HopS)
	count := int(end/p.HopS+0.999999) - first
	span := p.Span(first, count, hopMs/10)
	out := api.AudioPeaks{
		UtteranceId: u.ID, Channels: span.Channels, HopS: span.HopS, Frames: span.Frames(),
		Start: round3(float64(first) * p.HopS), DurationS: round3(dur), Encoding: api.Int8Minmax, Data: span.Bytes(),
	}
	if rate > 0 {
		out.OriginSampleRate = &rate
	}
	if hash != "" {
		out.Artifact = &hash
	}
	if len(span.Clipped) > 0 {
		out.Clipped = &span.Clipped
	}
	return api.PeaksGet200JSONResponse(out), nil
}

// WordsGet implements words.get: an utterance's timed hypothesis words, marked against a scores artifact.
func (s *Server) WordsGet(ctx context.Context, req api.WordsGetRequestObject) (api.WordsGetResponseObject, error) {
	if _, err := mediaViewer(ctx); err != nil {
		return nil, err
	}
	u, err := media.Lookup(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	hypA, err := s.readableArtifact(ctx, req.Params.Hypotheses)
	if err != nil {
		return nil, err
	}
	var scA *artifacts.Artifact
	if req.Params.Scores != nil {
		a, err := s.readableArtifact(ctx, *req.Params.Scores)
		if err != nil {
			return nil, err
		}
		scA = &a
	}
	for _, a := range []*artifacts.Artifact{&hypA, scA} {
		if a != nil && a.Evicted != nil {
			return nil, problems.ArtifactEvicted.New("the %s artifact %s was evicted from the content store on %s (eval artifacts are kept %d days after their eval record's last use); run the eval again to see its words",
				a.Type, a.Hash, a.Evicted.At.UTC().Format(time.DateOnly), defaults.Get().Eval.ArtifactRetentionDays.Value)
		}
	}
	hyp, sc, err := s.media.Words(u, hypA, scA)
	if err != nil {
		return nil, err
	}
	words, deletions, aligned := hyp.Words, []media.Deletion{}, false
	if sc != nil {
		words, deletions, aligned = media.Align(hyp.Words, sc.Ops)
	}
	out := api.UtteranceWords{UtteranceId: u.ID, Audio: u.Hash, Text: hyp.Text, Aligned: aligned,
		Words: make([]api.HypothesisWord, 0, len(words)), Deletions: make([]api.DeletedWord, 0, len(deletions))}
	for _, w := range words {
		hw := api.HypothesisWord{Word: w.Word, Start: w.Start, End: w.End, Confidence: w.Confidence}
		if w.Op != "" {
			op := api.HypothesisWordOp(w.Op)
			hw.Op = &op
		}
		if w.Ref != "" {
			hw.Ref = &w.Ref
		}
		out.Words = append(out.Words, hw)
	}
	for _, d := range deletions {
		out.Deletions = append(out.Deletions, api.DeletedWord{Before: d.Before, Ref: d.Ref})
	}
	if len(hyp.Partials) > 0 {
		ps := make([]api.PartialEvent, 0, len(hyp.Partials))
		for _, p := range hyp.Partials {
			ps = append(ps, api.PartialEvent{Text: p.Text, AudioOffsetMs: p.AudioOffsetMs, EmitMs: p.EmitMs})
		}
		out.Partials = &ps
	}
	if sc != nil {
		out.Ref, out.Sub, out.Del, out.Ins, out.RefWords = &sc.Ref, &sc.Sub, &sc.Del, &sc.Ins, &sc.RefWords
	}
	return api.WordsGet200JSONResponse(out), nil
}

func (s *Server) readableArtifact(ctx context.Context, hash string) (artifacts.Artifact, error) {
	a, err := artifacts.Get(ctx, s.Pool, hash)
	if err != nil {
		return artifacts.Artifact{}, err
	}
	if err := s.checkArtifact(ctx, a); err != nil {
		return artifacts.Artifact{}, err
	}
	return a, nil
}
