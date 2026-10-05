package promotions

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

// Event types. promotion.recorded is on the target's topic (entity.deployment_target.{id}); promotion.pending,
// promotion.confirmed and promotion.withdrawn are also on deploy.{deployment} when the record names a deployment.
const (
	EventRecorded  = "promotion.recorded"
	EventPending   = "promotion.pending"
	EventConfirmed = "promotion.confirmed"
	EventWithdrawn = "promotion.withdrawn"
)

// DeployTopic is the topic of one deployment (06 "Topic scheme").
func DeployTopic(deploymentID string) string { return "deploy." + deploymentID }

// Stager moves deployments when their promotion records close (stream D4, internal/deployments): a confirmation
// moves the deployment to the record's stage, a withdrawal returns it to the stage before. It runs in the
// transaction that appends the closing record.
type Stager interface {
	Confirm(ctx context.Context, tx pgx.Tx, promotion, confirmation Record) ([]events.Draft, error)
	Withdraw(ctx context.Context, tx pgx.Tx, promotion, withdrawal Record) ([]events.Draft, error)
}

// Service appends, reads and closes promotion records.
type Service struct {
	Pool     *pgxpool.Pool
	Keys     *Keyring
	Defaults func() *defaults.Defaults
	Log      *slog.Logger
	// Stages moves deployments (internal/deployments, stream D4); nil moves nothing.
	Stages Stager
	// Now is the clock (tests); time.Now when nil.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// AppendInput is one record to append. The envelope (schema, kind, id, target, key, createdAt, requestedBy,
// approval, slot, project, reason) is Cadence's; Body holds the kind's own fields (02 "Promotion records": for a
// promotion stage, trafficShare, model, deployable, decoding, previous, evidence).
type AppendInput struct {
	Kind         string
	TargetID     string
	Slot         string
	ProjectID    string
	DeploymentID string // dep_… (stream D4); kept beside the record, not signed
	RefersTo     string // the promotion or rollback a confirmation or withdrawal closes
	Actor        auth.Actor
	ApprovalID   string
	Approver     *auth.Actor
	DecidedAt    time.Time
	Reason       string
	Body         map[string]any
}

// envelopeKeys may not be set through AppendInput.Body.
var envelopeKeys = []string{"schema", "kind", "id", "target", "key", "createdAt", "requestedBy", "approval", "slot", "project", "reason"}

// Append signs and appends a record to its target's chain (stream D4 calls it for promotions and rollbacks, in the
// transaction that decides the approval). It locks the target's row, so appends to one chain are serial.
func (s *Service) Append(ctx context.Context, tx pgx.Tx, in AppendInput) (Record, []events.Draft, error) {
	return s.append(ctx, tx, in, nil, s.now())
}

func (s *Service) append(ctx context.Context, tx pgx.Tx, in AppendInput, signer *Key, now time.Time) (Record, []events.Draft, error) {
	switch in.Kind {
	case KindGenesis, KindPromotion, KindRollback, KindConfirmation, KindWithdrawal, KindTargetChanged, KindKeyRotation:
	default:
		return Record{}, nil, fmt.Errorf("promotions: unknown record kind %q", in.Kind)
	}
	for _, k := range envelopeKeys {
		if _, ok := in.Body[k]; ok {
			return Record{}, nil, fmt.Errorf("promotions: body key %q belongs to the envelope", k)
		}
	}
	if Closes(in.Kind) != (in.RefersTo != "") {
		return Record{}, nil, fmt.Errorf("promotions: only confirmations and withdrawals refer to a record")
	}
	t, err := targets.Lock(ctx, tx, in.TargetID)
	if err != nil {
		return Record{}, nil, err
	}
	if t.Kind != targets.KindDelivery {
		return Record{}, nil, problems.ValidationFailed.New("deployment target %s is a %s target: promotion records are kept for delivery targets only", t.Name, t.Kind)
	}
	if t.State == targets.StateArchived && (in.Kind == KindPromotion || in.Kind == KindRollback) {
		return Record{}, nil, problems.Conflict.New("deployment target %s is archived; it takes no promotions", t.Name)
	}
	if in.Slot != "" && !t.HasSlot(in.Slot) && in.Kind != KindConfirmation && in.Kind != KindWithdrawal {
		return Record{}, nil, problems.ValidationFailed.New("deployment target %s has no slot %q (its slots: %v)", t.Name, in.Slot, t.Slots)
	}
	seq, prev := 1, GenesisPrevHash
	if err := tx.QueryRow(ctx, `SELECT seq + 1, hash FROM promotion_records WHERE target_id = $1 ORDER BY seq DESC LIMIT 1`,
		t.ID).Scan(&seq, &prev); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Record{}, nil, fmt.Errorf("read the chain head: %w", err)
	}
	if (seq == 1) != (in.Kind == KindGenesis) {
		return Record{}, nil, fmt.Errorf("promotions: a chain starts with genesis and has only one (target %s, seq %d, kind %s)", t.Name, seq, in.Kind)
	}
	key := signer
	if key == nil {
		k, err := s.Keys.Current(ctx, tx)
		if err != nil {
			return Record{}, nil, err
		}
		key = &k
	}
	now = now.UTC().Truncate(time.Second)
	id := "prm_" + uuid.Must(uuid.NewV7()).String()
	body := maps.Clone(in.Body)
	if body == nil {
		body = map[string]any{}
	}
	body["schema"] = Schema
	body["kind"] = in.Kind
	body["id"] = id
	body["target"] = map[string]any{"id": t.ID, "name": t.Name, "seq": seq, "prevHash": prev}
	body["key"] = map[string]any{"id": key.ID, "alg": "Ed25519"}
	body["createdAt"] = now.Format(time.RFC3339)
	req := map[string]any{"actor": in.Actor.Kind, "id": in.Actor.ID}
	if in.Actor.Name != "" {
		req["name"] = in.Actor.Name
	}
	if in.Actor.SessionID != "" {
		req["sessionId"] = in.Actor.SessionID
	}
	body["requestedBy"] = req
	if in.ApprovalID != "" {
		ap := map[string]any{"id": in.ApprovalID}
		if in.Approver != nil {
			ap["approver"] = map[string]any{"id": in.Approver.ID, "name": in.Approver.Name}
		}
		if !in.DecidedAt.IsZero() {
			ap["decidedAt"] = in.DecidedAt.UTC().Truncate(time.Second).Format(time.RFC3339)
		}
		body["approval"] = ap
	}
	if in.Slot != "" {
		body["slot"] = in.Slot
	}
	if in.Reason != "" {
		body["reason"] = in.Reason
	}
	if in.ProjectID != "" {
		var slug string
		if err := tx.QueryRow(ctx, "SELECT slug FROM projects WHERE id = $1", in.ProjectID).Scan(&slug); err != nil {
			return Record{}, nil, fmt.Errorf("read project %s: %w", in.ProjectID, err)
		}
		body["project"] = map[string]any{"id": in.ProjectID, "slug": slug}
	}
	canonical, err := CanonicalJSON(body)
	if err != nil {
		return Record{}, nil, err
	}
	r := Record{ID: id, TargetID: t.ID, Seq: seq, Kind: in.Kind, Canonical: canonical, Hash: Hash(canonical), PrevHash: prev,
		Signature: Sign(key.Private, canonical), KeyID: key.ID, Slot: in.Slot, ProjectID: in.ProjectID,
		DeploymentID: in.DeploymentID, RefersTo: in.RefersTo, CreatedAt: now}
	if _, err := tx.Exec(ctx, `INSERT INTO promotion_records (id, target_id, seq, kind, canonical, hash, prev_hash, signature,
			key_id, slot, project_id, deployment_id, refers_to, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), NULLIF($11, ''), NULLIF($12, ''), NULLIF($13, ''), $14)`,
		r.ID, r.TargetID, r.Seq, r.Kind, string(r.Canonical), r.Hash, r.PrevHash, r.Signature, r.KeyID, r.Slot,
		r.ProjectID, r.DeploymentID, r.RefersTo, r.CreatedAt); err != nil {
		return Record{}, nil, fmt.Errorf("append promotion record: %w", err)
	}
	_ = json.Unmarshal(canonical, &r.Body)
	r.Verified = true
	if Pends(r.Kind) {
		r.State = StatePending
	}
	payload := map[string]any{"id": r.ID, "kind": r.Kind, "seq": r.Seq, "hash": r.Hash, "targetId": r.TargetID}
	if r.Slot != "" {
		payload["slot"] = r.Slot
	}
	drafts := []events.Draft{{Topic: targets.Topic(t.ID), Type: EventRecorded, ProjectID: r.ProjectID,
		Entity: &events.EntityRef{Kind: targets.Kind, ID: t.ID, Rev: t.Rev}, Payload: payload}}
	if r.DeploymentID != "" && Pends(r.Kind) {
		drafts = append(drafts, events.Draft{Topic: DeployTopic(r.DeploymentID), Type: EventPending, ProjectID: r.ProjectID, Payload: payload})
	}
	return r, drafts, nil
}

// Genesis appends a delivery target's first record: its payload and the public key that signs its chain. The
// target's creation (targets.OnCreated) calls it in the creating transaction; a staging target gets no chain.
func (s *Service) Genesis(ctx context.Context, tx pgx.Tx, t targets.Target, actor auth.Actor, approvalID string, approver *auth.Actor) (Record, []events.Draft, error) {
	key, err := s.Keys.Current(ctx, tx)
	if err != nil {
		return Record{}, nil, err
	}
	return s.append(ctx, tx, AppendInput{
		Kind: KindGenesis, TargetID: t.ID, Actor: actor, ApprovalID: approvalID, Approver: approver, DecidedAt: s.now(),
		Body: map[string]any{"targetConfig": t.Payload(), "publicKeyPem": PublicPEM(key.Public)},
	}, &key, s.now())
}

// TargetChanged appends a target-changed record naming the new configuration and what changed.
func (s *Service) TargetChanged(ctx context.Context, tx pgx.Tx, before, after targets.Target, changed []string, actor auth.Actor, approvalID string, approver *auth.Actor) (Record, []events.Draft, error) {
	return s.Append(ctx, tx, AppendInput{
		Kind: KindTargetChanged, TargetID: after.ID, Actor: actor, ApprovalID: approvalID, Approver: approver, DecidedAt: s.now(),
		Body: map[string]any{"targetConfig": after.Payload(), "changed": changed, "previousConfig": before.Payload()},
	})
}

const recordCols = `id, target_id, seq, kind, canonical, hash, prev_hash, signature, key_id, coalesce(slot, ''),
	coalesce(project_id, ''), coalesce(deployment_id, ''), coalesce(refers_to, ''), created_at`

func scanRecord(row pgx.CollectableRow) (Record, error) {
	var (
		r     Record
		canon string
	)
	err := row.Scan(&r.ID, &r.TargetID, &r.Seq, &r.Kind, &canon, &r.Hash, &r.PrevHash, &r.Signature, &r.KeyID, &r.Slot,
		&r.ProjectID, &r.DeploymentID, &r.RefersTo, &r.CreatedAt)
	r.Canonical = []byte(canon)
	return r, err
}

// Chain is a delivery target's verified chain.
type Chain struct {
	Target   targets.Target
	Records  []Record
	Intact   bool
	Problems []string
}

// LoadChain reads and verifies a target's whole chain, oldest first.
func LoadChain(ctx context.Context, q storage.Querier, t targets.Target) (Chain, error) {
	rows, err := q.Query(ctx, "SELECT "+recordCols+" FROM promotion_records WHERE target_id = $1 ORDER BY seq", t.ID)
	if err != nil {
		return Chain{}, fmt.Errorf("read the promotion chain: %w", err)
	}
	recs, err := pgx.CollectRows(rows, scanRecord)
	if err != nil {
		return Chain{}, fmt.Errorf("read the promotion chain: %w", err)
	}
	keys, err := Keys(ctx, q)
	if err != nil {
		return Chain{}, err
	}
	pubs := make(map[string]ed25519.PublicKey, len(keys))
	for _, k := range keys {
		pubs[k.ID] = k.Public
	}
	intact, probs := VerifyChain(recs, pubs)
	if t.Kind == targets.KindDelivery && len(recs) == 0 {
		intact, probs = false, append(probs, "the delivery target has no genesis record")
	}
	return Chain{Target: t, Records: recs, Intact: intact, Problems: probs}, nil
}

// Head is the newest record of a chain and how many promotions wait for a receipt.
type Head struct {
	Records, Seq, Pending int
	Hash                  string
}

// Heads returns the chain head of every delivery target with records, by target id (cheap: no verification).
func Heads(ctx context.Context, q storage.Querier) (map[string]Head, error) {
	rows, err := q.Query(ctx, `SELECT r.target_id, count(*), max(r.seq),
			(SELECT h.hash FROM promotion_records h WHERE h.target_id = r.target_id ORDER BY h.seq DESC LIMIT 1),
			count(*) FILTER (WHERE r.kind IN ('promotion', 'rollback')
				AND NOT EXISTS (SELECT 1 FROM promotion_records c WHERE c.refers_to = r.id))
		FROM promotion_records r GROUP BY r.target_id`)
	if err != nil {
		return nil, fmt.Errorf("read chain heads: %w", err)
	}
	defer rows.Close()
	out := map[string]Head{}
	for rows.Next() {
		var (
			id string
			h  Head
		)
		if err := rows.Scan(&id, &h.Records, &h.Seq, &h.Hash, &h.Pending); err != nil {
			return nil, fmt.Errorf("read chain heads: %w", err)
		}
		out[id] = h
	}
	return out, rows.Err()
}

// Get reads one record, verified within its whole chain, with the chain it belongs to.
func Get(ctx context.Context, q storage.Querier, id string) (Record, Chain, error) {
	var targetID string
	if err := q.QueryRow(ctx, "SELECT target_id FROM promotion_records WHERE id = $1", id).Scan(&targetID); errors.Is(err, pgx.ErrNoRows) {
		return Record{}, Chain{}, problems.NotFound.New("no promotion record %q", id)
	} else if err != nil {
		return Record{}, Chain{}, fmt.Errorf("read promotion record: %w", err)
	}
	t, err := targets.Get(ctx, q, targetID)
	if err != nil {
		return Record{}, Chain{}, err
	}
	c, err := LoadChain(ctx, q, t)
	if err != nil {
		return Record{}, Chain{}, err
	}
	for _, r := range c.Records {
		if r.ID == id {
			return r, c, nil
		}
	}
	return Record{}, Chain{}, problems.NotFound.New("no promotion record %q", id)
}

// Rev is a record's revision as the API shows it: 2 once a confirmation or withdrawal closed it, else 1.
func (r Record) Rev() int {
	if r.ClosedBy != "" {
		return 2
	}
	return 1
}

// Delivery is what promotions.verify needs of a record's delivery bundle (promotion_deliveries).
type Delivery struct {
	State         string
	SmokeTotal    int
	SmokeRequired int
}

func readDelivery(ctx context.Context, q storage.Querier, recordID string) (*Delivery, error) {
	var (
		d          Delivery
		total, req *int
	)
	err := q.QueryRow(ctx, "SELECT state, smoke_total, smoke_required FROM promotion_deliveries WHERE record_id = $1", recordID).
		Scan(&d.State, &total, &req)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read delivery of %s: %w", recordID, err)
	}
	if total != nil {
		d.SmokeTotal = *total
	}
	if req != nil {
		d.SmokeRequired = *req
	}
	return &d, nil
}

// SmokeRequired is how many of total smoke utterances must match the staging text for a share (ceil, at least one
// when there are any).
func SmokeRequired(total int, share float64) int {
	if total <= 0 {
		return 0
	}
	n := int(math.Ceil(share*float64(total) - 1e-9))
	return max(1, min(total, n))
}

// Confirm checks a receipt against pending promotion or rollback id and, when it holds, appends a signed
// confirmation record naming the person and lets the Stager move the deployment (02 "Confirmation"). Anything
// else answers promotion-receipt-mismatch and changes nothing. actor must be a person (the handler and the preset
// rule delivery-is-for-people refuse agents first).
func (s *Service) Confirm(ctx context.Context, tx pgx.Tx, id, text string, rev int, actor auth.Actor) (Record, Record, []events.Draft, error) {
	if actor.Kind != auth.KindUser {
		return Record{}, Record{}, nil, problems.PolicyDenied.New("a delivery is confirmed by the person who ran its script, not by %s %s", actor.Kind, actor.ID)
	}
	var targetID string
	if err := tx.QueryRow(ctx, "SELECT target_id FROM promotion_records WHERE id = $1", id).Scan(&targetID); errors.Is(err, pgx.ErrNoRows) {
		return Record{}, Record{}, nil, problems.NotFound.New("no promotion record %q", id)
	} else if err != nil {
		return Record{}, Record{}, nil, fmt.Errorf("read promotion record: %w", err)
	}
	t, err := targets.Lock(ctx, tx, targetID)
	if err != nil {
		return Record{}, Record{}, nil, err
	}
	c, err := LoadChain(ctx, tx, t)
	if err != nil {
		return Record{}, Record{}, nil, err
	}
	idx := -1
	for i, r := range c.Records {
		if r.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		return Record{}, Record{}, nil, problems.NotFound.New("no promotion record %q", id)
	}
	rec := c.Records[idx]
	if rev >= 0 && rev != rec.Rev() {
		return Record{}, Record{}, nil, problems.PreconditionFailed.New("promotion record %s is at revision %d, not %d; read it again", id, rec.Rev(), rev)
	}
	mismatch := func(format string, args ...any) (Record, Record, []events.Draft, error) {
		return Record{}, Record{}, nil, problems.PromotionReceiptMismatch.New(format, args...)
	}
	switch {
	case !Pends(rec.Kind):
		return mismatch("record %s is a %s record; only promotions and rollbacks take a receipt", id, rec.Kind)
	case rec.State == StateConfirmed:
		return mismatch("record %s is already confirmed (%s)", id, rec.ClosedBy)
	case rec.State == StateWithdrawn:
		return mismatch("record %s was withdrawn (%s) because no receipt came in time; promote again for a new record and bundle", id, rec.ClosedBy)
	case !rec.Verified:
		return mismatch("record %s does not verify (%v); no receipt can confirm it", id, rec.Problems)
	}
	for _, later := range c.Records[idx+1:] {
		if Pends(later.Kind) && later.Slot == rec.Slot {
			return mismatch("record %s is not the slot's pending one: %s (%s) came after it on slot %s", id, later.ID, later.Kind, rec.Slot)
		}
	}
	rc, err := ParseReceipt(text)
	if err != nil {
		return mismatch("%v", err)
	}
	if rc.RecordHash != rec.Hash {
		return mismatch("the receipt names record hash %s, but record %s hashes to %s: it comes from another record's script", rc.RecordHash, id, rec.Hash)
	}
	if want := str(rec.Body, "deployable.manifestSha256"); rc.ServedSHA256 != want {
		return mismatch("the installed files hash to %s, but the record approved %s: the model directory on the host is not the approved one", rc.ServedSHA256, want)
	}
	d, err := readDelivery(ctx, tx, id)
	if err != nil {
		return Record{}, Record{}, nil, err
	}
	switch {
	case d == nil || d.State != "ready":
		return mismatch("record %s has no finished delivery bundle, so no script of it can have run", id)
	case rc.SmokeTotal != d.SmokeTotal:
		return mismatch("the receipt counts %d smoke utterances, but the bundle holds %d", rc.SmokeTotal, d.SmokeTotal)
	case rc.SmokeOK < d.SmokeRequired:
		return mismatch("the smoke check passed %d of %d utterances; %d must match the staging text", rc.SmokeOK, rc.SmokeTotal, d.SmokeRequired)
	}
	stage := str(rec.Body, "stage")
	body := map[string]any{
		"closes":      map[string]any{"id": rec.ID, "hash": rec.Hash, "seq": rec.Seq, "kind": rec.Kind},
		"receipt":     rc.Line,
		"served":      map[string]any{"sha256": rc.ServedSHA256, "smoke": map[string]any{"ok": rc.SmokeOK, "total": rc.SmokeTotal}, "host": rc.Host, "ranAt": rc.RanAt.Format(time.RFC3339)},
		"confirmedBy": map[string]any{"id": actor.ID, "name": actor.Name},
	}
	if stage != "" {
		body["stage"] = stage
	}
	conf, drafts, err := s.Append(ctx, tx, AppendInput{Kind: KindConfirmation, TargetID: t.ID, Slot: rec.Slot,
		ProjectID: rec.ProjectID, DeploymentID: rec.DeploymentID, RefersTo: rec.ID, Actor: actor, Body: body})
	if err != nil {
		return Record{}, Record{}, nil, err
	}
	rec.State, rec.ClosedBy = StateConfirmed, conf.ID
	payload := map[string]any{"id": rec.ID, "confirmation": conf.ID, "slot": rec.Slot, "stage": stage, "targetId": t.ID}
	drafts = append(drafts, events.Draft{Topic: targets.Topic(t.ID), Type: EventConfirmed, ProjectID: rec.ProjectID, Payload: payload})
	if rec.DeploymentID != "" {
		drafts = append(drafts, events.Draft{Topic: DeployTopic(rec.DeploymentID), Type: EventConfirmed, ProjectID: rec.ProjectID, Payload: payload})
	}
	if s.Stages != nil {
		more, err := s.Stages.Confirm(ctx, tx, rec, conf)
		if err != nil {
			return Record{}, Record{}, nil, err
		}
		drafts = append(drafts, more...)
	}
	return rec, conf, drafts, nil
}

// WithdrawStale appends a withdrawal to every promotion or rollback older than deploy.delivery_pending_days that
// has no receipt, each in its own transaction, and lets the Stager return its deployment to the stage before
// (02 "Pending and withdrawal"). It returns how many it withdrew.
func (s *Service) WithdrawStale(ctx context.Context) (int, error) {
	days := 7
	if s.Defaults != nil {
		days = s.Defaults().Deploy.DeliveryPendingDays.Value
	}
	cutoff := s.now().Add(-time.Duration(days) * 24 * time.Hour)
	rows, err := s.Pool.Query(ctx, `SELECT r.id FROM promotion_records r WHERE r.kind IN ('promotion', 'rollback')
		AND r.created_at < $1 AND NOT EXISTS (SELECT 1 FROM promotion_records c WHERE c.refers_to = r.id) ORDER BY r.created_at`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("find stale promotions: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("find stale promotions: %w", err)
	}
	n := 0
	for _, id := range ids {
		ok, err := s.withdraw(ctx, id, days)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

func (s *Service) withdraw(ctx context.Context, id string, days int) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var targetID string
	if err := tx.QueryRow(ctx, "SELECT target_id FROM promotion_records WHERE id = $1", id).Scan(&targetID); err != nil {
		return false, fmt.Errorf("read promotion record: %w", err)
	}
	t, err := targets.Lock(ctx, tx, targetID)
	if err != nil {
		return false, err
	}
	var closed bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM promotion_records WHERE refers_to = $1)", id).Scan(&closed); err != nil {
		return false, fmt.Errorf("check promotion record: %w", err)
	}
	if closed {
		return false, nil // a receipt came in meanwhile
	}
	rec, _, err := Get(ctx, tx, id)
	if err != nil {
		return false, err
	}
	body := map[string]any{
		"closes":      map[string]any{"id": rec.ID, "hash": rec.Hash, "seq": rec.Seq, "kind": rec.Kind},
		"pendingDays": days,
	}
	if st := str(rec.Body, "stage"); st != "" {
		body["stage"] = st
	}
	w, drafts, err := s.Append(ctx, tx, AppendInput{Kind: KindWithdrawal, TargetID: t.ID, Slot: rec.Slot, ProjectID: rec.ProjectID,
		DeploymentID: rec.DeploymentID, RefersTo: rec.ID, Actor: System,
		Reason: fmt.Sprintf("no receipt within %d days (deploy.delivery_pending_days)", days), Body: body})
	if err != nil {
		return false, err
	}
	rec.State, rec.ClosedBy = StateWithdrawn, w.ID
	payload := map[string]any{"id": rec.ID, "withdrawal": w.ID, "slot": rec.Slot, "targetId": t.ID}
	drafts = append(drafts, events.Draft{Topic: targets.Topic(t.ID), Type: EventWithdrawn, ProjectID: rec.ProjectID, Payload: payload})
	if rec.DeploymentID != "" {
		drafts = append(drafts, events.Draft{Topic: DeployTopic(rec.DeploymentID), Type: EventWithdrawn, ProjectID: rec.ProjectID, Payload: payload})
	}
	if s.Stages != nil {
		more, err := s.Stages.Withdraw(ctx, tx, rec, w)
		if err != nil {
			return false, err
		}
		drafts = append(drafts, more...)
	}
	if err := events.Append(ctx, tx, System, nil, drafts); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit withdrawal: %w", err)
	}
	if s.Log != nil {
		s.Log.InfoContext(ctx, "promotion withdrawn: no receipt in time", "record", rec.ID, "target", t.Name, "slot", rec.Slot, "withdrawal", w.ID)
	}
	return true, nil
}
